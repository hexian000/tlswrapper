package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

const (
	headerSize    = 8
	maxPacketSize = 65535
	saltSize      = 16
)

var errAuthFailure = errors.New("authenticate failure")

type packetHeader struct {
	Sequence uint32
	Length   uint16
	Reserved uint16
}

func readHeader(data []byte) *packetHeader {
	return &packetHeader{
		binary.BigEndian.Uint32(data[0:4]),
		binary.BigEndian.Uint16(data[4:6]),
		binary.BigEndian.Uint16(data[6:8]),
	}
}

func makeHeader(data []byte, h *packetHeader) {
	binary.BigEndian.PutUint32(data[0:4], h.Sequence)
	binary.BigEndian.PutUint16(data[4:6], h.Length)
	binary.BigEndian.PutUint16(data[6:8], h.Reserved)
}

func randFill(buf []byte) {
	_, err := io.ReadFull(rand.Reader, buf)
	if err != nil {
		log.Panicln("crypto rand:", err)
	}
}

func (c *Config) loadKey(salt []byte, keySize uint32) []byte {
	const (
		opsLimit    = 4
		memLimit    = 8
		parallelism = 1
	)
	return argon2.IDKey([]byte(c.Password), salt, opsLimit, memLimit, parallelism, keySize)
}

// NewAEAD initialiazes AEAD for config
func (c *Config) NewAEAD(salt []byte) (cipher.AEAD, error) {
	if c.Password == "" {
		return nil, errors.New("password is required")
	}
	switch c.Cipher {
	case "chacha20poly1305":
		return chacha20poly1305.New(c.loadKey(salt, chacha20poly1305.KeySize))
	case "aes256gcm":
		block, err := aes.NewCipher(c.loadKey(salt, 32))
		if err != nil {
			return nil, err
		}
		return cipher.NewGCM(block)
	}
	return nil, errors.New("unsupported cipher: " + c.Cipher)
}

func writeAll(w net.Conn, b []byte) (err error) {
	var n int
	for n < len(b) && err == nil {
		n, err = w.Write(b[n:])
	}
	if n < len(b) {
		return &net.OpError{
			Op:     "write",
			Net:    network,
			Source: w.LocalAddr(),
			Addr:   w.RemoteAddr(),
			Err:    errors.New("short write"),
		}
	}
	return err
}

// Protocol serves a session
type Protocol interface {
	Accept(conn net.Conn)
}

// CryptoSession contains information about a session
type CryptoSession struct {
	*Config

	NoDelay       bool
	Tag           []byte
	ReadSequence  uint32
	WriteSequence uint32

	Closer sync.Once
}

// ServerProtocol is the server-side TCPCrypt protocol
type ServerProtocol struct {
	*Config
}

// ClientProtocol is the client-side TCPCrypt protocol
type ClientProtocol struct {
	*Config
}

// Accept run a server protocol
func (p *ServerProtocol) Accept(conn net.Conn) {
	p.SetConnParams(conn)
	dial, err := net.Dial(network, p.Dial)
	if err != nil {
		log.Println("session dial:", err)
		_ = conn.Close()
		return
	}
	log.Println("connection:", conn.RemoteAddr(), "<->", dial.RemoteAddr())
	s := &CryptoSession{
		Config:        p.Config,
		NoDelay:       p.Config.NoDelay,
		Tag:           []byte(p.Config.Tag),
		ReadSequence:  0,
		WriteSequence: 0,
	}
	go s.decryptSession(conn, dial)
	go s.encryptSession(dial, conn)
}

// Accept run a client protocol
func (p *ClientProtocol) Accept(conn net.Conn) {
	dial, err := net.Dial(network, p.Dial)
	if err != nil {
		log.Println("session dial:", err)
		_ = conn.Close()
		return
	}
	p.SetConnParams(dial)
	log.Println("connection:", conn.RemoteAddr(), "<->", dial.RemoteAddr())
	s := &CryptoSession{
		Config:        p.Config,
		NoDelay:       p.Config.NoDelay,
		Tag:           []byte(p.Config.Tag),
		ReadSequence:  0,
		WriteSequence: 0,
	}
	go s.encryptSession(conn, dial)
	go s.decryptSession(dial, conn)
}

func readAsMuch(conn net.Conn, buf []byte, timeout time.Duration) (n int, err error) {
	err = conn.SetReadDeadline(time.Time{})
	if err != nil {
		return
	}
	n, err = conn.Read(buf)
	if err != nil {
		return
	}
	err = conn.SetReadDeadline(time.Now().Add(timeout))
	if err != nil {
		return
	}
	for n < len(buf) && err == nil {
		var nn int
		nn, err = conn.Read(buf[n:])
		n += nn
	}
	if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		err = nil
	}
	return
}

