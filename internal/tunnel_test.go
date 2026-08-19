package internal_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pressatojump/mrok/internal/client"
	"github.com/pressatojump/mrok/internal/proto"
	"github.com/pressatojump/mrok/internal/server"
)

func TestHTTPTunnel(t *testing.T) {
	backend := http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hello" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, "hello-mrok")
	})}
	bln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	go backend.Serve(bln)

	srv := server.New(server.Config{
		HTTPAddr:    "127.0.0.1:0",
		ControlAddr: "127.0.0.1:0",
		Plain:       true,
		Open:        true,
		Domain:      "127.0.0.1.nip.io",
		MaxTunnels:  8,
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe(ctx) }()

	waitLn(t, &srv.HTTPLn)
	waitLn(t, &srv.CtrlLn)

	localPort := bln.Addr().(*net.TCPAddr).Port
	ctrl := srv.CtrlLn.Addr().String()
	sess, info, err := client.Dial(client.Options{
		Server: ctrl,
		Proto:  proto.ProtoHTTP,
		Local:  fmt.Sprintf("127.0.0.1:%d", localPort),
		Plain:  true,
		Name:   "demo",
		Token:  "",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	go func() { _ = client.Serve(sess, fmt.Sprintf("127.0.0.1:%d", localPort)) }()

	if info.ID != "demo" {
		t.Fatalf("id %q", info.ID)
	}

	httpPort := srv.HTTPLn.Addr().(*net.TCPAddr).Port
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/hello", httpPort), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "demo.127.0.0.1.nip.io"
	hc := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	var resp *http.Response
	for i := 0; i < 20; i++ {
		resp, err = hc.Do(req)
		if err == nil && resp.StatusCode == 200 {
			break
		}
		if resp != nil {
			_ = resp.Body.Close()
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello-mrok" {
		t.Fatalf("body %q status %d", body, resp.StatusCode)
	}

	// 1MiB POST — old 256KiB yamux window returned 413 on /v1/chat/completions
	echo := http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		fmt.Fprintf(w, "got %d", n)
	})}
	eln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = echo.Close() })
	go echo.Serve(eln)
	echoPort := eln.Addr().(*net.TCPAddr).Port
	sessE, _, err := client.Dial(client.Options{
		Server: ctrl,
		Proto:  proto.ProtoHTTP,
		Local:  fmt.Sprintf("127.0.0.1:%d", echoPort),
		Plain:  true,
		Name:   "bigpost",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sessE.Close() })
	go func() { _ = client.Serve(sessE, fmt.Sprintf("127.0.0.1:%d", echoPort)) }()
	payload := bytes.Repeat([]byte("x"), 1<<20)
	reqE, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", httpPort), bytes.NewReader(payload))
	reqE.Host = "bigpost.127.0.0.1.nip.io"
	reqE.Header.Set("Content-Type", "application/json")
	var respE *http.Response
	for i := 0; i < 20; i++ {
		respE, err = hc.Do(reqE)
		if err == nil && respE.StatusCode == 200 {
			break
		}
		if respE != nil {
			_ = respE.Body.Close()
		}
		reqE, _ = http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", httpPort), bytes.NewReader(payload))
		reqE.Host = "bigpost.127.0.0.1.nip.io"
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(respE.Body)
	_ = respE.Body.Close()
	if string(got) != "got 1048576" {
		t.Fatalf("big post %q status %d", got, respE.StatusCode)
	}

	// delayed first byte — chat/Ollama style; must not hit a 1–5s proxy timeout
	slow := http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second)
		fl, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "slow-ok")
		if fl != nil {
			fl.Flush()
		}
	})}
	sln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = slow.Close() })
	go slow.Serve(sln)
	slowPort := sln.Addr().(*net.TCPAddr).Port
	sess2, _, err := client.Dial(client.Options{
		Server: ctrl,
		Proto:  proto.ProtoHTTP,
		Local:  fmt.Sprintf("127.0.0.1:%d", slowPort),
		Plain:  true,
		Name:   "slowok",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess2.Close() })
	go func() { _ = client.Serve(sess2, fmt.Sprintf("127.0.0.1:%d", slowPort)) }()
	req2, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", httpPort), nil)
	req2.Host = "slowok.127.0.0.1.nip.io"
	hc2 := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	var resp2 *http.Response
	for i := 0; i < 20; i++ {
		resp2, err = hc2.Do(req2)
		if err == nil && resp2.StatusCode == 200 {
			break
		}
		if resp2 != nil {
			_ = resp2.Body.Close()
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	b2, _ := io.ReadAll(resp2.Body)
	if string(b2) != "slow-ok" {
		t.Fatalf("slow body %q status %d", b2, resp2.StatusCode)
	}
}

func TestLongNameWithoutToken(t *testing.T) {
	srv := server.New(server.Config{
		HTTPAddr:    "127.0.0.1:0",
		ControlAddr: "127.0.0.1:0",
		Plain:       true,
		Open:        true,
		Token:       "secret",
		Domain:      "localhost",
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.ListenAndServe(ctx) }()
	waitLn(t, &srv.CtrlLn)

	name := strings.Repeat("a", 48)
	sess, info, err := client.Dial(client.Options{
		Server: srv.CtrlLn.Addr().String(),
		Proto:  proto.ProtoHTTP,
		Local:  "127.0.0.1:9",
		Plain:  true,
		Name:   name,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	if info.ID != name {
		t.Fatalf("id %q", info.ID)
	}
}

func TestReservedNameRequiresToken(t *testing.T) {
	srv := server.New(server.Config{
		HTTPAddr:    "127.0.0.1:0",
		ControlAddr: "127.0.0.1:0",
		Plain:       true,
		Open:        true,
		Token:       "secret",
		Domain:      "localhost",
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.ListenAndServe(ctx) }()
	waitLn(t, &srv.CtrlLn)

	_, _, err := client.Dial(client.Options{
		Server: srv.CtrlLn.Addr().String(),
		Proto:  proto.ProtoHTTP,
		Local:  "127.0.0.1:9",
		Plain:  true,
		Name:   "mine",
	})
	if err == nil {
		t.Fatal("expected token error")
	}
}

func TestTCPTunnel(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 64)
				n, _ := c.Read(buf)
				_, _ = c.Write([]byte("echo:" + string(buf[:n])))
			}(c)
		}
	}()

	localPort := ln.Addr().(*net.TCPAddr).Port
	srv := server.New(server.Config{
		HTTPAddr:    "127.0.0.1:0",
		ControlAddr: "127.0.0.1:0",
		Plain:       true,
		Open:        true,
		TCPMin:      21000,
		TCPMax:      21005,
		PublicIP:    "127.0.0.1",
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.ListenAndServe(ctx) }()
	waitLn(t, &srv.CtrlLn)

	sess, info, err := client.Dial(client.Options{
		Server: srv.CtrlLn.Addr().String(),
		Proto:  proto.ProtoTCP,
		Local:  fmt.Sprintf("127.0.0.1:%d", localPort),
		Plain:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	go func() { _ = client.Serve(sess, fmt.Sprintf("127.0.0.1:%d", localPort)) }()

	var port int
	if _, err := fmt.Sscanf(info.URL, "tcp://127.0.0.1:%d", &port); err != nil {
		t.Fatalf("url %s: %v", info.URL, err)
	}

	var c net.Conn
	for i := 0; i < 20; i++ {
		c, err = net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err := c.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf[:n]) != "echo:ping" {
		t.Fatalf("got %q", buf[:n])
	}
}

func waitLn(t *testing.T, ln *net.Listener) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if *ln != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("listener not ready")
}
