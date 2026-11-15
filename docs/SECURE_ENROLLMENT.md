# Secure enrollment, QR codes, and languages

Meshport uses English as the default language for new installations. The client supports English, Traditional Chinese, and Simplified Chinese; the server console additionally supports Japanese. Language controls appear on the client enrollment/settings screens and in the console toolbar/settings. UI translations never modify device names, messages, file names, or cryptographic key material.

Client public keys and fingerprints are hidden until the user selects **View my keys**. On first access, the user creates a local key-view password of at least eight characters; subsequent access always requires it. Only a salted verifier is saved. The key viewer automatically hides secrets on dismissal, when backgrounded, or after 60 seconds. This password controls display of key information, not the device's underlying file-storage security.

Grant durations are `1h / 6h / 24h / 7d / 30d / perm`. A permanent grant is separate from the lifetime of the one-time enrollment credential.

## One code per device

Under **Devices → Enroll device**, generate a `mpjoin1.` enrollment code and its QR representation. Each one-time code enrolls **one** device. Android can scan a QR code with its camera; Android and desktop clients can also import a QR image or paste the code. If camera access is denied, image import and manual entry remain available.

The enrollment code contains the HTTPS endpoint, a unique secret, and the SHA-256 SPKI fingerprint of the server TLS public key. The client verifies the fingerprint **before** transmitting the enrollment secret and pins it for subsequent HTTPS, WSS, and file requests. A mismatched key, server-crossing redirect, or plaintext downgrade is rejected. Devices independently generate Ed25519 identity private keys and X25519 end-to-end encryption keys.

Multiple devices may trust the same server public key, but their enrollment secrets and private keys differ. A used code cannot enroll another device; revoke unused leaked codes in the console.

## Deployment

The private administration endpoint remains on `127.0.0.1:18080`, reachable over SSH forwarding. The device gateway exposes only the allowlisted routes in `deploy/Caddyfile.pinned`; all other paths return 404.

After provisioning a stable TLS private key and certificate, configure:

```yaml
enrollment_url: https://PUBLIC_IP_OR_HOST
enrollment_tls_cert: /etc/meshport/tls/device.crt
```

The gateway serves the same certificate. The backend reads only the public certificate; it does not need the private key. Configure the reverse proxy to override `X-Forwarded-Proto`, and limit `trusted_proxies` to the actual gateway. Public-key pinning allows use of a self-signed certificate without disabling TLS verification.

Back up the TLS private key. Certificate renewal can reuse the same public/private key pair, but changing the public key requires re-establishing trust on enrolled clients. An older HTTP device entry point may remain on port 80 for compatibility, **but secure codes must never be used over HTTP**.

Database migration adds `enroll_tokens.tls_required`; back up the database first. Legacy `mpe_` codes remain supported for deployments that do not enable secure enrollment. Do not extract or reuse the short token inside a secure code.

## Development checks

Translation dictionaries are committed to the repository; no Node.js service is needed at runtime. To regenerate Simplified Chinese resources, run `npm ci && npm run generate` in `scripts/i18n`. The packaged QR decoder is jsQR; see `internal/agent/web/vendor/jsQR.LICENSE`.

Check all console and client locales, one-time code reuse rejection, correct and incorrect TLS pins, downgrade protection, trusted proxies, encrypted transfers, pinned key persistence across client restarts, and permanent grants.
