package autostart

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/pressatojump/mrok/internal/config"
)

const (
	launchLabel = "com.mrok.tunnels"
	unitName    = "mrok.service"
)

type Tunnel struct {
	Name   string `json:"name"`
	Proto  string `json:"proto"`
	Local  string `json:"local"`
	Server string `json:"server,omitempty"`
	Token  string `json:"token,omitempty"`
	URL    string `json:"url,omitempty"`
}

func tunnelsPath() string {
	return filepath.Join(config.Dir(), "tunnels.json")
}

func Load() []Tunnel {
	b, err := os.ReadFile(tunnelsPath())
	if err != nil {
		return nil
	}
	var ts []Tunnel
	if json.Unmarshal(b, &ts) != nil {
		return nil
	}
	for i := range ts {
		ts[i].Local = normalizeLocal(ts[i].Local)
	}
	return ts
}

func Save(ts []Tunnel) error {
	if err := os.MkdirAll(config.Dir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(ts, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(tunnelsPath(), append(b, '\n'), 0o600)
}

func Find(local, proto string) (Tunnel, bool) {
	for _, t := range Load() {
		if t.Proto == proto && t.Name != "" && sameLocal(t.Local, local) {
			return t, true
		}
	}
	return Tunnel{}, false
}

func normalizeLocal(s string) string {
	if s == "" {
		return s
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return s
	}
	if anyOrLoop(host) {
		return net.JoinHostPort("0.0.0.0", port)
	}
	return s
}

func sameLocal(a, b string) bool {
	return normalizeLocal(a) == normalizeLocal(b) && a != "" && b != ""
}

func anyOrLoop(h string) bool {
	switch h {
	case "", "0.0.0.0", "*", "[::]", "::", "127.0.0.1", "localhost", "::1", "[::1]":
		return true
	default:
		return false
	}
}

func Upsert(t Tunnel) error {
	t.Local = normalizeLocal(t.Local)
	ts := Load()
	found := false
	for i := range ts {
		if ts[i].Name == t.Name || (ts[i].Proto == t.Proto && sameLocal(ts[i].Local, t.Local)) {
			if t.Token == "" {
				t.Token = ts[i].Token
			}
			if t.URL == "" {
				t.URL = ts[i].URL
			}
			if t.Server == "" {
				t.Server = ts[i].Server
			}
			ts[i] = t
			found = true
			break
		}
	}
	if !found {
		ts = append(ts, t)
	}
	return Save(ts)
}

func Binary() string {
	if p, err := os.Executable(); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
		return p
	}
	if p, err := exec.LookPath("mrok"); err == nil {
		return p
	}
	return "mrok"
}

func LaunchdPlist(bin string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>up</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
		<key>Crashed</key>
		<true/>
	</dict>
	<key>ThrottleInterval</key>
	<integer>5</integer>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, launchLabel, bin, filepath.Join(config.Dir(), "up.log"), filepath.Join(config.Dir(), "up.log"))
}

func SystemdUnit(bin string) string {
	return fmt.Sprintf(`[Unit]
Description=mrok tunnels
After=network-online.target

[Service]
ExecStart=%s up
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
`, bin)
}

func Enable() error {
	if err := os.MkdirAll(config.Dir(), 0o700); err != nil {
		return err
	}
	bin := Binary()
	switch runtime.GOOS {
	case "darwin":
		return enableLaunchd(bin)
	case "linux":
		return enableSystemd(bin)
	case "windows":
		return enableWindows(bin)
	default:
		return fmt.Errorf("autostart: unsupported os %s", runtime.GOOS)
	}
}

func enableLaunchd(bin string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	plist := filepath.Join(dir, launchLabel+".plist")
	if err := os.WriteFile(plist, []byte(LaunchdPlist(bin)), 0o644); err != nil {
		return err
	}
	// Write the plist only. Do not bootout/load — a running `mrok up`
	// keeps other tunnels alive; flock stops the same name doubling.
	return nil
}

func enableSystemd(bin string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	unit := filepath.Join(dir, unitName)
	if err := os.WriteFile(unit, []byte(SystemdUnit(bin)), 0o644); err != nil {
		return err
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	if err := exec.Command("systemctl", "--user", "enable", unitName).Run(); err != nil {
		desktopDir := filepath.Join(home, ".config", "autostart")
		_ = os.MkdirAll(desktopDir, 0o755)
		desktop := fmt.Sprintf("[Desktop Entry]\nType=Application\nName=mrok\nExec=%s up\nX-GNOME-Autostart-enabled=true\n", bin)
		return os.WriteFile(filepath.Join(desktopDir, "mrok.desktop"), []byte(desktop), 0o644)
	}
	return nil
}

func enableWindows(bin string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	startup := filepath.Join(home, "AppData", "Roaming", "Microsoft", "Windows", "Start Menu", "Programs", "Startup")
	if err := os.MkdirAll(startup, 0o755); err != nil {
		return err
	}
	cmd := fmt.Sprintf("@echo off\r\nstart \"\" \"%s\" up\r\n", bin)
	return os.WriteFile(filepath.Join(startup, "mrok.cmd"), []byte(cmd), 0o644)
}

func Disable() {
	switch runtime.GOOS {
	case "darwin":
		home, _ := os.UserHomeDir()
		plist := filepath.Join(home, "Library", "LaunchAgents", launchLabel+".plist")
		uid := strconv.Itoa(os.Getuid())
		_ = exec.Command("launchctl", "bootout", "gui/"+uid+"/"+launchLabel).Run()
		_ = exec.Command("launchctl", "unload", plist).Run()
		_ = os.Remove(plist)
	case "linux":
		_ = exec.Command("systemctl", "--user", "disable", "--now", unitName).Run()
		home, _ := os.UserHomeDir()
		_ = os.Remove(filepath.Join(home, ".config", "systemd", "user", unitName))
		_ = os.Remove(filepath.Join(home, ".config", "autostart", "mrok.desktop"))
	case "windows":
		home, _ := os.UserHomeDir()
		_ = os.Remove(filepath.Join(home, "AppData", "Roaming", "Microsoft", "Windows", "Start Menu", "Programs", "Startup", "mrok.cmd"))
	}
}
