package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pressatojump/mrok/internal/autostart"
	"github.com/pressatojump/mrok/internal/client"
	"github.com/pressatojump/mrok/internal/config"
	"github.com/pressatojump/mrok/internal/idgen"
	"github.com/pressatojump/mrok/internal/proto"
	"github.com/pressatojump/mrok/internal/server"
	"github.com/pressatojump/mrok/internal/tlsutil"
)

var (
	version       = "dev"
	defaultServer = ""
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	switch cmd {
	case "server":
		os.Exit(runServer(os.Args[2:]))
	case "http", "tcp":
		os.Exit(runClient(cmd, os.Args[2:]))
	case "up":
		os.Exit(runUp())
	case "autostart":
		if err := autostart.Enable(); err != nil {
			fmt.Fprintf(os.Stderr, "mrok: autostart: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("mrok: login autostart enabled")
		os.Exit(0)
	case "version", "-v", "--version":
		fmt.Printf("mrok %s\n", version)
	case "help", "-h", "--help":
		usage()
	default:
		if _, err := strconv.Atoi(cmd); err == nil {
			os.Exit(runClient(proto.ProtoHTTP, os.Args[1:]))
		}
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `mrok %s — open source tunnel (ngrok/zrok-sized, nano-sized)

Install:
  curl -fsSL https://raw.githubusercontent.com/pressatojump/mrok/main/install.sh | sh

Usage:
  mrok <port>                 expose local HTTP port (saved, starts at login)
  mrok http <port>            same
  mrok tcp <port>             expose local TCP port
  mrok up                     start every saved tunnel (used at login)
  mrok autostart              install the login agent
  mrok server                 run the public relay
  mrok version

Client flags:
  --server https://host[:443] relay address (https is the default)
  --host ADDR                 local attach address (default 0.0.0.0)
  --token TOKEN               required for reserved names
  --name NAME                 request a stable subdomain
  --ephemeral                 do not save or autostart this tunnel
  --plain                     no TLS (local/dev)
  --insecure                  skip TOFU cert pin

Server flags:
  --http 0.0.0.0:80
  --https 0.0.0.0:443
  --control 0.0.0.0:443
  --domain HOST               e.g. example.com  →  https://<id>.example.com
  --acme                      Let's Encrypt for https://<id>.<base> (default on)
  --acme-email EMAIL          optional ACME contact
  --token TOKEN               admin token for reserved names
  --token-file PATH
  --data-dir DIR
  --plain                     control without TLS
  --closed                    require token for every tunnel
  --max-tunnels N
`, version)
}

func runClient(kind string, args []string) int {
	port, flagArgs := splitPortArgs(args)
	fs := flag.NewFlagSet("mrok", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	serverAddr := fs.String("server", "", "relay host[:port]")
	localHost := fs.String("host", "0.0.0.0", "local attach address")
	token := fs.String("token", "", "auth token")
	nameFlag := fs.String("name", "", "requested subdomain")
	plain := fs.Bool("plain", false, "disable TLS")
	insecure := fs.Bool("insecure", false, "skip TOFU pin")
	ephemeral := fs.Bool("ephemeral", false, "do not save or autostart")
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if extra := fs.Args(); len(extra) > 0 && port == "8080" {
		port = strings.TrimPrefix(extra[0], ":")
	}

	addr, err := client.ResolveServer(*serverAddr, defaultServer)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	tok := *token
	if tok == "" {
		tok = os.Getenv("MROK_TOKEN")
	}
	if tok == "" {
		tok = config.Load().Token
	}
	host := strings.TrimSpace(*localHost)
	if host == "" || host == "*" || host == "::" || host == "[::]" {
		host = "0.0.0.0"
	}
	local := net.JoinHostPort(host, port)
	name := *nameFlag
	if name == "" {
		if prev, ok := autostart.Find(local, kind); ok {
			name = prev.Name
		} else {
			name = idgen.Random(idgen.RandomLen)
		}
	}

	opt := client.Options{
		Server:   addr,
		Token:    tok,
		Proto:    kind,
		Name:     name,
		Local:    local,
		Plain:    *plain,
		Insecure: *insecure,
	}
	if !*ephemeral {
		_ = autostart.Upsert(autostart.Tunnel{
			Name:   name,
			Proto:  kind,
			Local:  opt.Local,
			Server: addr,
			Token:  tok,
		})
		if err := autostart.Enable(); err != nil {
			fmt.Fprintf(os.Stderr, "mrok: autostart: %v\n", err)
		} else {
			fmt.Fprintln(os.Stderr, "mrok: saved — will start at login")
		}
	}

	lk, err := autostart.TryLock(name)
	if err != nil {
		if prev, ok := autostart.Find(local, kind); ok && prev.URL != "" {
			fmt.Printf("\n  mrok %s\n  public   %s\n  local    %s\n  already running (login agent)\n\n", version, prev.URL, local)
			return 0
		}
		fmt.Fprintf(os.Stderr, "mrok: already running\n")
		return 0
	}
	defer lk.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return runLoop(ctx, opt, true, !*ephemeral)
}

func runUp() int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	var mu sync.Mutex
	running := map[string]bool{}
	start := func(t autostart.Tunnel) {
		mu.Lock()
		if running[t.Name] {
			mu.Unlock()
			return
		}
		running[t.Name] = true
		mu.Unlock()
		go func() {
			defer func() {
				mu.Lock()
				delete(running, t.Name)
				mu.Unlock()
			}()
			lk, err := autostart.TryLock(t.Name)
			if err != nil {
				return
			}
			defer lk.Close()
			opt := client.Options{
				Server: t.Server,
				Token:  t.Token,
				Proto:  t.Proto,
				Name:   t.Name,
				Local:  t.Local,
			}
			if cfg := config.Load().Server; cfg != "" {
				opt.Server = cfg
			} else if opt.Server == "" {
				if addr, err := client.ResolveServer("", defaultServer); err == nil {
					opt.Server = addr
				}
			}
			if opt.Proto == "" {
				opt.Proto = proto.ProtoHTTP
			}
			_ = runLoop(ctx, opt, true, true)
		}()
	}
	for _, t := range autostart.Load() {
		start(t)
	}
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return 0
		case <-tick.C:
			for _, t := range autostart.Load() {
				start(t)
			}
		}
	}
}

