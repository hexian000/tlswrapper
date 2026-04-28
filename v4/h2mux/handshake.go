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
	protoMimeType    = "application/x-tlswrapper-proto"
	protoMimeVersion = "4"

	// protoType is the protocol message type identifier in MIME format
	protoType = mime.FormatMediaType(protoMimeType, map[string]string{"version": protoMimeVersion})
)

const (
	msgClientHello = iota
	msgServerHello
)

// serviceExt carries optional service identity in a protocol message.
type serviceExt struct {
	ID string `json:"id,omitempty"`
}

// message represents a protocol message
type message struct {
	Type       string `json:"type"`
	Msg        int    `json:"msgid"`
	Extensions struct {
		RejectInbound bool        `json:"reject_inbound,omitempty"`
		Service       *serviceExt `json:"service,omitempty"`
	} `json:"extensions,omitempty"`
}

var (
	errUnsupportedProtocol = errors.New("unsupported protocol")
	errIncompatibleVersion = errors.New("incompatible protocol version")
)

// readFrom decodes a message from r using plain JSON (no binary framing).
func readFrom(r io.Reader) (*message, error) {
	var msg message
	if err := json.NewDecoder(r).Decode(&msg); err != nil {
		return nil, err
	}
	if err := checkProtoType(msg.Type); err != nil {
		return nil, err
	}
	return &msg, nil
}

// writeTo encodes msg to w using plain JSON (no binary framing).
func writeTo(w io.Writer, msg *message) error {
	return json.NewEncoder(w).Encode(msg)
}

func checkProtoType(s string) error {
	mediatype, params, err := mime.ParseMediaType(s)
	if err != nil {
		return err
	}
	if mediatype != protoMimeType {
		return errUnsupportedProtocol
	}
	version, ok := params["version"]
	if !ok {
		return errUnsupportedProtocol
	}
	if version != protoMimeVersion {
		return errIncompatibleVersion
	}
	return nil
}
