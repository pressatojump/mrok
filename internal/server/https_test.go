package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPURLUsesHTTPSWhenACME(t *testing.T) {
	plain := New(Config{Plain: true, PublicIP: "34.239.87.73"})
	if got := plain.HTTPURL("70n6l2"); got != "http://70n6l2.34-239-87-73.sslip.io" {
		t.Fatalf("plain url %s", got)
	}
	sec := New(Config{ACME: true, PublicIP: "34.239.87.73", ACMEDir: t.TempDir()})
	if got := sec.HTTPURL("70n6l2"); got != "https://70n6l2.34-239-87-73.sslip.io" {
		t.Fatalf("acme url %s", got)
	}
}

func TestAllowHost(t *testing.T) {
	s := New(Config{ACME: true, PublicIP: "34.239.87.73", ACMEDir: t.TempDir()})
	if err := s.allowHost(context.Background(), "70n6l2.34-239-87-73.sslip.io"); err != nil {
		t.Fatal(err)
	}
	if err := s.allowHost(context.Background(), "34-239-87-73.sslip.io"); err != nil {
		t.Fatal(err)
	}
	if err := s.allowHost(context.Background(), "evil.example.com"); err == nil {
		t.Fatal("expected reject")
	}
	if err := s.allowHost(context.Background(), "a.b.34-239-87-73.sslip.io"); err == nil {
		t.Fatal("expected reject extra labels")
	}
}

func TestHTTPRedirectsToHTTPS(t *testing.T) {
	s := New(Config{ACME: true, PublicIP: "34.239.87.73", ACMEDir: t.TempDir()})
	req := httptest.NewRequest(http.MethodGet, "http://70n6l2.34-239-87-73.sslip.io/path?q=1", nil)
	req.Host = "70n6l2.34-239-87-73.sslip.io"
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, req)
	if rr.Code != http.StatusMovedPermanently {
		t.Fatalf("status %d", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != "https://70n6l2.34-239-87-73.sslip.io/path?q=1" {
		t.Fatalf("location %s", loc)
	}
}

func TestRewriteOpenAIPath(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://x/v1/v1/models", nil)
	rewriteOpenAIPath(req)
	if req.URL.Path != "/v1/models" {
		t.Fatalf("path %s", req.URL.Path)
	}
	req = httptest.NewRequest(http.MethodGet, "http://x/v1/chat/completions", nil)
	rewriteOpenAIPath(req)
	if req.URL.Path != "/v1/chat/completions" {
		t.Fatalf("untouched %s", req.URL.Path)
	}
}

func TestServeOpenAIRoot(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://x/v1", nil)
	rr := httptest.NewRecorder()
	if !serveOpenAIRoot(rr, req) || rr.Code != 200 {
		t.Fatalf("status %d handled", rr.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "http://x/v1/chat/completions", nil)
	rr = httptest.NewRecorder()
	if serveOpenAIRoot(rr, req) {
		t.Fatal("should not swallow chat")
	}
}

func TestHealthzNotRedirected(t *testing.T) {
	s := New(Config{ACME: true, PublicIP: "34.239.87.73", ACMEDir: t.TempDir()})
	req := httptest.NewRequest(http.MethodGet, "http://34.239.87.73/healthz", nil)
	req.Host = "34.239.87.73"
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, req)
	if rr.Code != 200 || rr.Body.String() != "ok\n" {
		t.Fatalf("status %d body %q", rr.Code, rr.Body.String())
	}
}
