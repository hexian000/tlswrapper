// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h3mux

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
)

// errorWriter is an io.Writer that always returns the given error.
type errorWriter struct{ err error }

func (e *errorWriter) Write(_ []byte) (int, error) { return 0, e.err }

func TestReadHandshakeEOF(t *testing.T) {
	_, err := readHandshake(strings.NewReader(""))
	if err == nil {
		t.Fatal("expected error on empty reader, got nil")
	}
	if !errors.Is(err, ErrHandshakeFailed) {
		t.Fatalf("error = %v, want to wrap ErrHandshakeFailed", err)
	}
}

func TestReadHandshakeTooLargeHeader(t *testing.T) {
	var buf bytes.Buffer
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], maxHandshakeMsgSize+1)
	buf.Write(hdr[:])
	_, err := readHandshake(&buf)
	if err == nil {
		t.Fatal("expected error for oversized header, got nil")
	}
	if !errors.Is(err, ErrHandshakeFailed) {
		t.Fatalf("error = %v, want to wrap ErrHandshakeFailed", err)
	}
}

func TestReadHandshakeBodyEOF(t *testing.T) {
	// Header claims 8 bytes but only 2 are present.
	var buf bytes.Buffer
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], 8)
	buf.Write(hdr[:])
	buf.Write([]byte{0, 1})
	_, err := readHandshake(&buf)
	if err == nil {
		t.Fatal("expected error for truncated body, got nil")
	}
	if !errors.Is(err, ErrHandshakeFailed) {
		t.Fatalf("error = %v, want to wrap ErrHandshakeFailed", err)
	}
}

func TestReadHandshakeInvalidJSON(t *testing.T) {
	body := []byte("not-json!")
	var buf bytes.Buffer
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(body)))
	buf.Write(hdr[:])
	buf.Write(body)
	_, err := readHandshake(&buf)
	if err == nil {
		t.Fatal("expected error for invalid JSON body, got nil")
	}
	if !errors.Is(err, ErrHandshakeFailed) {
		t.Fatalf("error = %v, want to wrap ErrHandshakeFailed", err)
	}
}

func TestWriteHandshakeTooLarge(t *testing.T) {
	msg := handshakeMsg{Identity: strings.Repeat("x", maxHandshakeMsgSize+1)}
	err := writeHandshake(io.Discard, msg)
	if err == nil {
		t.Fatal("expected error for oversized message, got nil")
	}
	if !errors.Is(err, ErrHandshakeFailed) {
		t.Fatalf("error = %v, want to wrap ErrHandshakeFailed", err)
	}
}

func TestWriteHandshakeHeaderError(t *testing.T) {
	msg := handshakeMsg{Identity: "test"}
	err := writeHandshake(&errorWriter{err: io.ErrClosedPipe}, msg)
	if err == nil {
		t.Fatal("expected error when writer fails, got nil")
	}
	if !errors.Is(err, ErrHandshakeFailed) {
		t.Fatalf("error = %v, want to wrap ErrHandshakeFailed", err)
	}
}
