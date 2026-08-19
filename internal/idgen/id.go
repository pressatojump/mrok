package idgen

import (
	"crypto/rand"
	"fmt"
	"io"
	"regexp"
	"strings"
)

const (
	alphabet  = "abcdefghijklmnopqrstuvwxyz0123456789"
	RandomLen = 48
	VanityMax = 16
	MaxLen    = 63
)

var valid = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)

func Random(n int) string {
	if n < 2 {
		n = RandomLen
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		panic(err)
	}
	out := make([]byte, n)
	for i := range b {
		out[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(out)
}

func Token() string {
	b := make([]byte, 24)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x", b)
}

func Normalize(name string) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return Random(RandomLen), nil
	}
	if len(name) > MaxLen || !valid.MatchString(name) {
		return "", fmt.Errorf("name %q must be 3-%d chars of [a-z0-9-]", name, MaxLen)
	}
	switch name {
	case "www", "mrok", "api", "control", "health", "healthz", "install":
		return "", fmt.Errorf("name %q is reserved", name)
	}
	return name, nil
}

func Valid(name string) bool {
	return valid.MatchString(name) && len(name) <= MaxLen
}

func Vanity(name string) bool {
	return name != "" && len(name) < VanityMax
}
