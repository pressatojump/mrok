# mrok

Open-source [ngrok](https://ngrok.com)/[zrok](https://zrok.io) in one static Go binary.
The public relay is built to run on the cheapest AWS box that exists: **t4g.nano** (Graviton2, 2 vCPU, 0.5 GiB, ~$3/month in `us-east-1`).

```sh
curl -fsSL https://raw.githubusercontent.com/pressatojump/mrok/main/install.sh | sh
mrok 3000
```

Every run of that one-liner uninstalls any previous `mrok` binary and `~/.mrok` config (saved tunnels are kept), then installs the latest release, rewrites the relay address, and enables login autostart. Traffic hitting the public URL is multiplexed down a single outbound TLS connection to your laptop — no inbound ports, no port-forward, no account.

## Client

```sh
mrok 3000                     # expose local HTTP :3000 — saved, starts at login
mrok http 8080 --name demo    # short vanity name (needs token)
mrok tcp 22                   # raw TCP (ssh, postgres, …)
mrok up                       # start every saved tunnel
```

Each tunnel gets a **48-character** subdomain and is registered to start at login (launchd on macOS, systemd user on Linux). Use `--ephemeral` to skip saving.

Flags: `--host 0.0.0.0` (default attach), `--server https://host:443`, `--token`, `--name`, `--ephemeral`, `--plain` (dev HTTP only), `--insecure`.

Config lives in `~/.mrok/config.json`. The installer writes the public relay address from [`endpoint`](./endpoint). Override with `MROK_SERVER` or `--server`.

Reserved names (`--name`) on the public relay need the admin token:

```sh
mrok http 3000 --name myapp --token "$MROK_TOKEN"
```

## Self-host the relay

Same binary.

```sh
# any Linux box with a public IP
sudo ./mrok server --http 0.0.0.0:80 --https 0.0.0.0:443 --token-file /etc/mrok/token
```

Clients then:

```sh
mrok 3000 --server https://your.ip.or.domain
```

Public URLs are `https://<id>.<dotted-ip-as-dashes>.sslip.io` (Let's Encrypt on demand; `:80` redirects to `:443`). Pass `--domain example.com` and point `*.example.com` at the box for `https://<id>.example.com`.

### Smallest AWS server

```sh
# needs aws cli credentials; creates t4g.nano + Elastic IP + security group
./deploy/aws.sh
```

What it launches:

| Resource | Choice | Why |
|---|---|---|
| Instance | `t4g.nano` | cheapest EC2 (ARM, 512 MiB) |
| AMI | AL2023 **minimal** arm64 | ~150 MiB OS, leaves RAM for the relay |
| Disk | 8 GB gp3 | AL2023 floor |
| Swap | 1 GB | nano OOM insurance |
| IP | Elastic IP | stable `endpoint` for the installer |
| Process | one `mrok` binary under systemd, `MemoryMax=180M` | no Docker, no nginx |

Rough on-demand cost in `us-east-1`: **~$3.07 instance + ~$0.64 disk ≈ $3.70/month**. Elastic IP is free while attached.

## Protocol

1. Client dials `:443` with TLS ALPN `mrok` (TOFU-pinned).
2. JSON hello/welcome assigns a tunnel id and public URL.
3. [yamux](https://github.com/hashicorp/yamux) multiplexes streams on that one connection.
4. Public `:443` (HTTPS, real Let's Encrypt cert per hostname) looks at the Host label, opens a stream, splices bytes. WebSockets work. `:80` answers ACME HTTP-01 and redirects everything else to HTTPS.

Browsers hitting `:443` without the `mrok` ALPN get the tunneled site. One port, one process.

## Build

```sh
go test ./...
make build          # bin/mrok
make dist           # linux/darwin/windows × amd64/arm64
```

`go install github.com/pressatojump/mrok@latest` also works.

## License

MIT. See [LICENSE](./LICENSE).
