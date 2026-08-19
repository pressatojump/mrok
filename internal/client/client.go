package client

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/pressatojump/mrok/internal/config"
	"github.com/pressatojump/mrok/internal/proto"
	"github.com/pressatojump/mrok/internal/tlsutil"
)

type Options struct {
	Server   string
	Token    string
	Proto    string
	Name     string
	Local    string
	Plain    bool
	Insecure bool
}

type Session struct {
	ID  string
	URL string
}

func ResolveServer(explicit, compiledDefault string) (string, error) {
	for _, s := range []string{
		explicit,
		os.Getenv("MROK_SERVER"),
		config.Load().Server,
		compiledDefault,
		fetchEndpoint(),
	} {
		if config.ParseServer(s).Dial != "" {
			return strings.TrimSpace(s), nil
		}
	}
	return "", fmt.Errorf("no server configured: pass --server, set MROK_SERVER, or install from https://github.com/pressatojump/mrok")
}

func fetchEndpoint() string {
	c := &http.Client{Timeout: 8 * time.Second}
	resp, err := c.Get(config.EndpointURL)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return ""
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(b))
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	if strings.HasPrefix(line, "#") {
		return ""
	}
	return line
}

// ConnectAddr is the TCP address used to reach a local service.
// 0.0.0.0 / :: mean "all interfaces"; we dial loopback to hit that listener.
func ConnectAddr(local string) string {
	if !strings.Contains(local, ":") {
		local = net.JoinHostPort("0.0.0.0", local)
	}
	host, port, err := net.SplitHostPort(local)
	if err != nil {
		return local
	}
	switch host {
	case "", "0.0.0.0", "*":
		host = "127.0.0.1"
	case "[::]", "::":
		host = "::1"
	}
	return net.JoinHostPort(host, port)
}

func Dial(opt Options) (*yamux.Session, Session, error) {
	if opt.Proto == "" {
		opt.Proto = proto.ProtoHTTP
	}
	if !strings.Contains(opt.Local, ":") {
		opt.Local = net.JoinHostPort("0.0.0.0", opt.Local)
	}
	parsed := config.ParseServer(opt.Server)
	if parsed.Dial == "" {
		return nil, Session{}, fmt.Errorf("empty server address")
	}
	addr := parsed.Dial
	plain := opt.Plain || !parsed.HTTPS

	raw, err := net.DialTimeout("tcp", addr, 12*time.Second)
	if err != nil {
		return nil, Session{}, fmt.Errorf("dial %s: %w", addr, err)
	}

	var conn net.Conn = raw
	if !plain {
		host, _, _ := net.SplitHostPort(addr)
		cfg := &tls.Config{
			ServerName:         host,
			MinVersion:         tls.VersionTLS12,
			NextProtos:         []string{proto.ALPN},
			InsecureSkipVerify: true, // TOFU below
		}
		tc := tls.Client(raw, cfg)
		if err := tc.Handshake(); err != nil {
			_ = raw.Close()
			return nil, Session{}, fmt.Errorf("tls: %w", err)
		}
		if !opt.Insecure {
			fp := tlsutil.Fingerprint(tc.ConnectionState())
			if err := tlsutil.CheckTOFU(config.KnownHostsPath(), host, fp); err != nil {
				_ = tc.Close()
				return nil, Session{}, err
			}
		}
		conn = tc
	}

	hello := proto.Hello{
		V:     proto.Version,
		Token: opt.Token,
		Proto: opt.Proto,
		Name:  opt.Name,
		Local: opt.Local,
	}
	if err := proto.WriteJSON(conn, hello); err != nil {
		_ = conn.Close()
		return nil, Session{}, err
	}
	var welcome proto.Welcome
	if err := proto.ReadJSON(conn, &welcome); err != nil {
		_ = conn.Close()
		return nil, Session{}, err
	}
	if welcome.Error != "" {
		_ = conn.Close()
		return nil, Session{}, fmt.Errorf("%s", welcome.Error)
	}

	sess, err := yamux.Client(conn, proto.YamuxConfig())
	if err != nil {
		_ = conn.Close()
		return nil, Session{}, err
	}
	return sess, Session{ID: welcome.ID, URL: welcome.URL}, nil
}

func Serve(sess *yamux.Session, local string) error {
	if !strings.Contains(local, ":") {
		local = net.JoinHostPort("0.0.0.0", local)
	}
	dial := ConnectAddr(local)
	for {
		stream, err := sess.Accept()
		if err != nil {
			return err
		}
		go handleStream(stream, dial)
	}
}

func handleStream(stream net.Conn, local string) {
	defer stream.Close()
	dst, err := net.DialTimeout("tcp", ConnectAddr(local), 30*time.Second)
	if err != nil {
		return
	}
	defer dst.Close()
	if tc, ok := dst.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(15 * time.Second)
	}
	errc := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(dst, stream)
		if tc, ok := dst.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
		errc <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(stream, dst)
		errc <- struct{}{}
	}()
	<-errc
	<-errc
}
