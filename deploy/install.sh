#!/usr/bin/env bash
# Meshport installer for Ubuntu 22.04 / 24.04.
# Run as root from the unpacked release directory (contains bin/ deploy/ docs/).
#
#   sudo ./deploy/install.sh --domain mesh.example.com --public-ip 203.0.113.10 --tls acme --with-turn
#
# Options
#   --domain NAME        public hostname of the console/API (required)
#   --public-ip ADDR     address frpc dials for the local frps node (default: --domain)
#   --tls MODE           acme (meshportd gets certificates itself, ports 80/443)
#                        proxy (meshportd on 127.0.0.1:8080 behind Caddy/nginx)   [default]
#   --with-turn          install and configure coturn for P2P NAT traversal
#   --subdomain-host H   wildcard host for http/https mappings (e.g. apps.example.com)
#   --frps-version V     frp release to download (default 0.72.0)
#   --frps-binary PATH   use an existing frps binary instead of downloading
set -euo pipefail

DOMAIN="" PUBLIC_IP="" TLS="proxy" WITH_TURN=0 SUBHOST="" FRP_VERSION="0.72.0" FRPS_BIN=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --domain) DOMAIN="$2"; shift 2 ;;
    --public-ip) PUBLIC_IP="$2"; shift 2 ;;
    --tls) TLS="$2"; shift 2 ;;
    --with-turn) WITH_TURN=1; shift ;;
    --subdomain-host) SUBHOST="$2"; shift 2 ;;
    --frps-version) FRP_VERSION="$2"; shift 2 ;;
    --frps-binary) FRPS_BIN="$2"; shift 2 ;;
    -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
    *) echo "unknown option $1" >&2; exit 2 ;;
  esac
done
[[ $EUID -eq 0 ]] || { echo "run as root" >&2; exit 1; }
[[ -n "$DOMAIN" ]] || { echo "--domain is required" >&2; exit 2; }
[[ -n "$PUBLIC_IP" ]] || PUBLIC_IP="$DOMAIN"
HERE="$(cd "$(dirname "$0")/.." && pwd)"
[[ -x "$HERE/bin/meshportd" ]] || { echo "bin/meshportd not found next to deploy/ (build with: make release)" >&2; exit 1; }

case "$(uname -m)" in
  x86_64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac

echo "==> user and directories"
id meshport &>/dev/null || useradd --system --home /var/lib/meshport --shell /usr/sbin/nologin meshport
install -d -o meshport -g meshport -m 0750 /var/lib/meshport
install -d -o meshport -g meshport -m 0750 /etc/meshport
install -d -o meshport -g meshport -m 0755 /opt/meshport/bin /opt/meshport/docs
install -o meshport -g meshport -m 0755 "$HERE/bin/meshportd" "$HERE/bin/meshctl" /opt/meshport/bin/
cp -r "$HERE/docs/." /opt/meshport/docs/ 2>/dev/null || true
ln -sf /opt/meshport/bin/meshportd /usr/local/bin/meshportd
ln -sf /opt/meshport/bin/meshctl /usr/local/bin/meshctl

echo "==> frps"
if [[ -n "$FRPS_BIN" ]]; then
  install -m 0755 "$FRPS_BIN" /opt/meshport/bin/frps
elif [[ ! -x /opt/meshport/bin/frps ]]; then
  tmp="$(mktemp -d)"
  base="https://github.com/fatedier/frp/releases/download/v${FRP_VERSION}"
  tarball="frp_${FRP_VERSION}_linux_${ARCH}.tar.gz"
  curl -fsSL -o "$tmp/$tarball" "$base/$tarball"
  if curl -fsSL -o "$tmp/sums.txt" "$base/frp_sha256_checksums.txt"; then
    (cd "$tmp" && grep " $tarball\$" sums.txt | sha256sum -c -) || { echo "frp checksum mismatch" >&2; exit 1; }
  else
    echo "warning: no checksum file published for frp v${FRP_VERSION}; verify $tarball manually" >&2
  fi
  tar -xzf "$tmp/$tarball" -C "$tmp"
  install -m 0755 "$tmp/frp_${FRP_VERSION}_linux_${ARCH}/frps" /opt/meshport/bin/frps
  rm -rf "$tmp"
