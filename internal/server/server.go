package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/pressatojump/mrok/internal/idgen"
	"github.com/pressatojump/mrok/internal/proto"
	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
	"golang.org/x/sync/singleflight"
)

type Config struct {
	HTTPAddr    string
	HTTPSAddr   string
	ControlAddr string
	Plain       bool
	TLSConfig   *tls.Config
	Token       string
	Domain      string
	PublicIP    string
	Open        bool
	MaxTunnels  int
	TCPMin      int
	TCPMax      int
	Idle        time.Duration
	Logger      *log.Logger
	ACME        bool
	ACMEDir     string
	ACMEEmail   string
}

type Server struct {
	cfg    Config
	log    *log.Logger
	mu     sync.RWMutex
	tuns   map[string]*Tunnel
	tcpMu  sync.Mutex
	tcp    map[int]string
	HTTPLn net.Listener
	CtrlLn net.Listener
	pubLn  *chanListener
	acme   *autocert.Manager
	certs  singleflight.Group
}

type Tunnel struct {
	ID      string
	Proto   string
	Session *yamux.Session
	TCPPort int
	TCPLn   net.Listener
	Created time.Time
}

func New(cfg Config) *Server {
	if cfg.MaxTunnels <= 0 {
		cfg.MaxTunnels = 64
	}
	if cfg.Idle <= 0 {
		cfg.Idle = 30 * time.Minute
	}
	if cfg.TCPMin <= 0 {
		cfg.TCPMin = 20000
	}
	if cfg.TCPMax <= 0 {
		cfg.TCPMax = 20031
	}
	lg := cfg.Logger
	if lg == nil {
		lg = log.New(os.Stderr, "mrok ", log.LstdFlags)
	}
	s := &Server{
		cfg:   cfg,
		log:   lg,
		tuns:  make(map[string]*Tunnel),
		tcp:   make(map[int]string),
		pubLn: newChanListener(),
	}
	if cfg.ACME && !cfg.Plain {
		dir := cfg.ACMEDir
		if dir == "" {
			dir = filepath.Join(os.TempDir(), "mrok-acme")
		}
		s.acme = &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			Cache:      autocert.DirCache(dir),
			HostPolicy: s.allowHost,
			Email:      cfg.ACMEEmail,
		}
	}
	return s
}

func (s *Server) PublicBase() string {
	if s.cfg.Domain != "" {
		return s.cfg.Domain
	}
	if s.cfg.PublicIP != "" {
		return strings.ReplaceAll(s.cfg.PublicIP, ".", "-") + ".sslip.io"
	}
	return "localhost"
}

func (s *Server) HTTPURL(id string) string {
	scheme := "http"
	if s.acme != nil {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s.%s", scheme, id, s.PublicBase())
}

func (s *Server) allowHost(_ context.Context, host string) error {
	host = strings.ToLower(stripPort(host))
	base := strings.ToLower(s.PublicBase())
	if host == base {
		return nil
	}
	if strings.HasSuffix(host, "."+base) {
		id := strings.TrimSuffix(host, "."+base)
		if !strings.Contains(id, ".") && idgen.Valid(id) {
			return nil
		}
	}
	return fmt.Errorf("disallowed host %q", host)
}

func (s *Server) tlsConfig() *tls.Config {
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{proto.ALPN, acme.ALPNProto, "http/1.1"},
	}
	var fallback *tls.Certificate
	if s.cfg.TLSConfig != nil && len(s.cfg.TLSConfig.Certificates) > 0 {
		fallback = &s.cfg.TLSConfig.Certificates[0]
	}
	cfg.GetCertificate = func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		name := strings.ToLower(hello.ServerName)
		if name == "" || net.ParseIP(name) != nil || name == "mrok" || name == "localhost" {
			if fallback != nil {
				return fallback, nil
			}
		}
		if s.acme != nil && name != "" && net.ParseIP(name) == nil {
			key := name
			for _, p := range hello.SupportedProtos {
				if p == acme.ALPNProto {
					key = name + "|acme"
					break
				}
			}
			v, err, _ := s.certs.Do(key, func() (any, error) {
				return s.acme.GetCertificate(hello)
			})
			if err != nil {
				return nil, err
			}
			return v.(*tls.Certificate), nil
		}
		if fallback != nil {
			return fallback, nil
		}
		return nil, fmt.Errorf("no certificate for %q", hello.ServerName)
	}
	return cfg
}

