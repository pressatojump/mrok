//go:build windows

package autostart

import (
	"os"
	"path/filepath"

	"github.com/pressatojump/mrok/internal/config"
)

func TryLock(name string) (*os.File, error) {
	if name == "" {
		name = "default"
	}
	dir := filepath.Join(config.Dir(), "locks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(filepath.Join(dir, name+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
}
