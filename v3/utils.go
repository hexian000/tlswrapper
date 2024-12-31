// tlswrapper (c) 2021-2025 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package tlswrapper

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/hexian000/gosnippets/formats"
	"github.com/hexian000/gosnippets/routines"
	"github.com/hexian000/gosnippets/slog"
	"github.com/hexian000/tlswrapper/v3/config"
)

func ioClose(c io.Closer) {
	if err := c.Close(); err != nil {
		msg := fmt.Sprintf("close: %s", formats.Error(err))
		slog.Output(2, slog.LevelWarning, nil, msg)
	}
}

func dumpConfig(f *AppFlags) int {
	cfg, err := config.LoadFile(f.Config)
	if err != nil {
		slog.Fatal("dumpconfig: ", formats.Error(err))
		return 1
	}
	b, err := cfg.Dump()
	if err != nil {
		slog.Fatal("dumpconfig: ", formats.Error(err))
		return 1
	}
	println(string(b))
	return 0
}

type keyGenerator func() (pubKey any, key any, err error)

func newCertificate(parent *x509.Certificate, signKey any, sni string, pubKey any, key any) (certPEM []byte, keyPEM []byte, err error) {
	rawKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		err = fmt.Errorf("PKCS8 private key: %s", formats.Error(err))
		return
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: rawKey,
	})

	now := time.Now()
	tmpl := x509.Certificate{
		NotBefore:    now,
		NotAfter:     now.AddDate(0, 0, 36500),
		SerialNumber: big.NewInt(now.UnixNano()),
		Subject: pkix.Name{
			Country:            []string{"US"},
			Province:           []string{"California"},
			Locality:           []string{"Mountain View"},
			Organization:       []string{"Your Organization"},
			OrganizationalUnit: []string{"Your Unit"},
			CommonName:         sni,
		},
		DNSNames: []string{sni},
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
			x509.ExtKeyUsageClientAuth,
		},
	}
	if parent == nil {
		// create self-signed certificate
		parent = &tmpl
		tmpl.BasicConstraintsValid = true
		tmpl.IsCA = true
		signKey = key
	}
	rawCert, err := x509.CreateCertificate(rand.Reader, &tmpl, parent, pubKey, signKey)
	if err != nil {
		return
	}
	certPEM = pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: rawCert,
	})
	return
}

func makeKeyGenerator(keytype string, keysize int) (keyGenerator, error) {
	switch keytype {
	case "rsa":
		bits := keysize
		if bits == 0 {
			bits = 4096
		}
		slog.Noticef("gencerts: keytype=%q keysize=%d", keytype, bits)
		return func() (any, any, error) {
			key, err := rsa.GenerateKey(rand.Reader, bits)
			if err != nil {
				return nil, nil, err
			}
			return &key.PublicKey, key, nil
		}, nil
	case "ecdsa":
		if keysize == 0 {
			keysize = 256
		}
		var curve elliptic.Curve
		switch keysize {
		case 224:
			curve = elliptic.P224()
		case 256:
			curve = elliptic.P256()
		case 384:
			curve = elliptic.P384()
		case 521:
			curve = elliptic.P521()
		default:
			return nil, errors.New("invalid key size")
		}
		slog.Noticef("gencerts: keytype=%q keysize=%d", keytype, keysize)
		return func() (any, any, error) {
			key, err := ecdsa.GenerateKey(curve, rand.Reader)
			if err != nil {
				return nil, nil, err
			}
			return &key.PublicKey, key, err
		}, nil
	case "ed25519":
		slog.Noticef("gencerts: keytype=%q", keytype)
		return func() (any, any, error) {
			pubKey, key, err := ed25519.GenerateKey(rand.Reader)
			return pubKey, key, err
		}, nil
	}
	return nil, errors.New("invalid key type")
}

func parsePEM(data []byte, blockType string) []byte {
	var p *pem.Block
	b := data
	for {
		p, b = pem.Decode(b)
		if p == nil || p.Type == blockType {
			return p.Bytes
		}
	}
}

func readKeyPair(name string) (*x509.Certificate, any, error) {
	certFile, keyFile := name+"-cert.pem", name+"-key.pem"
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return nil, nil, err
	}
	certDER := parsePEM(certPEM, "CERTIFICATE")
	if certDER == nil {
		return nil, nil, fmt.Errorf("%s: certificate not found", certFile)
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, nil, err
	}
	keyDER := parsePEM(keyPEM, "PRIVATE KEY")
	if keyDER == nil {
		return nil, nil, fmt.Errorf("%s: private key not found", keyFile)
	}
	key, err := x509.ParsePKCS8PrivateKey(keyDER)
	if err != nil {
		return nil, nil, err
	}
	slog.Noticef("gencerts: read %q, %q", certFile, keyFile)
	return cert, key, nil
}

func writeKeyPair(name string, certPEM, keyPEM []byte) error {
	certFile, keyFile := name+"-cert.pem", name+"-key.pem"
	if err := os.WriteFile(certFile, certPEM, 0644); err != nil {
		return err
	}
	if err := os.WriteFile(keyFile, keyPEM, 0600); err != nil {
		return err
	}
	slog.Noticef("gencerts: write %q, %q", certFile, keyFile)
	return nil
}

func genCerts(f *AppFlags) int {
	keygen, err := makeKeyGenerator(f.KeyType, f.KeySize)
	if err != nil {
		slog.Fatalf("gencerts: %s", formats.Error(err))
		return 1
	}
	var parent *x509.Certificate
	var signKey any
	if f.Sign != "" {
		parent, signKey, err = readKeyPair(f.Sign)
		if err != nil {
			slog.Fatalf("gencerts: read certificate: %s", formats.Error(err))
			return 1
		}
	}
	g := routines.NewGroup()
	for _, name := range strings.Split(f.GenCerts, ",") {
		name := name
		if err := g.Go(func() {
			pubKey, key, err := keygen()
			if err != nil {
				panic(fmt.Sprintf("generate key: %s", formats.Error(err)))
			}
			certPEM, keyPEM, err := newCertificate(parent, signKey, f.ServerName, pubKey, key)
			if err != nil {
				panic(fmt.Sprintf("gencerts %q: %s", name, formats.Error(err)))
			}
			if err := writeKeyPair(name, certPEM, keyPEM); err != nil {
				panic(fmt.Sprintf("gencerts %q: %s", name, formats.Error(err)))
			}
		}); err != nil {
			panic(err)
		}
	}
	if err := g.Wait(); err != nil {
		return 1
	}
	slog.Notice("gencerts: ok")
	return 0
}