func (s *Server) precacheCert(host string) {
	if s.acme == nil {
		return
	}
	hello := &tls.ClientHelloInfo{ServerName: host}
	_, err, _ := s.certs.Do(host, func() (any, error) {
		return s.acme.GetCertificate(hello)
	})
	if err != nil {
		s.log.Printf("acme %s: %v", host, err)
	}
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	errc := make(chan error, 3)

	if s.cfg.HTTPAddr != "" {
		ln, err := net.Listen("tcp", s.cfg.HTTPAddr)
		if err != nil {
			return fmt.Errorf("http listen: %w", err)
		}
		s.HTTPLn = ln
		s.log.Printf("http %s acme=%v", ln.Addr(), s.acme != nil)
		var handler http.Handler = s
		if s.acme != nil {
			handler = s.acme.HTTPHandler(s)
		}
		hs := &http.Server{Handler: handler, ReadHeaderTimeout: 30 * time.Second, ReadTimeout: 0, WriteTimeout: 0, IdleTimeout: 0}
		go func() {
			errc <- hs.Serve(ln)
		}()
		go func() {
			<-ctx.Done()
			_ = hs.Close()
		}()
	}

	ctrlAddr := s.cfg.ControlAddr
	if ctrlAddr == "" {
		ctrlAddr = s.cfg.HTTPSAddr
	}
	if ctrlAddr != "" {
		ln, err := net.Listen("tcp", ctrlAddr)
		if err != nil {
			return fmt.Errorf("control listen: %w", err)
		}
		s.CtrlLn = ln
		s.log.Printf("control %s tls=%v acme=%v", ln.Addr(), !s.cfg.Plain && s.cfg.TLSConfig != nil, s.acme != nil)
		if !s.cfg.Plain {
			hs := &http.Server{
				Handler:           s,
				ReadHeaderTimeout: 30 * time.Second,
				ReadTimeout:       0,
				WriteTimeout:      0,
				IdleTimeout:       0,
				TLSNextProto:      map[string]func(*http.Server, *tls.Conn, http.Handler){},
			}
			s.pubLn.addr = ln.Addr()
			go func() {
				errc <- hs.Serve(s.pubLn)
			}()
			go func() {
				<-ctx.Done()
				_ = hs.Close()
				_ = s.pubLn.Close()
			}()
		}
		go func() {
			errc <- s.serveControl(ctx, ln)
		}()
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errc:
		return err
	}
}

func (s *Server) serveControl(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go s.handleConn(c)
	}
}

func (s *Server) handleConn(c net.Conn) {
	_ = c.SetDeadline(time.Now().Add(90 * time.Second))
	var conn net.Conn = c
	if !s.cfg.Plain && (s.cfg.TLSConfig != nil || s.acme != nil) {
		tc := tls.Server(c, s.tlsConfig())
		if err := tc.Handshake(); err != nil {
			_ = c.Close()
			return
		}
		_ = c.SetDeadline(time.Time{})
		_ = tc.SetDeadline(time.Time{})
		np := tc.ConnectionState().NegotiatedProtocol
		if np == acme.ALPNProto {
			_ = tc.Close()
			return
		}
		if np != proto.ALPN {
			if err := s.pubLn.Enqueue(tc); err != nil {
				_ = tc.Close()
			}
			return
		}
		conn = tc
	}
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))

	var hello proto.Hello
	if err := proto.ReadJSON(conn, &hello); err != nil {
		_ = conn.Close()
		return
	}
	_ = conn.SetDeadline(time.Time{})

	id, url, err := s.register(hello)
	if err != nil {
		_ = proto.WriteJSON(conn, proto.Welcome{Error: err.Error()})
		_ = conn.Close()
		return
	}
	if err := proto.WriteJSON(conn, proto.Welcome{ID: id, URL: url}); err != nil {
		s.drop(id)
		_ = conn.Close()
		return
	}

	sess, err := yamux.Server(conn, proto.YamuxConfig())
	if err != nil {
		s.drop(id)
		_ = conn.Close()
		return
	}
	s.mu.Lock()
	if t, ok := s.tuns[id]; ok {
		t.Session = sess
	}
	s.mu.Unlock()
	s.log.Printf("tunnel up %s %s", id, url)
	<-sess.CloseChan()
	s.drop(id)
	s.log.Printf("tunnel down %s", id)
}

