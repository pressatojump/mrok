package config

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
)

const EndpointURL = "https://raw.githubusercontent.com/pressatojump/mrok/main/endpoint"

type File struct {
	Server string `json:"server,omitempty"`
	Token  string `json:"token,omitempty"`
}

func Dir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".mrok"
	}
	return filepath.Join(home, ".mrok")
}

func Path() string {
	return filepath.Join(Dir(), "config.json")
}

func Load() File {
	b, err := os.ReadFile(Path())
	if err != nil {
		return File{}
	}
	var f File
	_ = json.Unmarshal(b, &f)
	return f
}

func Save(f File) error {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(Path(), append(b, '\n'), 0o600)
}

func KnownHostsPath() string {
	return filepath.Join(Dir(), "known_hosts")
}

// Server is a relay address. Missing scheme means HTTPS :443.
type Server struct {
	Dial  string // host:port
	HTTPS bool
	Raw   string
}

func ParseServer(s string) Server {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	s = strings.TrimRight(s, "/")
	if s == "" || strings.HasPrefix(s, "#") {
		return Server{}
	}
	https := true
	low := strings.ToLower(s)
	switch {
	case strings.HasPrefix(low, "http://"):
		https = false
		s = s[len("http://"):]
	case strings.HasPrefix(low, "https://"):
		s = s[len("https://"):]
	}
	s = strings.TrimRight(strings.TrimSpace(s), "/")
	if s == "" {
		return Server{}
	}
	if _, _, err := net.SplitHostPort(s); err != nil {
		if https {
			s = net.JoinHostPort(s, "443")
		} else {
			s = net.JoinHostPort(s, "80")
		}
	}
	return Server{Dial: s, HTTPS: https, Raw: s}
}

func NormalizeServer(s string) string {
	return ParseServer(s).Dial
}
