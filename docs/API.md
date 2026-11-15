# Meshport API reference

The complete machine-readable API is available at `GET /api/v1/openapi.json` (OpenAPI 3.1), the embedded interactive documentation at `GET /api/docs`, and the checked-in specification at [openapi.json](openapi.json). This guide covers authentication, device/WebSocket integration, transports, and protocol conventions.

## 1. General conventions

- Base URL: `https://<domain>`. REST routes begin with `/api/v1`.
- Requests and responses use UTF-8 JSON; timestamps are RFC 3339 UTC, with `null` for absent dates.
- List responses generally use `{"items":[...]}` and may include `counts` or `summary`.
- Error responses use `{"error":{"code":"forbidden","message":"..."}}`.
- HTTP status codes: `400` invalid request, `401` unauthenticated, `403` forbidden, `404` not found, `409` conflict, `422` validation error, and `429` temporarily blocked.

Access tokens are accepted with `Authorization: Bearer <token>`:

| Prefix | Credential | How obtained | Default lifetime |
|---|---|---|---|
| `mps_` | Console session | `POST /auth/login`; also sets an HttpOnly cookie | 12 hours |
| `mpd_` | Device access token | Ed25519 challenge-response login | 24 hours |
| `mpt_` | Personal API token | Security Center | Configurable |
| `mpk_` | Service API key | System & API | Configurable |
| `mpe_` | Legacy device enrollment token | Devices → Enroll device | 24 hours |

Cookie-authenticated writes require `X-Requested-With` as CSRF protection. Browser WebSockets cannot set custom headers and may use `?access_token=` instead.

## 2. Console login and MFA

```http
POST /api/v1/auth/login
{"username":"admin","password":"…","totp":"123456"}
```

A successful response includes an `mps_` token, expiry, user information, and `mfa_setup_required`. If MFA is configured but `totp` is omitted, the server returns `401 mfa_required`. When MFA is mandatory, users without MFA cannot access `/admin/*` until they complete `/auth/mfa/setup` and `/auth/mfa/enable`.

## 3. Device enrollment and authentication

Each device generates an Ed25519 identity key and X25519 E2E encryption key. Private keys remain on the device; public keys and signatures use standard base64 encoding.

Legacy token-based registration: `POST /api/v1/devices/register`.

```json
{
  "name": "Pixel 9",
  "os": "Android 15",
  "kind": "phone",
  "pubkey": "<base64 ed25519 public key>",
  "box_pubkey": "<base64 x25519 public key>",
  "ts": 1794446990,
  "sig": "<sign(priv, 'meshport-register:' + pubkey + ':' + ts)>",
  "enroll_token": "mpe_…"
}
```

Enrollment settings determine automatic approval, device level, and group. Self-registration can instead supply username/password (and `totp`) when `open_registration=true`. The response is `201` with `device` and `status: active|pending`. For newer, TLS-pinned `mpjoin1.` enrollment codes, see [Secure enrollment](SECURE_ENROLLMENT.md).

**Challenge-response login:**

```http
POST /api/v1/devices/auth/challenge
{"device_id":"dev_…"}
```

The server returns `nonce` and `message: "meshport-login:dev_…:<nonce>"`.

```http
POST /api/v1/devices/auth/login
{"device_id":"dev_…","nonce":"…","signature":"<sign(priv, message)>"}
```

The response includes `mpd_` token, `expires_at`, `device`, `key_rotate_required`, and `capabilities`. Pending devices return `403 device_pending`; disabled/banned devices return `device_disabled` or `device_banned`. Repeat the challenge login when the token expires.

Key rotation uses `POST /api/v1/me/device/rotate-key` with `new_pubkey`, `old_sig`, and `new_sig`. Both keys sign `meshport-rotate:<device_id>:<new_pubkey>`. Administrators may also send a `rotate-key` command.

Go clients may use `pkg/client`:

```go
c := client.New("https://mesh.example.com")
c.Register(ctx, client.RegisterRequest{Name: "MacBook", EnrollToken: tok})
c.Login(ctx)
conn, _ := c.Connect(ctx)
conn.SendMessage(ctx, peerID, proto.ModeAuto, ciphertext)
```

## 4. Device WebSocket: `GET /api/v1/ws`

Every frame is `{"t":type,"id":optional_request_id,"d":payload}`. Requests with `id` receive matching `ack` or `error` frames. The server sends WebSocket ping every 25 seconds; clients may also send `ping`.

### Server-to-device frames

| Type (`t`) | Payload | Meaning |
|---|---|---|
| `hello` | `{device_id,did,level,server_time,queued,heartbeat_s,caps}` | First frame |
| `msg.new` | `{id,from,to,mode,ct,body,ts}` | Encrypted relay message, including queued deliveries |
| `msg.receipt` | `{ids,kind:delivered\|read,by}` | Receipt |
| `presence` | `{device_id,online,last_seen}` | Device presence |
| `signal` | `{from,sid,kind,mode,data}` | P2P signaling |
| `file.offer` | `{file_id,from,name,size,sha256}` | Relay file is downloadable |
| `cmd` | `{cmd_id,name,args}` | Remote command |
| `frp.config` | `{version}` | Fetch `/me/frpc` and reload frpc |
| `level.changed` | `{level,reason}` | Access level changed |
| `grant.update` | `{grant_id,status,target,level}` | Grant request update |
| `session.close` | `{sid,reason}` | Session terminated |
| `kick` | `{reason}` | Device disconnected or replaced |

