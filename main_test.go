package main

import (
	"reflect"
	"testing"
)

func TestSplitPortArgs(t *testing.T) {
	cases := []struct {
		in    []string
		port  string
		flags []string
	}{
		{[]string{"3000"}, "3000", nil},
		{[]string{"http", "3000"}, "http", []string{"3000"}}, // caller already stripped the verb
		{[]string{"3000", "--server", "1.2.3.4:443", "--plain"}, "3000", []string{"--server", "1.2.3.4:443", "--plain"}},
		{[]string{"--name", "demo", "8081"}, "8081", []string{"--name", "demo"}},
		{[]string{"--plain", "9000"}, "9000", []string{"--plain"}},
	}
	// first case after the mistaken one: runClient receives args AFTER the verb
	cases[1] = struct {
		in    []string
		port  string
		flags []string
	}{[]string{"3000", "--name", "x"}, "3000", []string{"--name", "x"}}

	for _, c := range cases {
		port, flags := splitPortArgs(c.in)
		if port != c.port || !reflect.DeepEqual(flags, c.flags) {
			t.Fatalf("in %#v: port=%q flags=%#v want %q %#v", c.in, port, flags, c.port, c.flags)
		}
	}
}