type chanListener struct {
	ch   chan net.Conn
	addr net.Addr
	mu   sync.Mutex
	done chan struct{}
}

func newChanListener() *chanListener {
	return &chanListener{ch: make(chan net.Conn, 64), done: make(chan struct{})}
}

func (l *chanListener) Enqueue(c net.Conn) error {
	select {
	case <-l.done:
		return net.ErrClosed
	case l.ch <- c:
		return nil
	}
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case <-l.done:
		return nil, net.ErrClosed
	case c, ok := <-l.ch:
		if !ok {
			return nil, net.ErrClosed
		}
		return c, nil
	}
}

func (l *chanListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case <-l.done:
	default:
		close(l.done)
	}
	return nil
}

func (l *chanListener) Addr() net.Addr {
	if l.addr != nil {
		return l.addr
	}
	return &net.TCPAddr{IP: net.IPv4zero, Port: 443}
}

func (s *Server) register(h proto.Hello) (string, string, error) {
	if h.V != proto.Version {
		return "", "", fmt.Errorf("unsupported version %d", h.V)
	}
	if h.Proto == "" {
		h.Proto = proto.ProtoHTTP
	}
	if h.Proto != proto.ProtoHTTP && h.Proto != proto.ProtoTCP {
		return "", "", fmt.Errorf("unsupported proto %q", h.Proto)
	}
	if !s.cfg.Open && s.cfg.Token != "" && h.Token != s.cfg.Token {
		return "", "", fmt.Errorf("unauthorized")
	}

	s.mu.Lock()
	if len(s.tuns) >= s.cfg.MaxTunnels {
		s.mu.Unlock()
		return "", "", fmt.Errorf("relay is full")
	}

	var (
		id  string
		err error
		old *Tunnel
	)
	if h.Name != "" {
		id, err = idgen.Normalize(h.Name)
		if err != nil {
			s.mu.Unlock()
			return "", "", err
		}
		if idgen.Vanity(id) && s.cfg.Token != "" && h.Token != s.cfg.Token {
			s.mu.Unlock()
			return "", "", fmt.Errorf("reserved names require a valid token")
		}
		if existing, ok := s.tuns[id]; ok {
			old = existing
			delete(s.tuns, id)
		}
	} else {
		for i := 0; i < 8; i++ {
			cand := idgen.Random(idgen.RandomLen)
			if _, ok := s.tuns[cand]; !ok {
				id = cand
				break
			}
		}
		if id == "" {
			s.mu.Unlock()
			return "", "", fmt.Errorf("could not allocate name")
		}
	}

	t := &Tunnel{ID: id, Proto: h.Proto, Created: time.Now()}
	var url string
	if h.Proto == proto.ProtoTCP {
		port, err := s.allocTCP(id)
		if err != nil {
			s.mu.Unlock()
			return "", "", err
		}
		t.TCPPort = port
		host := s.cfg.PublicIP
		if host == "" {
			host = s.PublicBase()
		}
		url = fmt.Sprintf("tcp://%s:%d", host, port)
	} else {
		url = s.HTTPURL(id)
	}
	s.tuns[id] = t
	s.mu.Unlock()

	if old != nil {
		if old.Session != nil {
			_ = old.Session.Close()
		}
		if old.TCPLn != nil {
			_ = old.TCPLn.Close()
		}
	}
	if h.Proto == proto.ProtoTCP {
		go s.listenTCP(t)
	} else {
		go s.precacheCert(id + "." + s.PublicBase())
	}
	return id, url, nil
}

