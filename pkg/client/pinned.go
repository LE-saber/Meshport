package client

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"meshport/pkg/enrollment"
)

// NewWithPin trusts only the server key distributed in the device's invitation.
// Empty pins retain the platform CA verification used by existing installations.
func NewWithPin(server, pin string) *Client {
	c := New(server)
	if pin == "" {
		return c
	}
	want, err := enrollment.PinBytes(pin)
	u, parseErr := url.Parse(server)
	if err != nil || parseErr != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		c.configurationError = errors.New("invalid pinned HTTPS server configuration")
		return c
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
		// The invitation's exact SPKI pin replaces CA trust. VerifyConnection
		// runs on every handshake, including resumed sessions; TLS still proves
		// possession of the corresponding private key.
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("server certificate missing")
			}
			cert := cs.PeerCertificates[0]
			got := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
			if subtle.ConstantTimeCompare(got[:], want) != 1 {
				return errors.New("server public key does not match this device's invitation")
			}
			now := time.Now()
			if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
				return errors.New("server identity certificate is outside its validity period")
			}
			return nil
		},
	}
	c.HTTP.Transport = transport
	c.HTTP.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || !strings.EqualFold(req.URL.Host, u.Host) {
			return errors.New("pinned connection cannot redirect to a different server or plaintext HTTP")
		}
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		return nil
	}
	return c
}
