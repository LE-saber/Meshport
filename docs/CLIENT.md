# Client applications (Windows, Linux, Android)

Meshport desktop and Android applications share the `internal/agent` Go core. It manages identity, connection state, end-to-end encrypted messages and files, embedded frpc, and a loopback-only Web UI. Platform shells present the UI and implement system integration such as notifications, autostart, tray icons, and file handling.

**Languages:** New installations default to English. Traditional Chinese and Simplified Chinese remain selectable on enrollment and settings pages; the selected language is persisted. The server administration console also offers Japanese. See [Secure enrollment and localization](SECURE_ENROLLMENT.md).

## 1. Architecture and platforms

| Platform | Shell | UI window | Background behavior |
|---|---|---|---|
| Windows 10/11 amd64/arm64 | `cmd/meshport`, pure Go | Edge `--app` window or default browser | System tray; optional login autostart via HKCU Run |
| Linux amd64/arm64 | Same Go executable | Chromium/Chrome/Edge/Brave `--app`, fallback `xdg-open` | StatusNotifierItem tray (GNOME may require an AppIndicator extension); `--headless` and systemd user unit |
| Android 7.0+ arm64/armv7/x86_64 | Kotlin app at `android/` | WebView | `specialUse` foreground service; optional boot autostart |

```text
┌────────────── Client process ──────────────────────────┐
│ Embedded Web UI (internal/agent/web)                    │
│    ↕ HTTP + SSE on 127.0.0.1 with local token          │
│ Local API (localapi.go)                                 │
│    ↕                                                   │
│ Agent core                                              │
│   conn.go       Ed25519 authentication, WebSocket       │
│   messaging.go  Delivery modes, receipts, retries       │
│   p2p.go        WebRTC negotiation and reporting        │
│   files.go      Encrypted transfers and relay resume    │
│   e2e.go        NaCl box / XChaCha20-Poly1305          │
│   frpc.go       Embedded frp v0.72 and hot reload       │
│   store.go      State, chat JSONL, transfers, logs      │
└────────────────────────────────────────────────────────┘
               ↕ REST / WebSocket       ↕ frp
             meshportd                frps nodes
```

On Android, gomobile builds `mobile/meshcore` into an AAR. The foreground service calls `Meshcore.start(...)` and loads the returned local UI URL in WebView.

## 2. Enrollment and identity

At first launch, each device creates an Ed25519 identity key (challenge-response signatures) and an X25519 key for end-to-end encryption. The private keys remain in its local `state.json` (mode 0600 on supported filesystems), never sent to the server.

Enrollment is via an administrator-issued enrollment code/QR image, or username/password (+ MFA) self-registration when the server permits it. Secure `mpjoin1.` codes pin the server TLS public key; see [Secure enrollment](SECURE_ENROLLMENT.md). Pending devices show an approval state and poll every 15 seconds until activated.

After activation, `PATCH /me/device` reports the E2E public key, client version, operating system, and LAN IP. **Rotate identity key** requires signatures from old and new private keys. Removing the device enrollment deletes local identity keys and chat history.

## 3. Delivery modes

The default mode is configurable in Settings, and each conversation can override it.

| Mode | Text and files | Offline or unreachable peer |
|---|---|---|
| Prefer P2P (`auto`) | Use an existing direct connection or negotiate WebRTC (including TURN) for up to about eight seconds, then fall back to relay; failed P2P files restart through relay | Encrypted server-side queue |
| P2P only (`p2p`) | Direct WebRTC excluding TURN; renegotiate if the current path uses TURN | Keep content locally pending; retry on reconnection or manually |
| Relay only (`relay`) | WebSocket relay for messages and resumable `/files` uploads | Encrypted server-side queue |

Active sessions report `session.report` every five seconds and on state changes, including path, `via`, latency, bitrate, delivery counters, and transfer progress.

Candidate types determine the reported transport: host pairs are LAN direct; server-reflexive/peer-reflexive candidates indicate UDP hole punching; relay candidates indicate TURN relay (reported as `path=relay`). Simultaneous SDP offers resolve glare by retaining the offer from the lexically larger device ID. Negotiation times out after about 25 seconds; idle connections close after ten minutes.

## 4. End-to-end encryption

**Messages:** NaCl box (X25519 + XSalsa20-Poly1305) using the sender's private key and the recipient's `box_pubkey`. The encrypted envelope uses `{v,k:text|file|ack|read|accept|reject,mid,ts,text?,file?,ids?}`. Receipts and file-accept notifications are encrypted too; altered or incorrectly authenticated envelopes are rejected.

**Files:** Each file uses a random 32-byte XChaCha20-Poly1305 key and 32 KiB encrypted chunks. Each nonce contains a 16-byte prefix and eight-byte chunk index. The final chunk is marked with authenticated additional data to detect truncation and reordering; the receiver also validates the plaintext SHA-256. The file key is inside the encrypted message envelope. Relay uploads use an opaque `<tid>.bin` filename. The server and TURN services only see ciphertext.

## 5. File transfers

**P2P:**

1. Sender transmits a `file` control envelope; recipient replies `accept`.
2. Sender opens the `f:<tid>` data channel and streams encrypted chunks.
3. Backpressure pauses sending if the buffered amount exceeds 4 MiB.
4. Recipient validates and sends `ack`.