func (s *Server) drop(id string) {
	s.mu.Lock()
	t, ok := s.tuns[id]
	if ok {
		delete(s.tuns, id)
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	if t.Session != nil {
		_ = t.Session.Close()
	}
	if t.TCPLn != nil {
		_ = t.TCPLn.Close()
	}
	if t.TCPPort > 0 {
		s.tcpMu.Lock()
		delete(s.tcp, t.TCPPort)
		s.tcpMu.Unlock()
	}
}

func (s *Server) lookup(id string) *Tunnel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tuns[id]
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" && s.isApex(r.Host) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "ok\n")
		return
	}
	if r.TLS == nil && s.acme != nil {
		host := r.Host
		if host == "" {
			host = r.URL.Host
		}
		http.Redirect(w, r, "https://"+host+r.URL.RequestURI(), http.StatusMovedPermanently)
		return
	}
	id, rest := s.route(r)
	if id == "" {
		s.landing(w, r)
		return
	}
	t := s.lookup(id)
	if t == nil || t.Session == nil || t.Proto != proto.ProtoHTTP {
		http.Error(w, "tunnel not found", http.StatusNotFound)
		return
	}
	if rest != "" {
		r.URL.Path = rest
		if r.URL.RawPath != "" {
			r.URL.RawPath = rest
		}
	}
	rewriteOpenAIPath(r)
	if serveOpenAIRoot(w, r) {
		return
	}
	s.proxy(w, r, t)
}

// Clients often set base URL to .../v1 while the SDK also prepends /v1.
func rewriteOpenAIPath(r *http.Request) {
	p := r.URL.Path
	for strings.HasPrefix(p, "/v1/v1") {
		p = "/v1" + strings.TrimPrefix(p, "/v1/v1")
	}
	if p != r.URL.Path {
		r.URL.Path = p
		if r.URL.RawPath != "" {
			r.URL.RawPath = p
		}
	}
}

// GET /v1 is not implemented by Ollama/vLLM and 404s; UIs then retry until they time out.
func serveOpenAIRoot(w http.ResponseWriter, r *http.Request) bool {
	p := strings.TrimSuffix(r.URL.Path, "/")
	if p != "/v1" {
		return false
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = io.WriteString(w, `{"object":"api","version":"v1"}`+"\n")
		}
		return true
	case http.MethodOptions:
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS, HEAD")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		w.WriteHeader(http.StatusNoContent)
		return true
	default:
		return false
	}
}

func (s *Server) isApex(host string) bool {
	h := stripPort(host)
	if h == s.cfg.PublicIP || h == s.PublicBase() || h == "localhost" || h == "127.0.0.1" {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return true
	}
	return false
}

func (s *Server) route(r *http.Request) (id, rest string) {
	host := stripPort(r.Host)
	base := s.PublicBase()
	if strings.HasSuffix(host, "."+base) {
		id = strings.TrimSuffix(host, "."+base)
		if idgen.Valid(id) {
			return id, ""
		}
	}
	if s.isApex(host) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			return "", ""
		}
		parts := strings.SplitN(path, "/", 2)
		if idgen.Valid(parts[0]) && s.lookup(parts[0]) != nil {
			if len(parts) == 2 {
				return parts[0], "/" + parts[1]
			}
			return parts[0], "/"
		}
	}
	// first label fallback (custom / unknown domain)
	if i := strings.IndexByte(host, '.'); i > 0 {
		cand := host[:i]
		if idgen.Valid(cand) && s.lookup(cand) != nil {
			return cand, ""
		}
	}
	return "", ""
}

func (s *Server) landing(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, `<!doctype html>
<meta charset="utf-8">
<title>mrok</title>
<pre>
mrok — open tunnel relay

  curl -fsSL https://raw.githubusercontent.com/pressatojump/mrok/main/install.sh | sh
  mrok 3000

tunnels %d / %d
</pre>
`, s.tunnelCount(), s.cfg.MaxTunnels)
}

