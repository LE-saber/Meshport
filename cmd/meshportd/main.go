// Command meshportd is the Meshport server: unified identity, device,
// permission, messaging relay, P2P signalling, FRP control and console API.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"meshport/internal/api"
	"meshport/internal/auth"
	"meshport/internal/config"
	"meshport/internal/events"
	"meshport/internal/policy"
	"meshport/internal/store"
)

const defaultConfig = "/etc/meshport/meshport.yaml"

func usage() {
	fmt.Fprintf(os.Stderr, `meshportd %s

Usage:
  meshportd init    [flags]   create config, database, first admin and (optionally) a local frps node
  meshportd serve   [-config]  run the server
  meshportd admin   [-config] reset-password|create <username> [-role admin|operator|viewer|user]
  meshportd backup  [-config]  write a database snapshot into <data_dir>/backups
  meshportd openapi [-config]  print the OpenAPI document
  meshportd version

Run "meshportd <command> -h" for flags.
`, api.Version)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "init":
		err = cmdInit(os.Args[2:])
	case "serve":
		err = cmdServe(os.Args[2:])
	case "admin":
		err = cmdAdmin(os.Args[2:])
	case "backup":
		err = cmdBackup(os.Args[2:])
	case "openapi":
		err = cmdOpenAPI(os.Args[2:])
	case "version", "-v", "--version":
		fmt.Println(api.Version)
	case "-h", "--help", "help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func openAll(path string) (*config.Config, *store.DB, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, nil, err
	}
	db, err := store.Open(filepath.Join(cfg.DataDir, "meshport.db"))
	if err != nil {
		return nil, nil, err
	}
	return cfg, db, nil
}

func newLogger(level string, bus *events.Bus) (*slog.Logger, *events.LogSink) {
	var l slog.Level
	_ = l.UnmarshalText([]byte(level))
	inner := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l})
	sink := events.NewLogSink(inner, bus)
	return slog.New(sink), sink
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	path := fs.String("config", defaultConfig, "config file")
	_ = fs.Parse(args)
	cfg, db, err := openAll(*path)
	if err != nil {
		return err
	}
	defer db.Close()
	bus := events.NewBus()
	log, sink := newLogger(cfg.LogLevel, bus)
	srv, err := api.New(api.Options{Config: cfg, DB: db, Bus: bus, Logs: sink, Logger: log})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return srv.ListenAndServe(ctx)
}

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	path := fs.String("config", defaultConfig, "config file to create")
	dataDir := fs.String("data-dir", "/var/lib/meshport", "data directory")
	publicURL := fs.String("public-url", "", "public URL of the console/API, e.g. https://mesh.example.com (required)")
	listen := fs.String("listen", ":8080", "listen address")
	tlsMode := fs.String("tls", "off", "off (behind a reverse proxy) | acme | file")
	adminName := fs.String("admin", "admin", "first admin username")
	frpsAddr := fs.String("frps-public-addr", "", "create a managed local frps node reachable at this IP/hostname")
	frpsName := fs.String("frps-name", "main", "name of the local frps node")
	frpsPorts := fs.String("frps-allow-ports", "20000-29999", "remote ports frpc may bind on the local node")
	subdomain := fs.String("frps-subdomain-host", "", "subdomain host for http/https mappings, e.g. apps.example.com")
	httpPort := fs.Int("frps-http-port", 8081, "frps vhost HTTP port (0 disables)")
	httpsPort := fs.Int("frps-https-port", 8444, "frps vhost HTTPS port (0 disables)")
	turnHost := fs.String("turn-host", "", "hostname of coturn (adds STUN/TURN URLs and a shared secret)")
	frpsBin := fs.String("frps-binary", "/usr/local/bin/frps", "frps executable")
	force := fs.Bool("force", false, "overwrite an existing config")
	_ = fs.Parse(args)

	if *publicURL == "" {
		return fmt.Errorf("-public-url is required")
	}
	u, err := url.Parse(*publicURL)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid -public-url")
	}
	if _, err := os.Stat(*path); err == nil && !*force {
		return fmt.Errorf("%s exists (use -force to overwrite)", *path)
	}
	cfg := config.Default()
	cfg.DataDir = *dataDir
	cfg.PublicURL = strings.TrimSuffix(*publicURL, "/")
	cfg.Listen = *listen
	cfg.TLS.Mode = *tlsMode
	if *tlsMode == "acme" {
		cfg.TLS.ACMEDomains = []string{u.Hostname()}
	}
	cfg.FRP.AuthToken = config.RandomSecret(24)
	cfg.FRP.Binary = *frpsBin
	if *turnHost != "" {
		cfg.TURN.Secret = config.RandomSecret(24)
		cfg.TURN.URLs = []string{"stun:" + *turnHost + ":3478", "turn:" + *turnHost + ":3478?transport=udp", "turn:" + *turnHost + ":3478?transport=tcp"}
		cfg.TURN.HealthAddr = net.JoinHostPort(*turnHost, "3478")
	}
	if err := cfg.Normalize(); err != nil {
		return err
	}
	if err := cfg.Save(*path); err != nil {
		return err
	}
	db, err := store.Open(filepath.Join(cfg.DataDir, "meshport.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.UserByName(*adminName); err == nil {
		fmt.Printf("admin user %s already exists; keeping it\n", *adminName)
	} else {
		pw := auth.RandomPassword()
		h, err := auth.HashPassword(pw)
		if err != nil {
			return err
		}
		if err := db.CreateUser(&store.User{Username: *adminName, DisplayName: "Administrator", PasswordHash: h, Role: policy.RoleAdmin}); err != nil {
			return err
		}
		fmt.Printf("created admin user %q with password: %s\n(change it after the first login)\n", *adminName, pw)
	}
	for _, g := range []string{"home", "office", "mobile", "servers"} {
		_, _ = db.GroupIDByName(g, true)
	}
	if *frpsAddr != "" {
		if _, err := db.NodeByID(*frpsName); err == nil {
			fmt.Printf("frps node %s already exists\n", *frpsName)
		} else {
			n := &store.Node{Name: *frpsName, Region: "local", PublicAddr: *frpsAddr, BindPort: 7000, VhostHTTPPort: *httpPort, VhostHTTPSPort: *httpsPort,
				SubdomainHost: *subdomain, AllowPorts: *frpsPorts, DashboardAddr: "127.0.0.1:7500", DashboardUser: "meshport", DashboardPass: auth.RandomHex(16),
				PluginToken: auth.RandomHex(24), Managed: true, Enabled: true}
			if err := db.CreateNode(n); err != nil {
				return err
			}
			fmt.Printf("created managed frps node %q (bind 7000, http %d, https %d, ports %s)\n", n.Name, n.VhostHTTPPort, n.VhostHTTPSPort, n.AllowPorts)
		}
	}
	fmt.Printf("wrote %s\nstart with: meshportd serve -config %s\n", *path, *path)
	if cfg.TURN.Secret != "" {
		fmt.Printf("coturn: set use-auth-secret and static-auth-secret=%s in /etc/turnserver.conf\n", cfg.TURN.Secret)
	}
	return nil
}

