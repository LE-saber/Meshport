# Cloud builds and releases

Repository: https://github.com/LE-saber/Meshport

## Source validation

Pushes to `main` and pull requests run CI. It checks Go formatting, static analysis, client-side JavaScript, integration tests with real frps/frpc binaries, race detection, and cross-compilation for Linux and Windows on amd64/arm64.

The server uses Linux-specific interfaces. Run the full `go test ./...` suite on Linux.

## Draft releases

1. In GitHub, open **Actions → Release → Run workflow**.
2. Select `main` and optionally supply a version such as `0.1.0-rc.1` (without the leading `v`). The default is `0.0.0-dev.<run number>`.
3. Check that server, desktop, Android, and publishing jobs pass.
4. Download the artifacts from Actions or the draft Release and test them on real devices.

Manual workflows create a **draft** rather than immediately publishing a public release.

| Artifact | Platform |
|---|---|
| `meshport-server-<version>-linux-<arch>.tar.gz` | Linux amd64/arm64 |
| `meshport-desktop-<version>-linux-<arch>.tar.gz` | Linux amd64/arm64 |
| `meshport-desktop-<version>-windows-<arch>.zip` | Windows amd64/arm64 |
| `meshport-android-<version>-<ABI>.apk` | Universal, arm64-v8a, armeabi-v7a, x86_64 |
| `SHA256SUMS.txt` | Release checksums |

The Android `anet` dependency uses Go internal symbols. Go 1.23+ needs `-ldflags "-checklinkname=0"` for this integration; the build workflow and `make android-aar` already apply it. See the [upstream anet notes](https://github.com/wlynxg/anet#how-to-build-with-go-1230-or-later). Recheck device networking behavior when upgrading Go or the dependency.

## Android signing

Configure these repository **Settings → Secrets and variables → Actions** secrets:

- `ANDROID_KEYSTORE_BASE64`
- `ANDROID_KEYSTORE_PASSWORD`
- `ANDROID_KEY_ALIAS`
- `ANDROID_KEY_PASSWORD`

Without them, the workflow may sign builds using a temporary debug key, so the resulting APK may not upgrade an earlier installation. Preserve the production signing key securely and never commit it or its passwords.

## Publishing

Publish a verified draft only after CI, desktop/Android device checks, and NAT/TURN testing pass. Alternatively, a `v<version>` tag triggers the workflow and a public release directly.

The management console is embedded from `internal/console/web`. `make console` runs syntax and frontend tests; production upgrades also require checking console authentication, APIs, and WebSocket connections on the target host. Set an old external `web_dir` to empty when switching to the embedded console. SSH helper scripts are packaged under `deploy/access`.
