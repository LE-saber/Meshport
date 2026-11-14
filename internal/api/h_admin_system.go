package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"meshport/internal/auth"
	"meshport/internal/events"
	"meshport/internal/store"
)

// ---------- service status ----------

type serviceStatus struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Status      string `json:"status"` // running | degraded | stopped | external | unknown
	Detail      string `json:"detail"`
	Restartable bool   `json:"restartable"`
}

func (s *Server) services() []serviceStatus {
	out := []serviceStatus{
		{Name: "api", Kind: "core", Status: "running", Detail: fmt.Sprintf("uptime %s", time.Since(s.started).Truncate(time.Second))},
		{Name: "hub", Kind: "core", Status: "running", Detail: fmt.Sprintf("%d device connections", s.hub.ConnCount()), Restartable: true},
	}
	dbStatus := "running"
	if err := s.db.PingContext(context.Background()); err != nil {
		dbStatus = "degraded"
	}
	out = append(out, serviceStatus{Name: "database", Kind: "core", Status: dbStatus, Detail: "SQLite " + s.sqliteVersion()})
	nodes, _ := s.db.Nodes()
	for _, n := range nodes {
		st := serviceStatus{Name: "frps:" + n.Name, Kind: "frps", Status: "unknown", Detail: n.PublicAddr, Restartable: n.Managed}
		switch {
		case !n.Enabled:
			st.Status = "stopped"
		case n.Status == "online":
			st.Status = "running"
			st.Detail = fmt.Sprintf("%s · frps %s", n.PublicAddr, n.Version)
		case n.Status == "offline":
			st.Status = "degraded"
			st.Detail = n.LastError
		}
		if ps, ok := s.frp.ProcStatus(n.ID); ok && !ps.Running {
			st.Status = "degraded"
			st.Detail = "process not running: " + ps.LastErr
		}
		out = append(out, st)
	}
	turn := serviceStatus{Name: "turn", Kind: "external", Status: "external", Detail: "not configured"}
	if s.cfg.TURN.HealthAddr != "" {
		if err := stunProbe(s.cfg.TURN.HealthAddr); err != nil {
			turn.Status, turn.Detail = "degraded", err.Error()
		} else {
			turn.Status, turn.Detail = "running", "STUN binding OK at "+s.cfg.TURN.HealthAddr
		}
	} else if len(s.cfg.TURN.URLs) > 0 {
		turn.Detail = "configured, health probe disabled"
	}
	out = append(out, turn)
	return out
}

