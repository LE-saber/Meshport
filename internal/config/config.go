// Package config loads the meshportd YAML configuration file.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

type TLS struct {
	// Mode is one of: off (plain HTTP, e.g. behind Caddy/nginx), file, acme.
	Mode        string   `yaml:"mode"`
	CertFile    string   `yaml:"cert_file"`
	KeyFile     string   `yaml:"key_file"`
	ACMEDomains []string `yaml:"acme_domains"`
	ACMEEmail   string   `yaml:"acme_email"`
}

type TURN struct {
	// URLs handed to clients, e.g. "turn:mesh.example.com:3478?transport=udp".
	URLs []string `yaml:"urls"`
	// Secret shared with coturn (static-auth-secret, use-auth-secret).
	Secret string        `yaml:"secret"`
	TTL    time.Duration `yaml:"ttl"`
	// HealthAddr is a host:port probed with a STUN binding request.
	HealthAddr string `yaml:"health_addr"`
}

type FRP struct {
	// AuthToken is frps auth.token; every frpc must present it.
	AuthToken string `yaml:"auth_token"`
	// Binary is the frps executable used for managed (local) nodes.
	Binary string `yaml:"binary"`
	// ConfigDir is where generated frps.toml files for managed nodes go.
	ConfigDir string `yaml:"config_dir"`
	// PollInterval for frps dashboard statistics.
	PollInterval time.Duration `yaml:"poll_interval"`
}

type Update struct {
	// ManifestURL returns {"version","url","sha256","signature","notes"} per channel.
	ManifestURL string `yaml:"manifest_url"`
	// PublicKey (base64 ed25519) used to verify the manifest signature of the binary hash.
	PublicKey string `yaml:"public_key"`
}

type Config struct {
	Listen         string   `yaml:"listen"`
	InternalListen string   `yaml:"internal_listen"`
	PublicURL      string   `yaml:"public_url"`
	DataDir        string   `yaml:"data_dir"`
	WebDir         string   `yaml:"web_dir"`
	LogLevel       string   `yaml:"log_level"`
	TrustedProxies []string `yaml:"trusted_proxies"`
	TLS            TLS      `yaml:"tls"`
	TURN           TURN     `yaml:"turn"`
	FRP            FRP      `yaml:"frp"`
	Update         Update   `yaml:"update"`

	path string
}

func (c *Config) Path() string { return c.path }

func Default() *Config {
	return &Config{
		Listen:         ":8080",
		InternalListen: "127.0.0.1:7401",
		PublicURL:      "http://localhost:8080",
		DataDir:        "/var/lib/meshport",
		LogLevel:       "info",
		TrustedProxies: []string{"127.0.0.1/32", "::1/128"},
		TLS:            TLS{Mode: "off"},
		TURN:           TURN{TTL: 12 * time.Hour},
		FRP: FRP{
			Binary:       "/usr/local/bin/frps",
			PollInterval: 5 * time.Second,
		},
	}
}

func Load(path string) (*Config, error) {
	c := Default()
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	c.path = path
	return c, c.normalize()
}

func (c *Config) normalize() error {
	if c.DataDir == "" {
		return fmt.Errorf("data_dir is required")
	}
	if c.FRP.ConfigDir == "" {
		c.FRP.ConfigDir = filepath.Join(c.DataDir, "frps")
	}
	if c.FRP.PollInterval <= 0 {
		c.FRP.PollInterval = 5 * time.Second
	}
	if c.TURN.TTL <= 0 {
		c.TURN.TTL = 12 * time.Hour
	}
	switch c.TLS.Mode {
	case "", "off":
		c.TLS.Mode = "off"
	case "file":
		if c.TLS.CertFile == "" || c.TLS.KeyFile == "" {
			return fmt.Errorf("tls.mode=file needs cert_file and key_file")
		}
	case "acme":
		if len(c.TLS.ACMEDomains) == 0 {
			return fmt.Errorf("tls.mode=acme needs acme_domains")
		}
	default:
		return fmt.Errorf("unknown tls.mode %q", c.TLS.Mode)
	}
	return nil
}

// Normalize is exported for configs built in code (tests).
func (c *Config) Normalize() error { return c.normalize() }

func (c *Config) Save(path string) error {
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func RandomSecret(nbytes int) string {
	b := make([]byte, nbytes)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
