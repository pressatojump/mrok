//go:build unix

package autostart

import (
	"os"
	"path/filepath"
	"syscall"

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
	f, err := os.OpenFile(filepath.Join(dir, name+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}
