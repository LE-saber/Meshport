# Private deployment: SSH-only administration

This example covers Ubuntu 24.04 on amd64. Store actual hostnames, SSH account names, and credentials in local configuration—not in the public repository.

| Component | Address or path |
|---|---|
| Administration and backend listener | Server `127.0.0.1:18080` |
| Initial client testing endpoint | `http://<server-host>`, port 80; device routes only |
| Internal frps authorization plugin | Server `127.0.0.1:17401` |
| Dedicated Meshport frps | `<server-address>:7001` |
| Meshport frps dashboard | Server `127.0.0.1:7501` |
| Configuration | `/etc/meshport/meshport.yaml` |
| Database and runtime data | `/var/lib/meshport` |
| Executables | `/opt/meshport/bin` |
| Management page | Embedded in `meshportd` with `web_dir: ""` |

Neither administration nor internal authorization ports should be exposed publicly. The initial HTTP gateway in `deploy/Caddyfile.client` forwards only health, enrollment, device login, device WebSocket, device self-service, and transfer endpoints. All other routes return 404. Console static files, admin login/API routes, internal plugins, and API documentation remain private. Meshport's `7001` frps port is distinct from an existing frps on `7000`.

Clients must enter the complete `http://<server-host>` URL for this **explicitly insecure testing-only** endpoint. A bare hostname defaults to HTTPS; this HTTP setup has no TLS. The Go client performs registration/login, while Android WebView uses a loopback UI and needs no weakened network security configuration.

Set `public_url` to the client endpoint; keep `listen: 127.0.0.1:18080` and `tls.mode: off`. Administration is available only over the SSH tunnel below.

## Device testing gateway

Install the official Caddy binary at `/opt/meshport/bin/caddy`, copy `deploy/Caddyfile.client` to `/etc/meshport/Caddyfile.client`, install `deploy/meshport-gateway.service` under `/etc/systemd/system/`, and make `/var/lib/meshport-gateway` owned by the `meshport` user.

```bash
/opt/meshport/bin/caddy validate --config /etc/meshport/Caddyfile.client --adapter caddyfile
systemctl daemon-reload
systemctl enable --now meshport-gateway
```

This configuration is for deliberate plaintext testing. Do not replace its allowlist with a blanket `/api/v1/*` reverse proxy. For secure enrollment and TLS pinning, follow [Secure enrollment](SECURE_ENROLLMENT.md).

## One-click Windows administration

Set `MESHPORT_SSH_SERVER` on your local system to `user@server` and double-click `deploy/access/Open-Meshport.cmd`. Alternatively pass `-Server`. The script uses your existing key-based SSH configuration to create a background tunnel:

```text
Browser → local 127.0.0.1:18080 → SSH → server 127.0.0.1:18080
```

It opens `http://127.0.0.1:18080/` automatically. Repeated invocations reuse the tunnel. `Close-Meshport.cmd` stops the tunnel created by the helper; closing the browser does not.

When the local port is occupied:

```powershell
.\Open-Meshport.ps1 -Server user@your-server -LocalPort 18081
.\Open-Meshport.ps1 -Server user@your-server -LocalPort 18081 -Stop
```

Tunnel process IDs and logs live in `%LOCALAPPDATA%\Meshport\tunnels`, without admin passwords or private keys. SSH host-key changes must be verified before reconnecting.

For a foreground terminal tunnel:

```powershell
ssh -N -T -o ExitOnForwardFailure=yes -o ServerAliveInterval=30 -o ServerAliveCountMax=3 -L 127.0.0.1:18080:127.0.0.1:18080 user@your-server
```

## Console and credentials

The embedded console manages devices, FRP, permissions, security, and system settings. `/api/docs` exposes the API reference to authenticated local administrators. If an older deployment has `web_dir: /opt/meshport/web`, back up its configuration and clear `web_dir` to load the current embedded UI after restarting the binary.

The initial account is `admin`; the bootstrap password is stored only in a root-readable server file:

```powershell
ssh user@your-server sudo cat /root/meshport-bootstrap.txt
```

Sign in with `POST /api/v1/auth/login` using `username`, `password`, and optionally `totp`. The response sets an HttpOnly session cookie and may include a token. Never commit credentials or tokens.

## Operations

```bash
systemctl status meshportd
journalctl -u meshportd -n 100 --no-pager
systemctl restart meshportd
curl -fsS http://127.0.0.1:18080/healthz
ss -lntp '( sport = :18080 or sport = :17401 or sport = :7501 )'
```

`meshportd` starts automatically and supervises its own frps. Back up the database and configuration before upgrades; do not re-run initialization on an existing installation.
