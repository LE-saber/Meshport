# Meshport server architecture

## 1. Scope and status

Meshport combines a centralized identity/permission server, encrypted communications, peer-to-peer signaling, TURN relay credentials, and centrally authorized FRP publishing. The embedded administration console lives at `internal/console/web`; desktop and Android clients share a Go core at `internal/agent`. The server also provides `pkg/client`, `pkg/proto`, and the `meshctl` test CLI.

The UI and API were designed around desktop, mobile, and eight server-console views. Backend operations are implemented around concrete server data and endpoint contracts. See [Client](CLIENT.md) for device-side behavior.

## 2. Technology choices

| Concern | Approach | Rationale |
|---|---|---|
| Server | Single Go binary, `meshportd` | Simple systemd deployment alongside frp |
| Data storage | SQLite via modernc (pure Go), WAL | Local persistence and `VACUUM INTO` backups without separate databases |
| Realtime events | coder/websocket | Presence, offline queues, receipts, signaling, commands |
| P2P | WebRTC DataChannel (pion/libwebrtc clients) | ICE/NAT negotiation without a custom packet protocol |
| TURN | coturn and temporary TURN REST credentials | 12-hour shared-secret credentials |
| Encrypted relay | Built-in store-and-forward | Offline delivery, receipts, and resumable files without server plaintext access |
| Service publishing | fatedier/frp v0.72, official frps plugin | Server-authorized logins/proxies/heartbeats and managed frps processes |

The platform does not create virtual network adapters. WebRTC DataChannels and frpc application-layer proxies do not need TUN/TAP or privileged networking.

Projects considered for inspiration include Tinode/OpenIM (offline messaging and receipts), Iroh (alternative QUIC transport), frp-panel (FRP administration), and MeshCentral (device administration). Meshport retains an opaque signaling payload for possible future transports but does not embed those entire systems.

## 3. Components

```text
                       Ubuntu server
  Browser (HTTPS) ───► meshportd ─── REST /api/v1
  Devices (WSS) ─────►    │
                          ├── Hub: presence, relay, signaling, telemetry, commands
                          ├── Policy: device level, console role, visibility
                          ├── FRP manager → managed frps ← remote frpc
                          │     └─ plugin authorization (login/proxy/heartbeat)
                          ├── Monitoring, events, alerts, backups, webhooks
                          └── SQLite (WAL)
  WebRTC clients ◄──────────── coturn / STUN / TURN
```

## 4. Transport modes

| User choice | Client behavior | Server behavior |
|---|---|---|
| P2P only (`p2p`) | Direct WebRTC, excluding TURN; queue undeliverable messages locally | Signaling only; reject `msg.send` with `mode=p2p` |
| Prefer P2P (`auto`) | Attempt WebRTC including TURN, then server relay on failure | Signaling and encrypted store-and-forward fallback |
| Relay only (`relay`) | Send via `msg.send` and resumable `/files`; optional TURN-only realtime streams | Queue/deliver ciphertext and receipts |

Clients send `session.report` on changes and at intervals of at most five seconds while active, including actual path, `via`, latency, bitrate, receipt counts, and transfer status. The console uses these reports for connection analysis and P2P success rates.

## 5. Permission model

**Device levels** determine capabilities:

- `guest`: messages and file transfers.
- `member`: guest functions plus publishing personal FRP mappings.
- `admin`: additional FRP administration and ability to request access grants for other devices.

**Console roles** are independent: `viewer` is read-only, `operator` performs everyday management, and `admin` manages everything. A `user` without console privileges may obtain appropriate enrollment tokens for personal guest devices. API token scopes `read/write/admin` are bounded by the owner's privileges.

**Discovery scope** is `all`, `group`, or `user`. Admin-level devices are visible to all devices and can see all devices.

Admin-level devices may request a grant for another device without exceeding their own level. An operator must approve it. Durations are `1h / 6h / 24h / 7d / 30d / permanent`. Expired or revoked grants restore the preceding level; approval of a new grant supersedes the prior grant. Changes are pushed immediately and FRP permissions re-evaluated.