Commands include `reload-frpc`, `rotate-key`, `refresh-peers`, `disconnect`, `collect-logs`, `restart-client`, and `ping`.

### Device-to-server frames

| Type (`t`) | Payload | Meaning |
|---|---|---|
| `msg.send` | `{to,mode:relay\|auto,cid,ct,body,sid?}` | Relay message; ack includes `cid,id,status:relayed\|queued` |
| `msg.delivered`, `msg.read` | `{ids}` | Receipts |
| `signal` | `{to,sid,kind:offer\|answer\|candidate\|bye,mode,data}` | Forward signaling |
| `session.report` | `{sid,peer,mode,path,via,state,lat_ms,rate_bps,sent,delivered,read,failed,file?,reason?}` | Session telemetry |
| `presence.set` | `{lan_ip,nat_type}` | Network hints |
| `cmd.result` | `{cmd_id,ok,output}` | Remote command result |

`msg.send.body` is base64 E2E ciphertext up to 256 KiB. Larger transfers use `/files`; relay-based `mode=p2p` submissions are rejected as `mode_violation`. Report session state changes immediately and active sessions at least every five seconds. Path values: `p2p/relay/nego/fail/idle`. State values: `up/nego/fail/idle/closed`.

## 5. WebRTC and transport selection

1. Fetch `GET /rtc/ice-servers` for STUN and TURN endpoints with temporary credentials valid for 12 hours.
2. For `p2p`, omit TURN servers. For `auto`, permit TURN. For relay-only realtime transport, use `iceTransportPolicy=relay`.
3. Exchange SDP offer/answer and ICE candidates through `signal` frames.
4. Report `session.report{path:"p2p",state:"up",via:"UDP hole punch"}` once connected.
5. If `auto` fails, switch to encrypted store-and-forward with `msg.send`; if `p2p` fails, report a failure and queue the content locally.

WebRTC DataChannel messages and files are encrypted using client-side X25519 keys; the signaling server does not receive plaintext.

## 6. Resumable relay files

```http
POST  /api/v1/files                 {"to":"…","name":"…","size":123,"sha256":"…"}
PATCH /api/v1/files/{id}           Upload-Offset: 0
HEAD  /api/v1/files/{id}
GET   /api/v1/files/{id}
GET   /api/v1/files/{id}/content
```

Creation returns `id,upload_url,offset,chunk_max`. Upload chunks are limited to 16 MiB. Use HEAD/GET to recover the offset after an interruption. The server validates SHA-256 and returns `422` on mismatch (restart the upload). A completed transfer triggers `file.offer`; downloads support HTTP Range. Retention is controlled by `file_retention_hours` (default 72 hours), with `relay_max_file_mb` as the size cap.

## 7. FRP integration

- `GET /frp/nodes`: list available nodes.
- `POST /me/mappings`: create a mapping (requires member level or higher).

```json
{"name":"ssh-mac","node":"tpe","proto":"tcp","local_port":22,"remote_port":6022}
```

For HTTP/HTTPS, specify `domains` or `subdomain`; for STCP/XTCP specify an optional `secret_key` (generated if omitted). Remote ports must be in the node's `allow_ports` range and unique per node; domains must also be unique. Responses provide `public_addr`.

`GET /me/frpc` returns per-node `frpc.toml` with `metadatas.device_id` and `metadatas.frp_secret`. Refresh it when `frp.config` arrives. Stopped/deleted proxies are excluded; frps authorization also enforces revocation at the next heartbeat/reconnection (up to approximately 10 seconds).

## 8. Access grant requests

- `POST /grants {target,level,reason,duration}`: admin-level devices may request grants no higher than their own level.
- `GET /me/grants`: outgoing and incoming grant requests.
- `grant.update` reports decisions and `level.changed` notifies approved targets.

## 9. Console APIs

Console coverage is mapped in [Architecture §7](ARCHITECTURE.md#7-console-views-and-api-mapping).

- Live stream: `WS /api/v1/admin/stream?events=metrics.tick,device.*,session.update,alert.*,log`; frames contain `{type,time,data}`.
- `metrics.tick` is emitted every two seconds and includes CPU, memory, disk, network bandwidth, device/session counts, and P2P success metrics.
- Historical metrics: `GET /admin/metrics?range=live|1h|24h|7d&keys=cpu,mem&points=120`.
- Device details: `GET /admin/devices/{id}`; mappings: `POST /admin/frp/mappings`.
- Remote command: `POST /admin/devices/{id}/command`, e.g. `{name:"reload-frpc",timeout_s:15}`.
- Webhooks use `X-Meshport-Signature: t=<unix>,v1=<hex HMAC-SHA256(secret, "<t>." + body)>` and `{id,type,time,data}` payloads; failed deliveries retry up to three times.
- Prometheus: `GET /metrics`, requiring a token with read scope.

## 10. Common error codes

| Code | Meaning |
|---|---|
| `unauthorized`, `invalid_credentials`, `invalid_signature`, `invalid_challenge` | Authentication failed |
| `mfa_required`, `invalid_mfa` | MFA required or invalid |
| `too_many_attempts` | Temporarily blocked (429) |
| `device_pending`, `device_disabled`, `device_banned` | Device state prohibits this operation |
| `forbidden` | Insufficient role, level, or visibility |
| `mode_violation` | P2P-only payload attempted via relay |
| `relay_disabled` | Server relay disabled |
| `peer_offline` | Signaling target offline |
| `offset_mismatch`, `checksum_mismatch` | Upload offset or checksum invalid |
| `conflict` | Duplicate or conflicting state |
