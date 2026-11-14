// Package api is the unified REST + WebSocket API used by the management
// console, the desktop clients and the mobile clients.
package api

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"meshport/internal/config"
	"meshport/internal/events"
	"meshport/internal/frp"
	"meshport/internal/hub"
	"meshport/internal/monitor"
	"meshport/internal/store"
)

// Version is set at build time with -ldflags "-X meshport/internal/api.Version=...".
var Version = "1.0.0-dev"

type Server struct {
	cfg  *config.Config
	db   *store.DB
	bus  *events.Bus
	logs *events.LogSink
	log  *slog.Logger
	hub  *hub.Hub
	frp  *frp.Manager
	mon  *monitor.Monitor

	mux         *http.ServeMux
	routes      []Route
	acl         *ACL
	trustedNets []*net.IPNet
	started     time.Time

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	backupMu sync.Mutex
	updateMu sync.Mutex

	hooksGen atomic.Int64 // bumped when webhooks change; the dispatcher reloads
}

type Options struct {
	Config *config.Config
	DB     *store.DB
	Bus    *events.Bus
	Logs   *events.LogSink
	Logger *slog.Logger
}

func New(o Options) (*Server, error) {
	s := &Server{cfg: o.Config, db: o.DB, bus: o.Bus, logs: o.Logs, log: o.Logger, mux: http.NewServeMux(), started: time.Now()}
	if s.bus == nil {
		s.bus = events.NewBus()
	}
	for _, c := range s.cfg.TrustedProxies {
		if !strings.Contains(c, "/") {
			if strings.Contains(c, ":") {
				c += "/128"
			} else {
				c += "/32"
			}
		}
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			return nil, err
		}
		s.trustedNets = append(s.trustedNets, n)
	}
	s.acl = newACL(s.db)
	if err := s.acl.Reload(); err != nil {
		return nil, err
	}
	s.hub = hub.New(s.db, s.bus, s.log)
	s.frp = frp.New(s.cfg, s.db, s.bus, s.log, s.hub, s.acl.Denied)
	s.mon = monitor.New(s.db, s.bus, s.cfg.DataDir, s.gauges)
	s.registerRoutes()
	return s, nil
}

func (s *Server) DB() *store.DB     { return s.db }
func (s *Server) Hub() *hub.Hub     { return s.hub }
func (s *Server) FRP() *frp.Manager { return s.frp }
func (s *Server) Routes() []Route   { return s.routes }
func (s *Server) Bus() *events.Bus  { return s.bus }

// Handler returns the public HTTP handler.
func (s *Server) Handler() http.Handler {
	return s.securityHeaders(s.mux)
}

// InternalHandler serves only the frps plugin endpoint (bound to localhost).
func (s *Server) InternalHandler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("POST /frps-plugin/{node}/{token}", s.frp.PluginHandler)
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	return m
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		if r.TLS != nil {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

// Start launches background workers (monitor, FRP poller, jobs, managed frps).
func (s *Server) Start(ctx context.Context) {
	s.ctx, s.cancel = context.WithCancel(ctx)
	if s.logs != nil {
		s.logs.SetPersist(s.persistLogs)
		s.goRun(s.logs.Run)
	}
	s.goRun(s.mon.Run)
	s.goRun(s.frp.RunPoller)
	s.goRun(s.runJobs)
	s.goRun(s.runWebhooks)
	s.goRun(s.runAlerts)
	s.frp.StartManaged(s.ctx, s.pluginAddr())
}

func (s *Server) goRun(f func(context.Context)) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		f(s.ctx)
	}()
}

func (s *Server) pluginAddr() string {
	if s.cfg.InternalListen != "" {
		return s.cfg.InternalListen
	}
	return strings.TrimSuffix(s.cfg.PublicURL, "/") + "/internal"
}

// Stop ends background workers.
func (s *Server) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.frp.StopAllProcs()
	s.hub.CloseAll("server shutting down")
	s.wg.Wait()
}

