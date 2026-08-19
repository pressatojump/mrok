#!/bin/bash
# cloud-init for Amazon Linux 2023 minimal on t4g.nano
set -euxo pipefail

# 1G swap — nano has 512MiB RAM
if ! swapon --show | grep -q /swapfile; then
  dd if=/dev/zero of=/swapfile bs=1M count=1024 status=none
  chmod 600 /swapfile
  mkswap /swapfile
  swapon /swapfile
  echo '/swapfile none swap sw 0 0' >> /etc/fstab
fi

dnf -y install --allowerasing curl ca-certificates tar gzip || true

mkdir -p /etc/mrok /var/lib/mrok /usr/local/bin
chmod 700 /etc/mrok /var/lib/mrok

# binary is copied by deploy/aws.sh after first boot; this is the fallback
if [ ! -x /usr/local/bin/mrok ]; then
  curl -fsSL https://raw.githubusercontent.com/pressatojump/mrok/main/install.sh \
    | MROK_INSTALL_DIR=/usr/local/bin sh || true
fi

if [ -x /usr/local/bin/mrok ] && [ -f /etc/systemd/system/mrok.service ]; then
  systemctl daemon-reload
  systemctl enable --now mrok.service || true
fi