// stunProbe sends a STUN Binding Request (RFC 5389) and waits for any response.
func stunProbe(addr string) error {
	conn, err := net.DialTimeout("udp", addr, 2*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	req := make([]byte, 20)
	req[0], req[1] = 0x00, 0x01                             // Binding Request
	req[4], req[5], req[6], req[7] = 0x21, 0x12, 0xA4, 0x42 // magic cookie
	copy(req[8:], []byte(auth.RandomHex(6))[:12])
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write(req); err != nil {
		return err
	}
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil {
		return fmt.Errorf("no STUN response: %w", err)
	}
	if n < 20 || buf[0] != 0x01 || buf[1] != 0x01 {
		return fmt.Errorf("unexpected STUN response")
	}
	return nil
}

func (s *Server) sqliteVersion() string {
	var v string
	_ = s.db.QueryRow(`SELECT sqlite_version()`).Scan(&v)
	return v
}

func (s *Server) hSystemStatus(c *Ctx) error {
	return c.OK(map[string]any{"services": s.services(), "version": Version, "uptime_s": time.Since(s.started).Seconds(), "public_url": s.cfg.PublicURL, "enrollment_secure": s.enrollmentPin != "", "enrollment_url": s.cfg.EnrollmentURL})
}

func (s *Server) hRestartService(c *Ctx) error {
	name := c.Param("name")
	switch {
	case name == "hub":
		n := s.hub.CloseAll("hub restart requested by administrator")
		c.Audit("system.service_restarted", "service", name, map[string]int{"disconnected": n})
		return c.OK(map[string]any{"ok": true, "disconnected": n})
	case strings.HasPrefix(name, "frps:"):
		n, err := s.db.NodeByID(strings.TrimPrefix(name, "frps:"))
		if err != nil {
			return notFoundErr("node")
		}
		if !n.Managed {
			return conflict("frps %s runs on another host; restart it there", n.Name)
		}
		if err := s.frp.Start(s.ctxOr(c), n, s.pluginAddr()); err != nil {
			return errf(500, "start_failed", "%v", err)
		}
		c.Audit("system.service_restarted", "service", name, nil)
		return c.Done()
	}
	return badRequest("service %s cannot be restarted from the console", name)
}

func (s *Server) hVersions(c *Ctx) error {
	comps := []map[string]string{
		{"name": "meshportd", "version": Version},
		{"name": "go", "version": runtime.Version()},
		{"name": "sqlite", "version": s.sqliteVersion()},
		{"name": "schema", "version": fmt.Sprint(s.db.SchemaVersion())},
		{"name": "os/arch", "version": runtime.GOOS + "/" + runtime.GOARCH},
	}
	nodes, _ := s.db.Nodes()
	for _, n := range nodes {
		v := n.Version
		if v == "" {
			v = "unknown"
		}
		comps = append(comps, map[string]string{"name": "frps:" + n.Name, "version": v})
	}
	return c.Items(comps)
}

func (s *Server) hDBStatus(c *Ctx) error {
	tables := []string{"devices", "users", "messages", "files", "comm_sessions", "mappings", "grants", "login_records", "audit_log", "logs", "metrics", "alerts", "failures"}
	counts := map[string]int{}
	for _, t := range tables {
		counts[t] = s.db.Count(`SELECT COUNT(*) FROM ` + t)
	}
	var pages, pageSize, freelist int64
	_ = s.db.QueryRow(`PRAGMA page_count`).Scan(&pages)
	_ = s.db.QueryRow(`PRAGMA page_size`).Scan(&pageSize)
	_ = s.db.QueryRow(`PRAGMA freelist_count`).Scan(&freelist)
	out := map[string]any{
		"engine": "SQLite " + s.sqliteVersion(), "path": s.db.Path, "schema_version": s.db.SchemaVersion(),
		"size_bytes": fileSize(s.db.Path), "wal_bytes": fileSize(s.db.Path + "-wal"), "pages": pages, "page_size": pageSize, "free_pages": freelist,
		"rows": counts,
	}
	if c.Query("check") == "1" {
		var res string
		_ = s.db.QueryRow(`PRAGMA quick_check`).Scan(&res)
		out["integrity"] = res
	}
	return c.OK(out)
}

// ---------- backups ----------

func (s *Server) backupDir() string { return filepath.Join(s.cfg.DataDir, "backups") }

// Backup writes a consistent snapshot of the database with VACUUM INTO.
func (s *Server) Backup(kind string) (string, error) {
	s.backupMu.Lock()
	defer s.backupMu.Unlock()
	if err := os.MkdirAll(s.backupDir(), 0o700); err != nil {
		return "", err
	}
	id := store.NewID("bak")
	name := "meshport-" + time.Now().UTC().Format("20060102-150405") + "-" + id[len(id)-4:] + ".db"
	path := filepath.Join(s.backupDir(), name)
	_, _ = s.db.Exec(`INSERT INTO backups (id, file, kind, status, created_at) VALUES (?,?,?, 'running', ?)`, id, name, kind, store.Now())
	s.bus.Publish("backup.started", map[string]any{"id": id, "kind": kind})
	if _, err := s.db.Exec(`VACUUM INTO ?`, path); err != nil {
		_, _ = s.db.Exec(`UPDATE backups SET status='failed', error=?, finished_at=? WHERE id=?`, err.Error(), store.Now(), id)
		s.raiseAlert("crit", "system", "Backup failed", err.Error(), "backup", "backup-failed")
		s.bus.Publish("backup.failed", map[string]any{"id": id, "error": err.Error()})
		return id, err
	}
	// keep the config next to the snapshot so a restore is self-contained
	if p := s.cfg.Path(); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			_ = os.WriteFile(path+".yaml", b, 0o600)
		}
	}
	sum, _ := fileSHA256(path)
	_, _ = s.db.Exec(`UPDATE backups SET status='ok', size=?, sha256=?, finished_at=? WHERE id=?`, fileSize(path), sum, store.Now(), id)
	s.resolveAlerts("backup-failed")
	s.bus.Publish("backup.completed", map[string]any{"id": id, "file": name, "size": fileSize(path)})
	s.log.Info("backup completed", "file", name, "kind", kind, "src", "backup")
	s.pruneBackups()
	return id, nil
}