// ListenAndServe runs the public and internal listeners until ctx ends.
func (s *Server) ListenAndServe(ctx context.Context) error {
	s.Start(ctx)
	defer s.Stop()
	errc := make(chan error, 2)
	pub := &http.Server{Addr: s.cfg.Listen, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
	var internal *http.Server
	if s.cfg.InternalListen != "" {
		internal = &http.Server{Addr: s.cfg.InternalListen, Handler: s.InternalHandler(), ReadHeaderTimeout: 5 * time.Second}
		go func() { errc <- internal.ListenAndServe() }()
	}
	switch s.cfg.TLS.Mode {
	case "file":
		go func() { errc <- pub.ListenAndServeTLS(s.cfg.TLS.CertFile, s.cfg.TLS.KeyFile) }()
	case "acme":
		mgr := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(s.cfg.TLS.ACMEDomains...),
			Cache:      autocert.DirCache(filepath.Join(s.cfg.DataDir, "acme")),
			Email:      s.cfg.TLS.ACMEEmail,
		}
		pub.TLSConfig = &tls.Config{GetCertificate: mgr.GetCertificate, MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1", "acme-tls/1"}}
		// port 80 for HTTP-01 challenges and redirects
		go func() { _ = http.ListenAndServe(":80", mgr.HTTPHandler(nil)) }()
		go func() { errc <- pub.ListenAndServeTLS("", "") }()
	default:
		go func() { errc <- pub.ListenAndServe() }()
	}
	s.log.Info("meshportd listening", "addr", s.cfg.Listen, "tls", s.cfg.TLS.Mode, "internal", s.cfg.InternalListen, "version", Version)
	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = pub.Shutdown(sctx)
	if internal != nil {
		_ = internal.Shutdown(sctx)
	}
	return nil
}

// gauges feeds platform metrics to the monitor every tick.
func (s *Server) gauges() map[string]float64 {
	online := float64(s.hub.ConnCount())
	var p2p, relay, nego float64
	rows, err := s.db.Query(`SELECT path, COUNT(*) FROM comm_sessions WHERE state IN ('up','nego') GROUP BY path`)
	if err == nil {
		for rows.Next() {
			var path string
			var n float64
			if rows.Scan(&path, &n) == nil {
				switch path {
				case "p2p":
					p2p = n
				case "relay":
					relay = n
				case "nego":
					nego = n
				}
			}
		}
		rows.Close()
	}
	var p2pRate float64
	attempted := s.db.Count(`SELECT COUNT(*) FROM comm_sessions WHERE p2p_attempted=1 AND started_at>=?`, time.Now().Add(-time.Hour).Unix())
	if attempted > 0 {
		ok := s.db.Count(`SELECT COUNT(*) FROM comm_sessions WHERE p2p_attempted=1 AND p2p_ok=1 AND started_at>=?`, time.Now().Add(-time.Hour).Unix())
		p2pRate = float64(ok) / float64(attempted) * 100
	} else {
		p2pRate = -1 // no data
	}
	relayBytes := float64(s.hub.RelayBytes.Swap(0))
	var p2pBps, relaySessBps float64
	rows, err = s.db.Query(`SELECT path, COALESCE(SUM(rate_bps),0) FROM comm_sessions WHERE state='up' GROUP BY path`)
	if err == nil {
		for rows.Next() {
			var path string
			var v float64
			if rows.Scan(&path, &v) == nil {
				if path == "p2p" {
					p2pBps = v
				} else if path == "relay" {
					relaySessBps = v
				}
			}
		}
		rows.Close()
	}
	out := map[string]float64{
		"devices_online": online,
		"sessions_p2p":   p2p,
		"sessions_relay": relay,
		"sessions_nego":  nego,
		"p2p_bps":        p2pBps,
		"relay_bps":      relaySessBps + relayBytes/monitor.Interval.Seconds(),
		"frp_bps":        float64(s.frp.TrafficBps.Load()),
		"frp_active":     float64(s.db.Count(`SELECT COUNT(*) FROM mappings WHERE enabled=1 AND status='online'`)),
		"ws_conns":       online,
	}
	if p2pRate >= 0 {
		out["p2p_success"] = p2pRate
	}
	return out
}

func (s *Server) persistLogs(batch []events.LogEntry) {
	tx, err := s.db.Begin()
	if err != nil {
		return
	}
	for _, e := range batch {
		_, _ = tx.Exec(`INSERT INTO logs (ts, level, source, msg) VALUES (?,?,?,?)`, e.TS.Unix(), e.Level, e.Source, e.Msg)
	}
	_ = tx.Commit()
}

func fileSize(p string) int64 {
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return st.Size()
}
