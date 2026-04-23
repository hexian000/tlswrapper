// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h2mux

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
)

var (
	mimeType    = "application/x-tlswrapper-proto"
	mimeVersion = "4"

	// Type is the protocol message type identifier in MIME format
	Type = mime.FormatMediaType(mimeType, map[string]string{"version": mimeVersion})
)

const (
	MsgClientHello = iota
	MsgServerHello
)

// ServiceExt carries optional service identity in a protocol message.
type ServiceExt struct {
	ID string `json:"id,omitempty"`
}

// Message represents a protocol message
type Message struct {
	Type       string `json:"type"`
	Msg        int    `json:"msgid"`
	Extensions struct {
		RejectInbound bool        `json:"reject_inbound,omitempty"`
		Service       *ServiceExt `json:"service,omitempty"`
	} `json:"extensions,omitempty"`
}

var (
	ErrUnsupportedProtocol  = errors.New("unsupported protocol")
	ErrIncompatiableVersion = errors.New("incompatible protocol version")
)

// ReadFrom decodes a Message from r using plain JSON (no binary framing).
// Used for HTTP body decoding.
func ReadFrom(r io.Reader) (*Message, error) {
	var msg Message
	if err := json.NewDecoder(r).Decode(&msg); err != nil {
		return nil, err
	}
	if err := checkType(msg.Type); err != nil {
		return nil, err
	}
	return &msg, nil
}

// WriteTo encodes msg to w using plain JSON (no binary framing).
// Used for HTTP body encoding.
func WriteTo(w io.Writer, msg *Message) error {
	return json.NewEncoder(w).Encode(msg)
}

func checkType(s string) error {
	mediatype, params, err := mime.ParseMediaType(s)
	if err != nil {
		return err
	}
	if mediatype != mimeType {
		return ErrUnsupportedProtocol
	}
	version, ok := params["version"]
	if !ok {
		return ErrUnsupportedProtocol
	}
	if version != mimeVersion {
		return ErrIncompatiableVersion
	}
	return nil
}