func (s *Server) pruneBackups() {
	keep := s.db.Settings().BackupKeep
	rows, err := s.db.Query(`SELECT id, file FROM backups WHERE status='ok' ORDER BY created_at DESC LIMIT -1 OFFSET ?`, keep)
	if err != nil {
		return
	}
	type b struct{ id, file string }
	var old []b
	for rows.Next() {
		var x b
		if rows.Scan(&x.id, &x.file) == nil {
			old = append(old, x)
		}
	}
	rows.Close()
	for _, x := range old {
		_ = os.Remove(filepath.Join(s.backupDir(), x.file))
		_ = os.Remove(filepath.Join(s.backupDir(), x.file+".yaml"))
		_, _ = s.db.Exec(`DELETE FROM backups WHERE id=?`, x.id)
	}
}

func (s *Server) hBackups(c *Ctx) error {
	rows, err := s.db.Query(`SELECT id, file, size, sha256, kind, status, error, created_at, finished_at FROM backups ORDER BY created_at DESC LIMIT 100`)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, file, sum, kind, status, errStr string
		var size int64
		var created, finished store.Time
		if err := rows.Scan(&id, &file, &size, &sum, &kind, &status, &errStr, &created, &finished); err != nil {
			return err
		}
		out = append(out, map[string]any{"id": id, "file": file, "size": size, "sha256": sum, "kind": kind, "status": status, "error": errStr, "created_at": created, "finished_at": finished})
	}
	st := s.db.Settings()
	return c.OK(map[string]any{"items": out, "schedule": map[string]int{"hour_utc": st.BackupHour, "keep": st.BackupKeep}, "dir": s.backupDir()})
}

func (s *Server) hCreateBackup(c *Ctx) error {
	id, err := s.Backup("manual")
	c.Audit("system.backup", "backup", id, nil)
	if err != nil {
		return errf(500, "backup_failed", "%v", err)
	}
	return c.JSON(201, map[string]string{"id": id})
}

func (s *Server) hDownloadBackup(c *Ctx) error {
	var file string
	if err := s.db.QueryRow(`SELECT file FROM backups WHERE id=? AND status='ok'`, c.Param("id")).Scan(&file); err != nil {
		return notFoundErr("backup")
	}
	c.Audit("system.backup_downloaded", "backup", c.Param("id"), nil)
	c.W.Header().Set("Content-Disposition", `attachment; filename="`+file+`"`)
	http.ServeFile(c.W, c.R, filepath.Join(s.backupDir(), file))
	return nil
}

func (s *Server) hDeleteBackup(c *Ctx) error {
	var file string
	if err := s.db.QueryRow(`SELECT file FROM backups WHERE id=?`, c.Param("id")).Scan(&file); err != nil {
		return notFoundErr("backup")
	}
	_ = os.Remove(filepath.Join(s.backupDir(), file))
	_ = os.Remove(filepath.Join(s.backupDir(), file+".yaml"))
	_, _ = s.db.Exec(`DELETE FROM backups WHERE id=?`, c.Param("id"))
	c.Audit("system.backup_deleted", "backup", c.Param("id"), nil)
	return c.Done()
}

