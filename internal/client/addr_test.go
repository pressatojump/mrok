package client

import "testing"

func TestConnectAddr(t *testing.T) {
	cases := map[string]string{
		"0.0.0.0:8000":   "127.0.0.1:8000",
		"*:8000":         "127.0.0.1:8000",
		"[::]:8000":      "[::1]:8000",
		"127.0.0.1:8000": "127.0.0.1:8000",
		"10.0.0.5:9000":  "10.0.0.5:9000",
		"8000":           "127.0.0.1:8000",
	}
	for in, want := range cases {
		if got := ConnectAddr(in); got != want {
			t.Fatalf("%s: got %s want %s", in, got, want)
		}
	}
}
