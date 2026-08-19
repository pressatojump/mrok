package idgen

import "testing"

func TestNormalize(t *testing.T) {
	got, err := Normalize("Demo-App")
	if err != nil {
		t.Fatal(err)
	}
	if got != "demo-app" {
		t.Fatalf("got %q", got)
	}
	if _, err := Normalize("www"); err == nil {
		t.Fatal("expected reserved")
	}
	if _, err := Normalize("A"); err == nil {
		t.Fatal("expected too short")
	}
}

func TestRandom(t *testing.T) {
	a, b := Random(RandomLen), Random(RandomLen)
	if a == b {
		t.Fatal("expected distinct ids")
	}
	if len(a) != RandomLen || !Valid(a) {
		t.Fatalf("invalid %q len=%d", a, len(a))
	}
}

func TestLongNameValid(t *testing.T) {
	n := Random(48)
	if !Valid(n) {
		t.Fatalf("48-char should be valid: %s", n)
	}
	if Vanity(n) {
		t.Fatal("long id is not vanity")
	}
	if !Vanity("demo") {
		t.Fatal("demo is vanity")
	}
}