func (s *Server) tunnelCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.tuns)
}

func (s *Server) proxy(w http.ResponseWriter, r *http.Request, t *Tunnel) {
	s.splice(w, r, t)
}

func (s *Server) splice(w http.ResponseWriter, r *http.Request, t *Tunnel) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	stream, err := t.Session.Open()
	if err != nil {
		http.Error(w, "tunnel busy", http.StatusBadGateway)
		return
	}
	defer stream.Close()

	conn, rw, err := hj.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Time{})
	keepAlive(conn)

	if ip, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		r.Header.Set("X-Forwarded-For", ip)
	}
	if r.TLS != nil {
		r.Header.Set("X-Forwarded-Proto", "https")
	} else {
		r.Header.Set("X-Forwarded-Proto", "http")
	}
	r.Header.Set("X-Forwarded-Host", r.Host)

	if err := r.Write(stream); err != nil {
		return
	}
	if n := rw.Reader.Buffered(); n > 0 {
		buf := make([]byte, n)
		_, _ = rw.Reader.Read(buf)
		_, _ = stream.Write(buf)
	}

	errc := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(stream, conn)
		closeWrite(stream)
		errc <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(conn, stream)
		closeWrite(conn)
		errc <- struct{}{}
	}()
	<-errc
	<-errc
}

func keepAlive(c net.Conn) {
	switch t := c.(type) {
	case *net.TCPConn:
		_ = t.SetKeepAlive(true)
		_ = t.SetKeepAlivePeriod(15 * time.Second)
	case *tls.Conn:
		if nc := t.NetConn(); nc != nil {
			keepAlive(nc)
		}
	}
}

func closeWrite(c net.Conn) {
	type closer interface{ CloseWrite() error }
	if cw, ok := c.(closer); ok {
		_ = cw.CloseWrite()
		return
	}
	if t, ok := c.(*tls.Conn); ok {
		if nc := t.NetConn(); nc != nil {
			closeWrite(nc)
		}
	}
}

func (s *Server) allocTCP(id string) (int, error) {
	s.tcpMu.Lock()
	defer s.tcpMu.Unlock()
	for p := s.cfg.TCPMin; p <= s.cfg.TCPMax; p++ {
		if _, used := s.tcp[p]; !used {
			s.tcp[p] = id
			return p, nil
		}
	}
	return 0, fmt.Errorf("no free tcp ports")
}

func (s *Server) listenTCP(t *Tunnel) {
	ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", t.TCPPort))
	if err != nil {
		s.log.Printf("tcp listen %d: %v", t.TCPPort, err)
		return
	}
	t.TCPLn = ln
	defer ln.Close()
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		s.mu.RLock()
		cur := s.tuns[t.ID]
		s.mu.RUnlock()
		if cur == nil || cur.Session == nil {
			_ = c.Close()
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			stream, err := cur.Session.Open()
			if err != nil {
				return
			}
			defer stream.Close()
			splice(c, stream)
		}(c)
	}
}

func splice(a, b io.ReadWriteCloser) {
	errc := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(a, b); errc <- struct{}{} }()
	go func() { _, _ = io.Copy(b, a); errc <- struct{}{} }()
	<-errc
}

func stripPort(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

func DetectPublicIP() string {
	if ip := os.Getenv("MROK_PUBLIC_IP"); ip != "" {
		return ip
	}
	client := &http.Client{Timeout: 2 * time.Second}
	req, _ := http.NewRequest(http.MethodGet, "http://169.254.169.254/latest/meta-data/public-ipv4", nil)
	if resp, err := client.Do(req); err == nil {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
		if ip := strings.TrimSpace(string(b)); net.ParseIP(ip) != nil {
			return ip
		}
	}
	req, _ = http.NewRequest(http.MethodGet, "https://checkip.amazonaws.com", nil)
	if resp, err := client.Do(req); err == nil {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
		if ip := strings.TrimSpace(string(b)); net.ParseIP(ip) != nil {
			return ip
		}
	}
	return ""
}
