// Package enrollment encodes a single-device invitation with a trusted server key.
package enrollment

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/url"
	"strings"
)

const Prefix = "mpjoin1."

type Invitation struct {
	Version int    `json:"v"`
	Server  string `json:"server"`
	Token   string `json:"token"`
	Pin     string `json:"pin"`
}

func Pin(cert *x509.Certificate) string {
	h := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return "sha256/" + base64.StdEncoding.EncodeToString(h[:])
}

func PinFromPEM(data []byte) (string, error) {
	b, _ := pem.Decode(data)
	if b == nil || b.Type != "CERTIFICATE" {
		return "", errors.New("enrollment certificate must be PEM")
	}
	c, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		return "", err
	}
	return Pin(c), nil
}

func PinBytes(pin string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(pin, "sha256/"))
	if !strings.HasPrefix(pin, "sha256/") || err != nil || len(b) != sha256.Size {
		return nil, errors.New("invalid server public-key fingerprint")
	}
	return b, nil
}

func (i Invitation) Validate() error {
	u, err := url.Parse(i.Server)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("secure invitation requires an HTTPS server origin")
	}
	if i.Version != 1 || !strings.HasPrefix(i.Token, "mpe_") || len(i.Token) > 256 {
		return errors.New("invalid invitation")
	}
	_, err = PinBytes(i.Pin)
	return err
}

func Encode(i Invitation) (string, error) {
	if err := i.Validate(); err != nil {
		return "", err
	}
	b, err := json.Marshal(i)
	return Prefix + base64.RawURLEncoding.EncodeToString(b), err
}

func Parse(text string) (*Invitation, error) {
	text = strings.TrimSpace(text)
	if len(text) > 4096 {
		return nil, errors.New("invitation is too long")
	}
	if u, err := url.Parse(text); err == nil && u.Fragment != "" {
		text = u.Fragment
	}
	if !strings.HasPrefix(text, Prefix) {
		return nil, errors.New("unsupported invitation format")
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(text, Prefix))
	if err != nil {
		return nil, errors.New("invalid invitation encoding")
	}
	var i Invitation
	if json.Unmarshal(b, &i) != nil {
		return nil, errors.New("invalid invitation data")
	}
	if err := i.Validate(); err != nil {
		return nil, err
	}
	i.Server = strings.TrimRight(i.Server, "/")
	return &i, nil
}
