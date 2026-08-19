package config

import "testing"

func TestParseServerDefaultsToHTTPS(t *testing.T) {
	cases := []struct {
		in    string
		dial  string
		https bool
	}{
		{"34.239.87.73", "34.239.87.73:443", true},
		{"34.239.87.73:443", "34.239.87.73:443", true},
		{"https://34.239.87.73", "34.239.87.73:443", true},
		{"https://34.239.87.73:443", "34.239.87.73:443", true},
		{"http://127.0.0.1:18443", "127.0.0.1:18443", false},
		{"http://127.0.0.1", "127.0.0.1:80", false},
		{"# comment", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got := ParseServer(c.in)
		if got.Dial != c.dial || got.HTTPS != c.https {
			t.Fatalf("%q: got %+v want dial=%s https=%v", c.in, got, c.dial, c.https)
		}
	}
}
