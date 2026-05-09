// Package protocol describes the framing used on the control TCP connection
// before it is upgraded to a yamux session.
//
// Wire layout (big-endian):
//
//	client -> server: MAGIC(4) | VER(1) | LEN(2) | HelloJSON
//	server -> client: MAGIC(4) | VER(1) | STATUS(1) | LEN(2) | AckJSON
//
// After a successful handshake both sides switch to yamux on the same conn.
package protocol

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	Version    byte = 1
	maxPayload      = 8 << 10
)

var Magic = [4]byte{'A', 'X', 'R', '1'}

// Hello is sent by the client right after dial.
type Hello struct {
	Token   string `json:"token"`
	Service string `json:"service,omitempty"` // requested service name; required when token is wildcard ("*")
	Version string `json:"version,omitempty"` // client lib version, informational
}

// Ack is the server's response to Hello.
type Ack struct {
	Service string `json:"service,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Status codes for Ack.
const (
	StatusOK         byte = 0
	StatusBadAuth    byte = 1
	StatusConflict   byte = 2 // service already taken by another connection
	StatusBadRequest byte = 3
	StatusInternal   byte = 4
)

func WriteHello(w io.Writer, h Hello) error {
	body, err := json.Marshal(h)
	if err != nil {
		return err
	}
	if len(body) > maxPayload {
		return errors.New("hello payload too large")
	}
	hdr := make([]byte, 4+1+2)
	copy(hdr[:4], Magic[:])
	hdr[4] = Version
	binary.BigEndian.PutUint16(hdr[5:7], uint16(len(body)))
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

func ReadHello(r io.Reader) (Hello, error) {
	var h Hello
	hdr := make([]byte, 4+1+2)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return h, err
	}
	if [4]byte{hdr[0], hdr[1], hdr[2], hdr[3]} != Magic {
		return h, fmt.Errorf("bad magic: %x", hdr[:4])
	}
	if hdr[4] != Version {
		return h, fmt.Errorf("unsupported version: %d", hdr[4])
	}
	n := binary.BigEndian.Uint16(hdr[5:7])
	if int(n) > maxPayload {
		return h, errors.New("hello too large")
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return h, err
	}
	return h, json.Unmarshal(body, &h)
}

func WriteAck(w io.Writer, status byte, a Ack) error {
	body, err := json.Marshal(a)
	if err != nil {
		return err
	}
	if len(body) > maxPayload {
		return errors.New("ack payload too large")
	}
	hdr := make([]byte, 4+1+1+2)
	copy(hdr[:4], Magic[:])
	hdr[4] = Version
	hdr[5] = status
	binary.BigEndian.PutUint16(hdr[6:8], uint16(len(body)))
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

func ReadAck(r io.Reader) (byte, Ack, error) {
	var a Ack
	hdr := make([]byte, 4+1+1+2)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return 0, a, err
	}
	if [4]byte{hdr[0], hdr[1], hdr[2], hdr[3]} != Magic {
		return 0, a, fmt.Errorf("bad magic: %x", hdr[:4])
	}
	if hdr[4] != Version {
		return 0, a, fmt.Errorf("unsupported version: %d", hdr[4])
	}
	status := hdr[5]
	n := binary.BigEndian.Uint16(hdr[6:8])
	if int(n) > maxPayload {
		return 0, a, errors.New("ack too large")
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, a, err
	}
	if err := json.Unmarshal(body, &a); err != nil {
		return 0, a, err
	}
	return status, a, nil
}
