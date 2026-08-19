#!/bin/sh
# mrok one-line installer — always uninstalls, then installs fresh
# curl -fsSL https://raw.githubusercontent.com/pressatojump/mrok/main/install.sh | sh
set -eu

REPO="${MROK_REPO:-pressatojump/mrok}"
BIN="${MROK_BIN:-mrok}"
VERSION="${MROK_VERSION:-latest}"

say() { printf 'mrok: %s\n' "$*"; }
err() { printf 'mrok: %s\n' "$*" >&2; exit 1; }

need() {
  command -v "$1" >/dev/null 2>&1 || err "need $1"
}

need uname
if command -v curl >/dev/null 2>&1; then
  DL='curl'
elif command -v wget >/dev/null 2>&1; then
  DL='wget'
else
  err "need curl or wget"
fi

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  armv7l) arch=arm ;;
  *) err "unsupported arch: $arch" ;;
esac
case "$os" in
  linux|darwin) ;;
  mingw*|msys*|cygwin*) os=windows ;;
  *) err "unsupported os: $os" ;;
esac

ext=""
if [ "$os" = windows ]; then
  ext=".exe"
fi

dest="${MROK_INSTALL_DIR:-}"
if [ -z "$dest" ]; then
  if [ -w /usr/local/bin ] || [ -w /usr/local ]; then
    dest=/usr/local/bin
  elif mkdir -p "$HOME/.local/bin" 2>/dev/null && [ -w "$HOME/.local/bin" ]; then
    dest="$HOME/.local/bin"
  else
    dest="$HOME/bin"
    mkdir -p "$dest"
  fi
fi

remove_bin() {
  f=$1
  [ -n "$f" ] && [ -e "$f" ] || return 0
  if rm -f "$f" 2>/dev/null; then
    say "removed $f"
    return 0
  fi
  if command -v sudo >/dev/null 2>&1 && sudo rm -f "$f"; then
    say "removed $f"
    return 0
  fi
  err "could not remove $f"
}

say "uninstalling"
# drop every previous client copy we know about
if command -v "$BIN" >/dev/null 2>&1; then
  existing=$(command -v "$BIN")
  case "$existing" in
    /*) remove_bin "$existing" ;;
  esac
fi
remove_bin "/usr/local/bin/$BIN$ext"
remove_bin "$HOME/.local/bin/$BIN$ext"
remove_bin "$HOME/bin/$BIN$ext"
remove_bin "$dest/$BIN$ext"

# login agents from a previous install
if [ "$(uname -s)" = Darwin ]; then
  launchctl bootout "gui/$(id -u)/com.mrok.tunnels" >/dev/null 2>&1 || true
  rm -f "$HOME/Library/LaunchAgents/com.mrok.tunnels.plist"
fi
rm -f "$HOME/.config/systemd/user/mrok.service" \
      "$HOME/.config/autostart/mrok.desktop" \
      "$HOME/AppData/Roaming/Microsoft/Windows/Start Menu/Programs/Startup/mrok.cmd" 2>/dev/null || true

cfg_dir="${HOME}/.mrok"
tunnels_bak=""
if [ -f "$cfg_dir/tunnels.json" ]; then
  tunnels_bak=$(mktemp)
  cp "$cfg_dir/tunnels.json" "$tunnels_bak"
fi
if [ -d "$cfg_dir" ]; then
  rm -rf "$cfg_dir"
  say "removed $cfg_dir"
fi
if [ -n "$tunnels_bak" ]; then
  mkdir -p "$cfg_dir"
  mv "$tunnels_bak" "$cfg_dir/tunnels.json"
  say "kept saved tunnels"
fi

hash -r 2>/dev/null || true

asset="${BIN}_${os}_${arch}${ext}"
if [ "$VERSION" = latest ]; then
  url="https://github.com/${REPO}/releases/latest/download/${asset}"
else
  url="https://github.com/${REPO}/releases/download/${VERSION}/${asset}"
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
out="$tmp/$BIN$ext"

say "downloading $url"
if [ "$DL" = curl ]; then
  curl -fL --retry 3 -o "$out" "$url" || err "download failed"
else
  wget -O "$out" "$url" || err "download failed"
fi
chmod +x "$out"

mkdir -p "$dest"
if ! install -m 0755 "$out" "$dest/$BIN$ext" 2>/dev/null; then
  if command -v sudo >/dev/null 2>&1; then
    sudo install -m 0755 "$out" "$dest/$BIN$ext" || err "install failed"
  else
    err "install failed (cannot write $dest)"
  fi
fi
say "installed $dest/$BIN$ext"

endpoint_url="https://raw.githubusercontent.com/${REPO}/main/endpoint"
endpoint=""
if [ "$DL" = curl ]; then
  endpoint=$(curl -fsSL "$endpoint_url" 2>/dev/null || true)
else
  endpoint=$(wget -qO- "$endpoint_url" 2>/dev/null || true)
fi
endpoint=$(printf '%s\n' "$endpoint" | sed -n '1p' | tr -d '\r')
case "$endpoint" in
  ""|\#*) endpoint="" ;;
  https://*|http://*) ;;
  *:*) endpoint="https://$endpoint" ;;
  *) endpoint="https://${endpoint}:443" ;;
esac

mkdir -p "$cfg_dir"
if [ -n "$endpoint" ]; then
  printf '{\n  "server": "%s"\n}\n' "$endpoint" > "$cfg_dir/config.json"
  chmod 600 "$cfg_dir/config.json"
  say "relay $endpoint"
fi

case ":$PATH:" in
  *":$dest:"*) ;;
  *)
    say "add $dest to PATH:"
    say "  export PATH=\"$dest:\$PATH\""
    ;;
esac

if [ -x "$dest/$BIN$ext" ]; then
  "$dest/$BIN$ext" autostart >/dev/null 2>&1 && say "login autostart enabled" || true
fi

say "run:  $BIN 3000"