func runLoop(ctx context.Context, opt client.Options, banner, login bool) int {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return 0
		}
		sess, info, err := client.Dial(opt)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mrok: %v — retrying\n", err)
			select {
			case <-ctx.Done():
				return 0
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		if login {
			_ = autostart.Upsert(autostart.Tunnel{
				Name:   info.ID,
				Proto:  opt.Proto,
				Local:  opt.Local,
				Server: opt.Server,
				Token:  opt.Token,
				URL:    info.URL,
			})
		}
		if banner {
			extra := "ctrl+c stops this process"
			if login {
				extra = "starts at login · " + extra
			}
			fmt.Printf(`
  mrok %s
  public   %s
  local    %s
  id       %s

  %s

`, version, info.URL, opt.Local, info.ID, extra)
			_ = os.Stdout.Sync()
			banner = false
		} else {
			fmt.Fprintf(os.Stderr, "mrok: reconnected %s\n", info.URL)
		}
		errc := make(chan error, 1)
		go func() { errc <- client.Serve(sess, opt.Local) }()
		select {
		case <-ctx.Done():
			_ = sess.Close()
			return 0
		case err := <-errc:
			_ = sess.Close()
			if err != nil {
				fmt.Fprintf(os.Stderr, "mrok: disconnected: %v\n", err)
			}
		}
	}
}

