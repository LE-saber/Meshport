// Package auth holds the credential primitives: password hashing, TOTP,
// opaque bearer tokens, device identity keys and TURN REST credentials.
package auth

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"meshport/pkg/proto"
)

// ---------- passwords ----------

var bcryptCost = 12

var (
	dummyOnce sync.Once
	dummyHash []byte
)

// SetFastHashing lowers the bcrypt cost; only for tests.
func SetFastHashing() { bcryptCost = bcrypt.MinCost }

func HashPassword(pw string) (string, error) {
	if len(pw) < 10 {
		return "", errors.New("password must be at least 10 characters")
	}
	if len(pw) > 72 {
		return "", errors.New("password must be at most 72 bytes")
	}
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
	return string(b), err
}

func CheckPassword(hash, pw string) bool {
	if hash == "" {
		// burn comparable time so missing users are not distinguishable
		dummyOnce.Do(func() { dummyHash, _ = bcrypt.GenerateFromPassword([]byte(RandomPassword()), bcryptCost) })
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(pw))
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// RandomPassword returns a 20 character password suitable for first-run admin accounts.
func RandomPassword() string {
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 20)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

// ---------- opaque tokens ----------

// NewToken returns a random bearer token "<prefix>_<43 chars>" and its storage hash.
func NewToken(prefix string) (token, hash string) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	token = prefix + "_" + base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token)
}

// HashToken is the SHA-256 hex digest used to store and look up tokens.
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func RandomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---------- TOTP (RFC 6238, SHA-1, 6 digits, 30 s) ----------

func NewTOTPSecret() string {
	b := make([]byte, 20)
	_, _ = rand.Read(b)
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
}

func TOTPURI(issuer, account, secret string) string {
	v := url.Values{}
	v.Set("secret", secret)
	v.Set("issuer", issuer)
	v.Set("algorithm", "SHA1")
	v.Set("digits", "6")
	v.Set("period", "30")
	return "otpauth://totp/" + url.PathEscape(issuer+":"+account) + "?" + v.Encode()
}

func totpAt(secret string, counter uint64) (string, error) {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimRight(secret, "=")))
	if err != nil {
		return "", err
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	code := (binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff) % 1000000
	return fmt.Sprintf("%06d", code), nil
}

// TOTPCode returns the current code (exported for tests and the meshctl helper).
func TOTPCode(secret string, t time.Time) string {
	c, _ := totpAt(secret, uint64(t.Unix()/30))
	return c
}

// CheckTOTP accepts the current code and one step of clock drift either way.
func CheckTOTP(secret, code string, t time.Time) bool {
	code = strings.ReplaceAll(code, " ", "")
	if len(code) != 6 || secret == "" {
		return false
	}
	step := t.Unix() / 30
	for d := int64(-1); d <= 1; d++ {
		c, err := totpAt(secret, uint64(step+d))
		if err == nil && subtle.ConstantTimeCompare([]byte(c), []byte(code)) == 1 {
			return true
		}
	}
	return false
}

// ---------- device identity ----------

func ParsePublicKey(b64 string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		b, err = base64.RawURLEncoding.DecodeString(b64)
	}
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("pubkey must be a base64 ed25519 public key (32 bytes)")
	}
	return ed25519.PublicKey(b), nil
}

func ParseBoxKey(b64 string) error {
	if b64 == "" {
		return nil
	}
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(b) != 32 {
		return errors.New("box_pubkey must be a base64 x25519 public key (32 bytes)")
	}
	return nil
}

// Fingerprint renders "SHA256:<base64>" like OpenSSH.
func Fingerprint(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(h[:])
}

// VerifySignature checks a base64 signature over msg.
func VerifySignature(pub ed25519.PublicKey, msg []byte, sigB64 string) bool {
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		sig, err = base64.RawURLEncoding.DecodeString(sigB64)
	}
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(pub, msg, sig)
}

// Signed message formats live in pkg/proto so external clients can use them.
func DeviceLoginMessage(deviceID, nonce string) []byte {
	return proto.DeviceLoginMessage(deviceID, nonce)
}
func RegisterMessage(pubkey string, ts int64) []byte { return proto.RegisterMessage(pubkey, ts) }
func RotateMessage(deviceID, newPub string) []byte   { return proto.RotateMessage(deviceID, newPub) }

// NewDID returns a display ID like MP-4F2A-91C3.
func NewDID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	h := strings.ToUpper(hex.EncodeToString(b))
	return "MP-" + h[:4] + "-" + h[4:]
}

// ---------- TURN REST API credentials (coturn use-auth-secret) ----------

// TURNCredentials implements draft-uberti-behave-turn-rest: username is
// "<expiry unix>:<id>", credential is base64(HMAC-SHA1(secret, username)).
func TURNCredentials(secret, id string, ttl time.Duration, now time.Time) (username, credential string, expires time.Time) {
	expires = now.Add(ttl).Truncate(time.Second)
	username = strconv.FormatInt(expires.Unix(), 10) + ":" + id
	m := hmac.New(sha1.New, []byte(secret))
	m.Write([]byte(username))
	credential = base64.StdEncoding.EncodeToString(m.Sum(nil))
	return
}

// ---------- webhook signatures ----------

// SignWebhook returns "t=<unix>,v1=<hex hmac-sha256(secret, t.body)>".
func SignWebhook(secret string, body []byte, t time.Time) string {
	ts := strconv.FormatInt(t.Unix(), 10)
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(ts + "."))
	m.Write(body)
	return "t=" + ts + ",v1=" + hex.EncodeToString(m.Sum(nil))
}

func ConstantEq(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
