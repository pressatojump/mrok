package proto

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/hashicorp/yamux"
)

const (
	Version   = 1
	MaxFrame  = 1 << 20
	ALPN      = "mrok"
	ProtoHTTP = "http"
	ProtoTCP  = "tcp"
	// StreamWindow fits chat history and image payloads.
	// Yamux's 256KiB default shows up as HTTP 413 on /v1/chat/completions.
	StreamWindow = 16 << 20
)

func YamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 15 * time.Second
	cfg.MaxStreamWindowSize = StreamWindow
	cfg.LogOutput = io.Discard
	return cfg
}

type Hello struct {
	V     int    `json:"v"`
	Token string `json:"token,omitempty"`
	Proto string `json:"proto"`
	Name  string `json:"name,omitempty"`
	Local string `json:"local,omitempty"`
}

type Welcome struct {
	ID    string `json:"id"`
	URL   string `json:"url"`
	Error string `json:"error,omitempty"`
}

func WriteJSON(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > MaxFrame {
		return fmt.Errorf("frame too large: %d", len(b))
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(b)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

func ReadJSON(r io.Reader, v any) error {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 || n > MaxFrame {
		return fmt.Errorf("invalid frame length %d", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return err
	}
	return json.Unmarshal(buf, v)
}
