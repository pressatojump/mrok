package autostart

import (
	"strings"
	"testing"
)

func TestLaunchdPlist(t *testing.T) {
	p := LaunchdPlist("/usr/local/bin/mrok")
	for _, want := range []string{
		"<string>com.mrok.tunnels</string>",
		"<string>/usr/local/bin/mrok</string>",
		"<string>up</string>",
		"<key>RunAtLoad</key>",
		"<true/>",
		"<key>KeepAlive</key>",
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("plist missing %q", want)
		}
	}
}

func TestSystemdUnit(t *testing.T) {
	u := SystemdUnit("/usr/local/bin/mrok")
	if !strings.Contains(u, "ExecStart=/usr/local/bin/mrok up") {
		t.Fatalf("unit: %s", u)
	}
	if !strings.Contains(u, "WantedBy=default.target") {
		t.Fatal("missing install target")
	}
}

func TestUpsert(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if err := Upsert(Tunnel{Name: "aaa", Proto: "http", Local: "127.0.0.1:3000"}); err != nil {
		t.Fatal(err)
	}
	if err := Upsert(Tunnel{Name: "aaa", Proto: "http", Local: "127.0.0.1:3001"}); err != nil {
		t.Fatal(err)
	}
	ts := Load()
	if len(ts) != 1 || ts[0].Local != "0.0.0.0:3001" {
		t.Fatalf("%+v", ts)
	}
}

func TestUpsertKeepsURLAndToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if err := Upsert(Tunnel{Name: "x", Proto: "http", Local: "0.0.0.0:8000", Token: "tok", URL: "https://x.example"}); err != nil {
		t.Fatal(err)
	}
	if err := Upsert(Tunnel{Name: "x", Proto: "http", Local: "0.0.0.0:8000"}); err != nil {
		t.Fatal(err)
	}
	got, ok := Find("127.0.0.1:8000", "http")
	if !ok || got.Token != "tok" || got.URL != "https://x.example" {
		t.Fatalf("%+v ok=%v", got, ok)
	}
}

func TestFindTreatsAnyAndLoopbackAsSamePort(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if err := Upsert(Tunnel{Name: "x", Proto: "http", Local: "127.0.0.1:8000"}); err != nil {
		t.Fatal(err)
	}
	got, ok := Find("0.0.0.0:8000", "http")
	if !ok || got.Name != "x" {
		t.Fatalf("find: %+v ok=%v", got, ok)
	}
}
