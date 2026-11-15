# Meshport

**Meshport** is a self-hosted private communication and device-management platform. It combines end-to-end encrypted messaging and file transfers, WebRTC peer-to-peer connectivity, and centrally managed FRP service publishing.

- **Server — `meshportd`:** manages identities, devices, permissions, signaling, relay, monitoring, and REST/WebSocket APIs.
- **Desktop — `meshport`:** Windows and Linux applications, with a local browser-based UI, system tray, and embedded frpc.
- **Android:** a Kotlin application with the same Go-based client core and a foreground connection service.
- **Control panel:** an embedded web console for device enrollment, communications, FRP mappings, permissions, security, and operations.

> **Language:** English is the default for new installations. The server console also supports Traditional Chinese, Simplified Chinese, and Japanese. Desktop and Android clients support English, Traditional Chinese, and Simplified Chinese. Previously selected languages remain available; existing settings are not overwritten.

## Features

| Feature | What it does |
|---|---|
| Encrypted communications | Text and file transfers with client-side end-to-end encryption and delivery/read receipts |
| Connection modes | P2P only, relay only, or P2P first with TURN/server-relay fallback |
| Offline delivery | Encrypted store-and-forward messages and resumable relay file transfers |
| FRP service publishing | Centralized frps plugin authorization, device-scoped mappings, embedded frpc, and mapping lifecycle controls |
| Device management | Ed25519 device identities, individual enrollment codes, QR enrollment, approvals, groups, presence, and remote commands |
| Access control | Device levels (admin/member/guest), console roles (admin/operator/viewer), scoped API tokens, and time-limited access grants |
| Security | bcrypt, optional/required TOTP MFA, audit trails, IP controls, key rotation, and signed webhooks |
| Operations | Live metrics, logs, alerts, scheduled backups, update configuration, and REST/OpenAPI documentation |

## Repository layout

| Directory | Purpose |
|---|---|
| `cmd/meshportd`, `internal/{api,hub,store,frp}` | Server, transport, policy, persistence, and FRP orchestration |
| `internal/console/web` | Embedded server administration console |
| `internal/agent`, `internal/agent/web` | Cross-platform client core and embedded local UI |
| `cmd/meshport` | Desktop application for Windows and Linux |
| `mobile/meshcore`, `android/` | gomobile integration and Android application |
| `pkg/client`, `pkg/proto` | Go client SDK and protocol definitions |
| `deploy/`, `packaging/` | Deployment and application packaging |
| `docs/` | Technical documentation |

## Quick start (local development)

Requires Go and the project's build tools.

```bash
make build
./bin/meshportd init -config ./dev/meshport.yaml -data-dir ./dev/data \
    -public-url http://127.0.0.1:8080 -listen 127.0.0.1:8080
./bin/meshportd serve -config ./dev/meshport.yaml
```

Open `http://127.0.0.1:8080/` and sign in with the administrator credentials printed by `init`. API documentation is available at `http://127.0.0.1:8080/api/docs`.

To manage a local frps, add `-frps-public-addr 127.0.0.1 -frps-binary /path/to/frps` to `init`. Build frps/frpc with `make frp-bins`.

## Development and validation

```bash
make test       # Go vet and tests
make console    # Console JavaScript checks and regression tests
make race       # Race detector
make e2e        # Real frps/frpc integration (builds the binaries)
node --test tests/client_i18n.test.cjs
```

Real-device validation is still required before relying on desktop notifications/tray behavior, Android lifecycle handling, and cross-NAT/TURN connectivity. Refer to [Client testing](docs/CLIENT.md#11-testing) for coverage and limitations.

## Documentation

- [Client applications, encryption, and builds](docs/CLIENT.md)
- [Server architecture and permissions](docs/ARCHITECTURE.md)
- [Deployment](docs/DEPLOY.md)
- [SSH-only administrative deployment](docs/PRIVATE_DEPLOY.md)
- [Secure enrollment and languages](docs/SECURE_ENROLLMENT.md)
- [Server console](docs/CONSOLE.md)
- [API and WebSocket protocol](docs/API.md)
- [Release workflow](docs/RELEASE.md)
- [OpenAPI 3.1 specification](docs/openapi.json)

## Releases

GitHub Actions builds Linux server packages, Linux/Windows desktop packages, and Android APKs. Prefer manually triggered draft releases for evaluation; tagged `v*` releases publish public artifacts. See [Release guide](docs/RELEASE.md).

The source code, documentation, and security-sensitive deployment details should be reviewed and tested for your own environment before production use.