// ---------- webhooks ----------

func (s *Server) hWebhooks(c *Ctx) error {
	rows, err := s.db.Query(`SELECT id, name, url, events, enabled, created_at, last_status, last_error, last_delivery_at, fail_count FROM webhooks ORDER BY created_at`)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, name, u, evs, lastErr string
		var enabled bool
		var created, last store.Time
		var status, fails int
		if err := rows.Scan(&id, &name, &u, &evs, &enabled, &created, &status, &lastErr, &last, &fails); err != nil {
			return err
		}
		var ev []string
		_ = json.Unmarshal([]byte(evs), &ev)
		out = append(out, map[string]any{"id": id, "name": name, "url": u, "events": ev, "enabled": enabled, "created_at": created, "last_status": status,
			"last_error": lastErr, "last_delivery_at": last, "fail_count": fails})
	}
	return c.OK(map[string]any{"items": out, "event_types": webhookEventTypes})
}

var webhookEventTypes = []string{"device.*", "device.registered", "device.online", "device.offline", "device.approved", "device.banned", "grant.*", "grant.requested",
	"grant.approved", "grant.rejected", "grant.revoked", "grant.expired", "frp.*", "frp.mapping.error", "frp.node.offline", "alert.*", "alert.raised",
	"security.abnormal_login", "user.mfa_reminder", "backup.*", "system.*"}

type webhookReq struct {
	Name    *string   `json:"name"`
	URL     *string   `json:"url"`
	Events  *[]string `json:"events"`
	Enabled *bool     `json:"enabled"`
}

func validWebhookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("url must be an absolute http(s) URL")
	}
	return nil
}