func cmdAdmin(args []string) error {
	fs := flag.NewFlagSet("admin", flag.ExitOnError)
	path := fs.String("config", defaultConfig, "config file")
	role := fs.String("role", "admin", "role for create")
	_ = fs.Parse(args)
	rest := fs.Args()
	if len(rest) < 2 {
		return fmt.Errorf("usage: meshportd admin [-config f] reset-password|create|disable-mfa <username>")
	}
	_, db, err := openAll(*path)
	if err != nil {
		return err
	}
	defer db.Close()
	pw := auth.RandomPassword()
	h, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	switch rest[0] {
	case "reset-password":
		res, err := db.Exec(`UPDATE users SET password_hash=?, disabled=0 WHERE username=?`, h, rest[1])
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("no such user")
		}
		_, _ = db.Exec(`DELETE FROM web_sessions WHERE user_id=(SELECT id FROM users WHERE username=?)`, rest[1])
		fmt.Printf("new password for %s: %s\n", rest[1], pw)
	case "create":
		if !policy.ValidRole(*role) {
			return fmt.Errorf("invalid role")
		}
		if err := db.CreateUser(&store.User{Username: rest[1], PasswordHash: h, Role: *role}); err != nil {
			return err
		}
		fmt.Printf("created %s (%s) with password: %s\n", rest[1], *role, pw)
	case "disable-mfa":
		if _, err := db.Exec(`UPDATE users SET mfa_enabled=0, mfa_secret='' WHERE username=?`, rest[1]); err != nil {
			return err
		}
		fmt.Println("MFA disabled for", rest[1])
	default:
		return fmt.Errorf("unknown admin command %q", rest[0])
	}
	return nil
}

func cmdBackup(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	path := fs.String("config", defaultConfig, "config file")
	_ = fs.Parse(args)
	cfg, db, err := openAll(*path)
	if err != nil {
		return err
	}
	defer db.Close()
	srv, err := api.New(api.Options{Config: cfg, DB: db, Logger: slog.New(slog.NewTextHandler(os.Stderr, nil))})
	if err != nil {
		return err
	}
	id, err := srv.Backup("manual")
	if err != nil {
		return err
	}
	var file string
	_ = db.QueryRow(`SELECT file FROM backups WHERE id=?`, id).Scan(&file)
	fmt.Println(filepath.Join(cfg.DataDir, "backups", file))
	return nil
}

func cmdOpenAPI(args []string) error {
	fs := flag.NewFlagSet("openapi", flag.ExitOnError)
	path := fs.String("config", "", "config file (optional; only public_url is used)")
	_ = fs.Parse(args)
	cfg := config.Default()
	if *path != "" {
		c, err := config.Load(*path)
		if err != nil {
			return err
		}
		cfg = c
	}
	tmp, err := os.MkdirTemp("", "meshport-openapi")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	cfg.DataDir = tmp
	db, err := store.Open(filepath.Join(tmp, "x.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	srv, err := api.New(api.Options{Config: cfg, DB: db, Logger: slog.New(slog.NewTextHandler(os.Stderr, nil))})
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(srv.OpenAPI())
}
