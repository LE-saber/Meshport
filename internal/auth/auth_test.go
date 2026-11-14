package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestTOTPRFC6238Vector(t *testing.T) {
	// RFC 6238 appendix B, SHA-1 secret "12345678901234567890": T=59 -> 94287082 (8 digits)
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("12345678901234567890"))
	if got := TOTPCode(secret, time.Unix(59, 0)); got != "287082" {
		t.Fatalf("TOTP(59) = %s, want 287082", got)
	}
	if got := TOTPCode(secret, time.Unix(1111111109, 0)); got != "081804" {
		t.Fatalf("TOTP(1111111109) = %s, want 081804", got)
	}
	now := time.Unix(1111111109, 0)
	if !CheckTOTP(secret, "081804", now) || !CheckTOTP(secret, "081804", now.Add(30*time.Second)) {
		t.Fatal("expected code to be accepted with one step of drift")
	}
	if CheckTOTP(secret, "081804", now.Add(90*time.Second)) {
		t.Fatal("code accepted outside the drift window")
	}
}

func TestPasswords(t *testing.T) {
	SetFastHashing()
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("short password accepted")
	}
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(h, "correct horse battery") || CheckPassword(h, "wrong password!") || CheckPassword("", "x") {
		t.Fatal("password check mismatch")
	}
}

func TestTokens(t *testing.T) {
	tok, h := NewToken("mpd")
	if !strings.HasPrefix(tok, "mpd_") || HashToken(tok) != h || len(h) != 64 {
		t.Fatal("bad token/hash")
	}
}

func TestDeviceSignatures(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	b64 := base64.StdEncoding.EncodeToString(pub)
	p, err := ParsePublicKey(b64)
	if err != nil {
		t.Fatal(err)
	}
	msg := DeviceLoginMessage("dev_x", "nonce")
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, msg))
	if !VerifySignature(p, msg, sig) || VerifySignature(p, DeviceLoginMessage("dev_y", "nonce"), sig) {
		t.Fatal("signature verification wrong")
	}
	if !strings.HasPrefix(Fingerprint(p), "SHA256:") {
		t.Fatal("fingerprint format")
	}
	if _, err := ParsePublicKey("AAAA"); err == nil {
		t.Fatal("short key accepted")
	}
}

func TestTURNCredentials(t *testing.T) {
	// reference value computed independently: base64(hmac.new(b"secret", b"1700000000:dev_a", sha1))
	u, c, exp := TURNCredentials("secret", "dev_a", time.Hour, time.Unix(1699996400, 0))
	if u != "1700000000:dev_a" || exp.Unix() != 1700000000 {
		t.Fatalf("username %s", u)
	}
	if c != "7p1V0m1z34vS0aYvc3E4NYWA+ZI=" {
		t.Fatalf("credential %s", c)
	}
}

func TestWebhookSignature(t *testing.T) {
	sig := SignWebhook("whsec", []byte(`{"a":1}`), time.Unix(1700000000, 0))
	if !strings.HasPrefix(sig, "t=1700000000,v1=") || len(sig) != len("t=1700000000,v1=")+64 {
		t.Fatalf("bad signature %s", sig)
	}
}