func (s *CryptoSession) closeSession(from, to net.Conn) {
	err := recover() // calm down
	s.Closer.Do(func() {
		_ = to.Close()
		if err == errAuthFailure {
			log.Printf("%s: %s", from.RemoteAddr(), errAuthFailure)
			if s.Config.IsServer() {
				const minDelay = 10 * time.Second
				const maxDelay = 100 * time.Second

				var buf [4]byte
				_, _ = rand.Read(buf[:])
				modulo := maxDelay - minDelay
				additional := time.Duration(binary.BigEndian.Uint32(buf[:])) % modulo
				time.Sleep(minDelay + additional)
			} else {
				log.Fatalln("incorrect password?")
			}
		}
		_ = from.Close()
	})
}

func (s *CryptoSession) encryptSession(from net.Conn, to net.Conn) {
	defer s.closeSession(from, to)
	salt := make([]byte, saltSize)
	randFill(salt)
	aead, err := s.Config.NewAEAD(salt)
	if err != nil {
		log.Panicln("crypto init:", err)
	}
	nonceSize := aead.NonceSize()
	overhead := aead.Overhead()
	buf := make([]byte, nonceSize+headerSize+overhead+maxPacketSize+overhead)
	nonce := buf[:nonceSize]
	header := buf[nonceSize : nonceSize+headerSize+overhead]
	payload := buf[nonceSize+headerSize+overhead:]
	for {
		var n int
		var err error
		if s.NoDelay {
			n, err = from.Read(payload[:maxPacketSize])
		} else {
			n, err = readAsMuch(from, payload[:maxPacketSize], 10*time.Millisecond)
		}
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			} else if err == io.EOF {
				log.Println("connection:", from.RemoteAddr(), "-x>", to.RemoteAddr())
				return
			}
			log.Panicln("recv data:", err)
		}
		if n < 1 {
			continue
		}
		if salt != nil {
			err = writeAll(to, salt)
			if err != nil {
				log.Panicln("send salt:", err)
			}
			salt = nil
		}
		randFill(nonce)
		makeHeader(header, &packetHeader{
			Sequence: s.WriteSequence,
			Length:   uint16(n),
			Reserved: 0,
		})
		s.WriteSequence++
		aead.Seal(header[:0], nonce, header[:headerSize], s.Tag)
		aead.Seal(payload[:0], nonce, payload[:n], s.Tag)
		err = writeAll(to, buf[:nonceSize+headerSize+overhead+n+overhead])
		if err != nil {
			log.Panicln("send data:", err)
		}
	}
}

func (s *CryptoSession) decryptSession(from net.Conn, to net.Conn) {
	defer s.closeSession(from, to)
	salt := make([]byte, saltSize)
	_, err := io.ReadFull(from, salt)
	if err != nil {
		if errors.Is(err, net.ErrClosed) {
			return
		} else if err == io.EOF {
			log.Println("connection:", from.RemoteAddr(), "-x>", to.RemoteAddr())
			return
		}
		log.Panicln("recv salt:", err)
	}
	aead, err := s.Config.NewAEAD(salt)
	if err != nil {
		log.Panicln("crypto init:", err)
	}
	nonceSize := aead.NonceSize()
	overhead := aead.Overhead()
	buf := make([]byte, nonceSize+headerSize+overhead+maxPacketSize+overhead)
	nonce := buf[:nonceSize]
	header := buf[nonceSize : nonceSize+headerSize+overhead]
	payload := buf[nonceSize+headerSize+overhead:]
	for {
		_, err = io.ReadFull(from, buf[:nonceSize+headerSize+overhead])
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			} else if err == io.EOF {
				log.Println("connection:", from.RemoteAddr(), "-x>", to.RemoteAddr())
				return
			}
			log.Panicln("recv data:", err)
		}
		_, err = aead.Open(header[:0], nonce, header, s.Tag)
		if err != nil {
			panic(errAuthFailure)
		}
		h := readHeader(header)
		if h.Sequence != s.ReadSequence {
			panic(errAuthFailure)
		}
		s.ReadSequence++
		n := int(h.Length)

		_, err = io.ReadFull(from, payload[:n+overhead])
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			} else if err == io.EOF {
				log.Println("connection:", from.RemoteAddr(), "-x>", to.RemoteAddr())
				return
			}
			log.Panicln(err)
		}
		_, err = aead.Open(payload[:0], nonce, payload[:n+overhead], s.Tag)
		if err != nil {
			panic(errAuthFailure)
		}
		err = writeAll(to, payload[:n])
		if err != nil {
			log.Panicln("send data:", err)
		}
	}
}