fi
/opt/meshport/bin/frps --version

echo "==> meshportd init"
INIT_OUT=""
if [[ ! -f /etc/meshport/meshport.yaml ]]; then
  LISTEN="127.0.0.1:8080"; TLSMODE="off"
  if [[ "$TLS" == "acme" ]]; then LISTEN=":443"; TLSMODE="acme"; fi
  args=(-config /etc/meshport/meshport.yaml -data-dir /var/lib/meshport -public-url "https://$DOMAIN"
        -listen "$LISTEN" -tls "$TLSMODE" -frps-public-addr "$PUBLIC_IP" -frps-binary /opt/meshport/bin/frps)
  [[ -n "$SUBHOST" ]] && args+=(-frps-subdomain-host "$SUBHOST")
  [[ $WITH_TURN -eq 1 ]] && args+=(-turn-host "$DOMAIN")
  INIT_OUT="$(runuser -u meshport -- /opt/meshport/bin/meshportd init "${args[@]}")"
  echo "$INIT_OUT"
else
  echo "/etc/meshport/meshport.yaml exists; keeping it"
fi

if [[ $WITH_TURN -eq 1 ]]; then
  echo "==> coturn"
  DEBIAN_FRONTEND=noninteractive apt-get install -y coturn >/dev/null
  SECRET="$(awk '/^turn:/{t=1} t && /secret:/{print $2; exit}' /etc/meshport/meshport.yaml)"
  sed -e "s/__TURN_SECRET__/$SECRET/" -e "s/__REALM__/$DOMAIN/g" "$HERE/deploy/turnserver.conf" > /etc/turnserver.conf
  chmod 0640 /etc/turnserver.conf; chgrp turnserver /etc/turnserver.conf 2>/dev/null || true
  [[ -f /etc/default/coturn ]] && sed -i 's/^#\?TURNSERVER_ENABLED=.*/TURNSERVER_ENABLED=1/' /etc/default/coturn
  systemctl enable --now coturn
  systemctl restart coturn
fi

echo "==> systemd"
install -m 0644 "$HERE/deploy/meshportd.service" /etc/systemd/system/meshportd.service
systemctl daemon-reload
systemctl enable --now meshportd
sleep 2
systemctl --no-pager --lines=5 status meshportd || true

cat <<EOF

Meshport is installed.

  Console / API : https://$DOMAIN   (API reference: https://$DOMAIN/api/docs)
  Config        : /etc/meshport/meshport.yaml
  Data          : /var/lib/meshport   (backups in /var/lib/meshport/backups)
  Logs          : journalctl -u meshportd -f

Firewall ports to open (ufw allow ...):
  $( [[ "$TLS" == "acme" ]] && echo "80/tcp 443/tcp  console/API (ACME)" || echo "443/tcp          console/API via your reverse proxy (see deploy/Caddyfile)" )
  7000/tcp         frps (frpc control connections)
  8081/tcp 8444/tcp frps vhost HTTP/HTTPS (change in the console; 80/443 need a free port)
  20000:29999/tcp, 20000:29999/udp   remote ports for tcp/udp mappings
EOF
if [[ $WITH_TURN -eq 1 ]]; then
  echo "  3478/tcp 3478/udp 5349/tcp 49160:49200/udp   coturn (STUN/TURN)"
fi
if [[ "$TLS" != "acme" ]]; then
  echo
  echo "Reverse proxy: point https://$DOMAIN to 127.0.0.1:8080 (Caddy example: deploy/Caddyfile)."
fi
echo
echo "Admin password was printed above by 'meshportd init' (reset: meshportd admin -config /etc/meshport/meshport.yaml reset-password admin)."