**Relay:**

1. Encrypt a temporary file.
2. Create an upload with `POST /files` and send 4 MiB blocks using `PATCH`. Recover from interruptions by querying `Upload-Offset`.
3. Notify the recipient with an encrypted file envelope.
4. Recipient downloads with HTTP Range, resumes at full-chunk boundaries, and deletes the server-side file after completion.

Desktop files are saved to `~/Downloads/Meshport` by default (configurable). Android uses its app-specific external storage and can copy received files to Downloads/Meshport.

## 6. Built-in FRP client

The Go core embeds the frp v0.72 client library; no separate frpc executable is needed. Each enabled node runs its own frpc service.

Configuration is generated by the server at `GET /me/frpc`. The client refetches it on `frp.config`, `reload-frpc`, or manual Reload. Proxy-list changes use `UpdateConfigSource` hot reload; changed server address or credentials restart the affected frpc service.

The Mappings page creates, edits, enables, disables, and deletes device-scoped mappings for member-level devices or higher. It displays their status and public addresses. Logs are written to `frpc.log`. Virtual network adapters are not used.

## 7. Local API and UI security

- Listen only on loopback. Desktop defaults to `127.0.0.1:47100` and selects a random port if occupied; Android uses a random port.
- Reject `Host` headers other than `127.0.0.1`, `localhost`, and `::1` to prevent DNS rebinding.
- Require an installation-specific `ui-token` (file mode 0600). Browsers authenticate via HttpOnly SameSite=Strict cookie; CLI uses `X-Meshport-Token`.
- Desktop windows open using a single-use login code valid for two minutes; the token is not exposed in command-line arguments.
- Mutating requests require `X-Requested-With`, with a strict same-origin CSP.
- Remote user text is inserted as text, not HTML.
- `/local/api/*` proxies only allowlisted device endpoints: mappings, nodes, grants, device info, and files.

## 8. Desktop usage

```bash
meshport                         # Open window and tray (or focus existing window)
meshport --minimized             # Tray only
meshport --headless              # No window/tray; suitable for a service
meshport setup --server https://mesh.example.com --enroll mpe_xxx --name build-01
meshport status
meshport open
```

Data directory: `~/.config/meshport` on Linux and `%APPDATA%\Meshport` on Windows. Override using `--data` or `MESHPORT_DATA`.

Stored data includes `state.json` (settings and keys), `messages/*.jsonl` (conversation history), `meshport.log`, `frpc.log`, and `ui-token`.

The Linux distribution contains `install.sh` for unprivileged installation under `~/.local`. `--service` enables a headless systemd user service; `loginctl enable-linger` keeps it running when logged out. Windows ships as a portable ZIP and can enable login autostart in Settings.

## 9. Android behavior

First launch requests notification permission (Android 13+) and optionally permission to ignore battery optimization to maintain connectivity during sleep.

The foreground service shows connection state and a Quit action; closing the UI does not stop the connection. New-message notifications are shown when the app is not foregrounded. WebView supports picking multiple files, saving received files to Downloads, opening files in another app, and opening external links in the system browser.

## 10. Builds and releases

| Workflow | Trigger | Behavior |
|---|---|---|
| `ci.yml` | Push to main, pull requests | gofmt, vet, UI syntax, real frps/frpc tests (race), cross-compilation |
| `release.yml` | `v*` tag or manual draft run | Server tarballs, Windows/Linux desktop packages, Android APKs by ABI, SHA-256 checksums |

```bash
git tag v1.1.0 && git push origin v1.1.0
make desktop VERSION=1.1.0   # Optional local builds
make android VERSION=1.1.0   # Needs Android SDK/NDK, JDK 17, Gradle 8.9+
```

Configure a stable Android signing key for upgrade compatibility. Generate a keystore (keep it private):

```bash
keytool -genkeypair -v -keystore meshport.jks -alias meshport -keyalg RSA -keysize 4096 -validity 10000
```

Set `ANDROID_KEYSTORE_BASE64`, `ANDROID_KEYSTORE_PASSWORD`, `ANDROID_KEY_ALIAS`, and `ANDROID_KEY_PASSWORD` in GitHub Actions secrets. Unsigned Windows binaries can trigger SmartScreen on first launch. Consult [Release guide](RELEASE.md) for the full process.

## 11. Testing

`go test ./internal/agent/` covers encryption/decryption, invalid keys and tampering, chunk boundaries and truncation/reordering, chat persistence and deduplication, filenames, local API authorization, CSRF/host checks, and forwarding allowlists.

Integration tests start a real `meshportd` in-process with two clients. They cover relay messaging and receipts, WebRTC P2P messages, 300 KB relay files, 2 MB P2P files, and P2P-only queues when peers are offline. With `MESHPORT_FRPS`, embedded frpc is tested against a real frps for TCP forwarding and mapping hot reload. JavaScript localization tests cover English default and retained Chinese options.

**Still requires real-device verification before production release:**

- Windows/Linux windows, tray icons, notifications, and autostart.
- Complete Android app lifecycle, built AAR/APK, and device storage behavior.
- Cross-NAT UDP hole punching and TURN paths (in-process tests primarily exercise local host candidates).
- Android 11+ network-interface enumeration compatibility.