func (s *Server) hCreateWebhook(c *Ctx) error {
	var req webhookReq
	if err := c.Bind(&req); err != nil {
		return err
	}
	if req.URL == nil {
		return badRequest("url is required")
	}
	if err := validWebhookURL(*req.URL); err != nil {
		return badRequest("%v", err)
	}
	evs := []string{"*"}
	if req.Events != nil && len(*req.Events) > 0 {
		evs = *req.Events
	}
	name := ""
	if req.Name != nil {
		name = trimTo(*req.Name, 64)
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	id := store.NewID("whk")
	secret := "whsec_" + auth.RandomHex(24)
	evb, _ := json.Marshal(evs)
	if _, err := s.db.Exec(`INSERT INTO webhooks (id, name, url, events, secret, enabled, created_at) VALUES (?,?,?,?,?,?,?)`, id, name, *req.URL, string(evb), secret, enabled, store.Now()); err != nil {
		return err
	}
	s.hooksGen.Add(1)
	c.Audit("system.webhook.created", "webhook", id, map[string]any{"url": *req.URL, "events": evs})
	return c.JSON(201, map[string]any{"id": id, "secret": secret,
		"verify": "X-Meshport-Signature: t=<unix>,v1=hex(HMAC-SHA256(secret, \"<t>.\" + body))"})
}

func (s *Server) hUpdateWebhook(c *Ctx) error {
	id := c.Param("id")
	if s.db.Count(`SELECT COUNT(*) FROM webhooks WHERE id=?`, id) == 0 {
		return notFoundErr("webhook")
	}
	var req webhookReq
	if err := c.Bind(&req); err != nil {
		return err
	}
	if req.URL != nil {
		if err := validWebhookURL(*req.URL); err != nil {
			return badRequest("%v", err)
		}
		_, _ = s.db.Exec(`UPDATE webhooks SET url=? WHERE id=?`, *req.URL, id)
	}
	if req.Name != nil {
		_, _ = s.db.Exec(`UPDATE webhooks SET name=? WHERE id=?`, trimTo(*req.Name, 64), id)
	}
	if req.Events != nil {
		evb, _ := json.Marshal(*req.Events)
		_, _ = s.db.Exec(`UPDATE webhooks SET events=? WHERE id=?`, string(evb), id)
	}
	if req.Enabled != nil {
		_, _ = s.db.Exec(`UPDATE webhooks SET enabled=?, fail_count=CASE WHEN ? THEN 0 ELSE fail_count END WHERE id=?`, *req.Enabled, *req.Enabled, id)
	}
	s.hooksGen.Add(1)
	c.Audit("system.webhook.updated", "webhook", id, req)
	return c.Done()
}

func (s *Server) hDeleteWebhook(c *Ctx) error {
	res, err := s.db.Exec(`DELETE FROM webhooks WHERE id=?`, c.Param("id"))
	if err != nil {
		return err
	}
	s.hooksGen.Add(1)
	if n, _ := res.RowsAffected(); n == 0 {
		return notFoundErr("webhook")
	}
	c.Audit("system.webhook.deleted", "webhook", c.Param("id"), nil)
	return c.Done()
}

func (s *Server) hTestWebhook(c *Ctx) error {
	var u, secret string
	if err := s.db.QueryRow(`SELECT url, secret FROM webhooks WHERE id=?`, c.Param("id")).Scan(&u, &secret); err != nil {
		return notFoundErr("webhook")
	}
	status, err := s.deliver(c.Param("id"), u, secret, events.Event{Type: "webhook.test", Time: time.Now().UTC(), Data: map[string]string{"message": "Meshport webhook test"}})
	if err != nil {
		return c.OK(map[string]any{"ok": false, "status": status, "error": err.Error()})
	}
	return c.OK(map[string]any{"ok": true, "status": status})
}

// deliver POSTs one signed event; it records the outcome on the webhook row.
func (s *Server) deliver(id, u, secret string, ev events.Event) (int, error) {
	body, _ := json.Marshal(map[string]any{"id": store.NewID("evt"), "type": ev.Type, "time": ev.Time, "data": ev.Data})
	req, err := http.NewRequest("POST", u, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "meshportd/"+Version)
	req.Header.Set("X-Meshport-Event", ev.Type)
	req.Header.Set("X-Meshport-Signature", auth.SignWebhook(secret, body, time.Now()))
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	status := 0
	if err == nil {
		status = resp.StatusCode
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		if status >= 300 {
			err = fmt.Errorf("HTTP %d", status)
		}
	}
	if err != nil {
		_, _ = s.db.Exec(`UPDATE webhooks SET last_status=?, last_error=?, last_delivery_at=?, fail_count=fail_count+1 WHERE id=?`, status, err.Error(), store.Now(), id)
	} else {
		_, _ = s.db.Exec(`UPDATE webhooks SET last_status=?, last_error='', last_delivery_at=?, fail_count=0 WHERE id=?`, status, store.Now(), id)
	}
	return status, err
}

// ---------- updates ----------

type manifest struct {
	Version   string `json:"version"`
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
	Signature string `json:"signature"` // base64 ed25519 over "<version>:<sha256>"
	Notes     string `json:"notes"`
}

func (s *Server) fetchManifest(ctx context.Context) (*manifest, error) {
	if s.cfg.Update.ManifestURL == "" {
		return nil, fmt.Errorf("update.manifest_url is not configured")
	}
	u, err := url.Parse(s.cfg.Update.ManifestURL)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("channel", s.db.Settings().UpdateChannel)
	q.Set("current", Version)
	q.Set("arch", runtime.GOOS+"-"+runtime.GOARCH)
	u.RawQuery = q.Encode()
	req, _ := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("manifest: HTTP %d", resp.StatusCode)
	}
	var m manifest
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (s *Server) hUpdateCheck(c *Ctx) error {
	out := map[string]any{"current": Version, "channel": s.db.Settings().UpdateChannel, "auto_update": s.db.Settings().AutoUpdate}
	m, err := s.fetchManifest(c.R.Context())
	if err != nil {
		out["available"] = false
		out["error"] = err.Error()
		return c.OK(out)
	}
	_ = s.db.SetKV("update_checked_at", time.Now().UTC().Format(time.RFC3339))
	out["latest"] = m.Version
	out["notes"] = m.Notes
	out["available"] = m.Version != "" && m.Version != Version
	return c.OK(out)
}

func (s *Server) hUpdateInstall(c *Ctx) error {
	if !s.updateMu.TryLock() {
		return conflict("an update is already running")
	}
	defer s.updateMu.Unlock()
	m, err := s.fetchManifest(c.R.Context())
	if err != nil {
		return errf(502, "manifest_unavailable", "%v", err)
	}
	if m.Version == Version {
		return conflict("already on %s", Version)
	}
	if s.cfg.Update.PublicKey == "" {
		return errf(412, "unsigned_updates_disabled", "set update.public_key to install updates from the console")
	}
	pk, err := base64.StdEncoding.DecodeString(s.cfg.Update.PublicKey)
	if err != nil || len(pk) != ed25519.PublicKeySize {
		return errf(500, "bad_public_key", "update.public_key is not a base64 ed25519 key")
	}
	sig, err := base64.StdEncoding.DecodeString(m.Signature)
	if err != nil || !ed25519.Verify(pk, []byte(m.Version+":"+strings.ToLower(m.SHA256)), sig) {
		return errf(422, "bad_signature", "manifest signature verification failed")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	tmp := exe + ".new"
	req, _ := http.NewRequestWithContext(c.R.Context(), "GET", m.URL, nil)
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return errf(502, "download_failed", "%v", err)
	}
	defer resp.Body.Close()
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return errf(500, "write_failed", "%v (is the binary directory writable by the service user?)", err)
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, 512<<20))
	f.Close()
	if err != nil || !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), m.SHA256) {
		os.Remove(tmp)
		return errf(422, "checksum_mismatch", "downloaded binary does not match the signed checksum")
	}
	if _, err := s.Backup("pre-update"); err != nil {
		os.Remove(tmp)
		return errf(500, "backup_failed", "pre-update backup failed: %v", err)
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Remove(tmp)
		return errf(500, "replace_failed", "%v", err)
	}
	c.Audit("system.update_installed", "system", "meshportd", map[string]string{"from": Version, "to": m.Version})
	s.log.Warn("update installed; exiting so systemd restarts the new version", "to", m.Version)
	go func() {
		time.Sleep(time.Second)
		os.Exit(0) // Restart=always in the systemd unit brings up the new binary
	}()
	return c.OK(map[string]string{"installed": m.Version, "restart": "in 1 second"})
}

// ---------- settings ----------

func (s *Server) hSettings(c *Ctx) error {
	return c.OK(map[string]any{"settings": s.db.Settings(), "defaults": store.DefaultSettings()})
}

func (s *Server) hUpdateSettings(c *Ctx) error {
	st := s.db.Settings()
	raw, err := io.ReadAll(io.LimitReader(c.R.Body, 64<<10))
	if err != nil {
		return err
	}
	// merge: only provided keys change
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&st); err != nil {
		return badRequest("invalid settings: %v", err)
	}
	if st.MFARequired && !s.db.Settings().MFARequired && c.P.User != nil && !c.P.User.MFAEnabled {
		return conflict("enable MFA on your own account before requiring it")
	}
	if err := s.db.SaveSettings(st); err != nil {
		return badRequest("%v", err)
	}
	var changed map[string]any
	_ = json.Unmarshal(raw, &changed)
	c.Audit("system.settings_updated", "settings", "", changed)
	s.bus.Publish("system.settings_updated", changed)
	return c.OK(st)
}