The frps plugin validates every login, proxy creation, and heartbeat against device state, level, mapping status, allowed port/domain, and credentials. Revoked mappings cause disconnection/re-registration at the next heartbeat. The real-binary `TestFRPEndToEnd` covers this enforcement.

## 6. Security

- Device identity: Ed25519 public/private key pair, challenge-response with single-use nonce and two-minute validity, and dual-signature key rotation.
- End-to-end encryption: X25519 `box_pubkey` is shared through `/peers`; the server stores and forwards ciphertext.
- Console authentication: bcrypt, TOTP MFA (optionally mandatory), rate limits and login anomaly alerts. The default block threshold is five failed attempts in ten minutes, with a 15-minute temporary block.
- Credentials: tokens stored as SHA-256 hashes; session cookies use `SameSite=Strict`, plus `X-Requested-With` on writes.
- IP controls: deny rules apply to all traffic including published service connections; allow rules limit console/API token sources without blocking mobile devices.
- Audit: changes are recorded in `audit_log` and may be sent via HMAC-SHA256-signed webhooks.
- Updates: Ed25519-signed manifests and SHA-256 verification, with automatic backup and systemd restart.

## 7. Console views and API mapping

The embedded single-page console is served at `/` by `meshportd`; configure `web_dir` to use custom assets or `off` to disable it. The design-derived template is `admin.tpl.html`, with color/icons/English–Chinese–Japanese dictionaries in `theme.js`, additional controls in `strings.js`, renderer `dc.js`, and live API logic `app.js`.

Style and event declarations in the rendered template use `data-s` and `data-on-*` for compatibility with the content security policy. Responses use content ETags and `Cache-Control: no-cache`. Console sessions use HttpOnly SameSite cookies, same-origin API requests, and strict CSP/frame restrictions. Data returned by the server is rendered as text, not executable markup.

| View | Representative API endpoints |
|---|---|
| Overview | `GET /admin/overview`, `WS /admin/stream` |
| Devices | `/admin/devices`, `/admin/groups`, `/admin/enroll-tokens` |
| Communications | `/admin/sessions`, `POST /admin/sessions/{id}/disconnect` |
| FRP | `/admin/frp/nodes`, `/admin/frp/clients`, `/admin/frp/mappings`, `/admin/frp/stop-all` |
| Access & Approvals | `/admin/grants`, `/admin/levels`, `/admin/users` |
| Security | `/admin/security/logins`, `keys`, `mfa`, `tokens`, `acl`, `bans`, `/admin/alerts` |
| Monitoring & Logs | `/admin/metrics`, `/admin/metrics/p2p`, `/admin/failures`, `/admin/logs`, `/admin/audit` |
| System & API | `/admin/system/status`, `versions`, `db`, `backups`, `webhooks`, `api-keys`, `update`, `settings`, `endpoints` |

The device client also uses `/peers`, `/ws`, `/messages`, `/files`, `/rtc/ice-servers`, `/me/mappings`, `/me/frpc`, `/frp/nodes`, and grant endpoints. Device-level admin FRP operations are supported for selected `/admin/frp/*` routes.

## 8. Source layout

```text
cmd/meshportd      Server CLI (init / serve / admin / backup / openapi)
cmd/meshctl        Device test CLI
internal/config    YAML configuration
internal/store     SQLite migrations, models, queries
internal/auth      Passwords, TOTP, tokens, Ed25519, TURN, webhooks
internal/policy    Central authorization
internal/hub       Device WebSocket relay/signaling/presence/commands
internal/frp       frps plugin, config generation, managed processes
internal/monitor   OS metrics and time-series sampling
internal/events    Events and log ingestion
internal/api       REST endpoints, OpenAPI, background jobs
pkg/proto          WebSocket protocol and signed payloads
pkg/client         Go client SDK
deploy/            Installation, systemd, coturn, Caddy
```