func runServer(args []string) int {
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	httpAddr := fs.String("http", "0.0.0.0:80", "public HTTP listen address")
	httpsAddr := fs.String("https", "0.0.0.0:443", "public HTTPS / control listen address")
	ctrlAddr := fs.String("control", "", "control listen address (default: --https)")
	domain := fs.String("domain", "", "public base domain")
	token := fs.String("token", "", "admin token")
	tokenFile := fs.String("token-file", "", "read/write admin token here")
	dataDir := fs.String("data-dir", defaultDataDir(), "state directory")
	plain := fs.Bool("plain", false, "control without TLS")
	closed := fs.Bool("closed", false, "require token for every tunnel")
	maxTunnels := fs.Int("max-tunnels", 64, "max concurrent tunnels")
	publicIP := fs.String("public-ip", "", "advertised public IPv4")
	acme := fs.Bool("acme", true, "issue Let's Encrypt certs and advertise https:// URLs")
	acmeEmail := fs.String("acme-email", "", "optional ACME account email")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	tok := *token
	if tok == "" {
		tok = os.Getenv("MROK_TOKEN")
	}
	if *tokenFile != "" {
		if b, err := os.ReadFile(*tokenFile); err == nil {
			if t := strings.TrimSpace(string(b)); t != "" {
				tok = t
			}
		}
	}
	if tok == "" {
		tok = idgen.Token()
		if *tokenFile != "" {
			if err := os.MkdirAll(filepath.Dir(*tokenFile), 0o700); err == nil {
				_ = os.WriteFile(*tokenFile, []byte(tok+"\n"), 0o600)
			}
		}
		fmt.Fprintf(os.Stderr, "mrok: generated admin token (reserved names): %s\n", tok)
	} else if *tokenFile != "" {
		if _, err := os.Stat(*tokenFile); os.IsNotExist(err) {
			if err := os.MkdirAll(filepath.Dir(*tokenFile), 0o700); err == nil {
				_ = os.WriteFile(*tokenFile, []byte(tok+"\n"), 0o600)
			}
		}
	}

	ip := *publicIP
	if ip == "" {
		ip = server.DetectPublicIP()
	}

	cfg := server.Config{
		HTTPAddr:    *httpAddr,
		HTTPSAddr:   *httpsAddr,
		ControlAddr: *ctrlAddr,
		Plain:       *plain,
		Token:       tok,
		Domain:      *domain,
		PublicIP:    ip,
		Open:        !*closed,
		MaxTunnels:  *maxTunnels,
		ACME:        *acme && !*plain,
		ACMEDir:     filepath.Join(*dataDir, "certs"),
		ACMEEmail:   *acmeEmail,
	}
	if !*plain {
		hosts := []string{*domain, ip}
		tlsCfg, err := tlsutil.ServerTLS(*dataDir, hosts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mrok: tls: %v\n", err)
			return 1
		}
		cfg.TLSConfig = tlsCfg
	}

	s := server.New(cfg)
	fmt.Fprintf(os.Stderr, "mrok server %s  domain=%s ip=%s open=%v\n", version, s.PublicBase(), ip, cfg.Open)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := s.ListenAndServe(ctx); err != nil && err != context.Canceled {
		fmt.Fprintf(os.Stderr, "mrok: %v\n", err)
		return 1
	}
	return 0
}

func defaultDataDir() string {
	if d := os.Getenv("MROK_DATA"); d != "" {
		return d
	}
	return "/var/lib/mrok"
}

func splitPortArgs(args []string) (port string, flags []string) {
	port = "8080"
	found := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			if !strings.Contains(a, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				name := strings.TrimLeft(a, "-")
				if name != "plain" && name != "insecure" && name != "ephemeral" {
					i++
					flags = append(flags, args[i])
				}
			}
			continue
		}
		if !found {
			port = strings.TrimPrefix(a, ":")
			found = true
			continue
		}
		flags = append(flags, a)
	}
	return port, flags
}
