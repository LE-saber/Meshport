# Deployment (Ubuntu 22.04 / 24.04)

## 1. Prerequisites

- A public Ubuntu server and a DNS record (for example, `mesh.example.com`) pointing to it.
- For HTTP/HTTPS services published under subdomains, a wildcard DNS entry such as `*.apps.example.com` pointing to the same server.
- A development machine with the required Go toolchain (Go 1.26+ for this workflow).

```bash
make test           # Unit and integration tests
make e2e            # Real frps/frpc integration tests (optional)
make release VERSION=1.0.0
# Produces release/meshport-server-1.0.0-linux-amd64.tar.gz,
# including bin/, deploy/, and docs/
```

## 2. Automated installation

```bash
scp release/meshport-server-1.0.0-linux-amd64.tar.gz root@server:
ssh root@server
tar xzf meshport-server-1.0.0-linux-amd64.tar.gz
cd meshport-server-1.0.0-linux-amd64
./deploy/install.sh --domain mesh.example.com --public-ip 203.0.113.10 \
    --tls acme --with-turn --subdomain-host apps.example.com
```

The script:

1. Creates the `meshport` system account and `/opt/meshport`, `/etc/meshport`, and `/var/lib/meshport`.
2. Downloads frps from GitHub and verifies published checksums when available.
3. Runs `meshportd init`, generating configuration, random secrets, a bootstrap administrator password (shown once), and managed local frps node `main`.
4. Optionally installs and configures coturn with a TURN REST shared secret when `--with-turn` is supplied.
5. Installs/starts the `meshportd` systemd unit, which manages frps itself.

**TLS options:** `--tls acme` lets Meshport obtain Let's Encrypt certificates (ports 80 and 443 required). The default `--tls proxy` mode listens on `127.0.0.1:8080` behind Caddy/nginx. See `deploy/Caddyfile`; WebSockets require no special additional proxy configuration in the provided Caddy example.

For SSH-only administration and a strictly allowlisted device gateway, see [Private deployment](PRIVATE_DEPLOY.md).

## 3. Firewall

| Port | Purpose |
|---|---|
| 443/tcp (plus 80/tcp for ACME) | Console, REST API, WebSocket |
| 7000/tcp | frps client connections |
| 8081/tcp and 8444/tcp | frps HTTP/HTTPS virtual hosts (changeable in console) |
| 20000–29999 tcp/udp | Remote service mapping ports |
| 3478 tcp/udp, 5349/tcp, 49160–49200/udp | coturn |

```bash
ufw allow 443/tcp && ufw allow 80/tcp && ufw allow 7000/tcp && ufw allow 8081,8444/tcp
ufw allow 20000:29999/tcp && ufw allow 20000:29999/udp
ufw allow 3478 && ufw allow 5349/tcp && ufw allow 49160:49200/udp
```

Only open the ports your deployment actually uses; never expose loopback-only administrative/plugin services.

## 4. First login

1. Open `https://mesh.example.com/api/docs` to check the API documentation endpoint.
2. Log in using the bootstrap admin credentials from `init`; change the password and enable MFA in **Security**.
3. Create an enrollment token under **Devices → Enroll device** or via `POST /api/v1/admin/enroll-tokens`.
4. Verify using `meshctl`:

```bash
meshctl register -server https://mesh.example.com -name test-box -enroll mpe_xxx
meshctl peers
meshctl listen
meshctl send <device-id> hello
meshctl api POST /api/v1/me/mappings '{"name":"ssh","node":"main","proto":"tcp","local_port":22,"remote_port":20022}'
meshctl frpc > frpc.toml && frpc -c frpc.toml
```

Run `meshctl listen` in a separate terminal. Creating an FRP mapping requires the `member` access level or higher.

## 5. Additional frps nodes

1. Register a remote node via `POST /admin/frp/nodes` with `managed:false`, and specify a `dashboard_addr` reachable by meshportd (for example, a private/WireGuard address such as `10.8.0.2:7500`).
2. The response includes `frps_toml`. Its authorization plugin URL resembles `https://mesh.example.com/internal/frps-plugin/<node>/<token>`.
3. Start frps on the remote host using this configuration. Use the official release's `frps.service` if deploying under systemd.

## 6. Maintenance

| Task | Procedure |
|---|---|
| Logs | `journalctl -u meshportd -f`, or **Monitoring & Logs** |
| Backups | Scheduled at 03:00 UTC by default, retaining 14. Also available via **Back up now** or `meshportd backup -config /etc/meshport/meshport.yaml`; saves a SQLite snapshot plus configuration copy |
| Restore | Stop `meshportd`, restore `/var/lib/meshport/meshport.db`, remove stale `meshport.db-wal` and `meshport.db-shm`, then restart |
| Reset password | `meshportd admin -config /etc/meshport/meshport.yaml reset-password admin` |
| Disable MFA | Use the corresponding `disable-mfa admin` admin command |
| Upgrade | Back up first; replace `/opt/meshport/bin/meshportd` and restart systemd. Database migrations run automatically |
| Online updates | Configure `update.manifest_url` and `update.public_key` (Ed25519) with a signed `{version,url,sha256,signature,notes}` manifest |
| Monitoring | Scrape Prometheus `/metrics` with a read-scoped API key |

## 7. Configuration outline: `/etc/meshport/meshport.yaml`

```yaml
listen: 127.0.0.1:8080           # :443 for ACME
internal_listen: 127.0.0.1:7401 # Local frps plugin; never expose publicly
public_url: https://mesh.example.com
data_dir: /var/lib/meshport
trusted_proxies: [127.0.0.1/32, ::1/128]
tls: {mode: off}                 # off | acme | file
turn:
  urls: [stun:mesh.example.com:3478, turn:mesh.example.com:3478?transport=udp, turn:mesh.example.com:3478?transport=tcp]
  secret: <same as coturn static-auth-secret>
  ttl: 12h
  health_addr: mesh.example.com:3478
frp:
  auth_token: <frps auth.token>
  binary: /opt/meshport/bin/frps
web_dir: ""                      # "" embedded console, directory custom UI, "off" disabled
```

Runtime-editable configuration, including enrollment approvals, discovery, relay, retention, backups, appearance, and **default language (English on new installations)**, is available in **System & API → Settings** or `PATCH /api/v1/admin/system/settings`. Existing saved language preferences are preserved.
