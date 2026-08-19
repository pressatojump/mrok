package proto

import (
	"bytes"
	"testing"
)

func TestWriteReadJSON(t *testing.T) {
	var buf bytes.Buffer
	in := Hello{V: Version, Proto: ProtoHTTP, Name: "demo", Local: "127.0.0.1:3000"}
	if err := WriteJSON(&buf, in); err != nil {
		t.Fatal(err)
	}
	var out Hello
	if err := ReadJSON(&buf, &out); err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Fatalf("got %+v want %+v", out, in)
	}
}

func TestReadJSONRejectsHugeFrame(t *testing.T) {
	var buf bytes.Buffer
	buf.Write([]byte{0xff, 0xff, 0xff, 0xff})
	var out Hello
	if err := ReadJSON(&buf, &out); err == nil {
		t.Fatal("expected error")
	}
}

func TestWelcomeRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	in := Welcome{ID: "abc123", URL: "http://abc123.example"}
	if err := WriteJSON(&buf, in); err != nil {
		t.Fatal(err)
	}
	var out Welcome
	if err := ReadJSON(&buf, &out); err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Fatalf("got %+v want %+v", out, in)
	}
}
