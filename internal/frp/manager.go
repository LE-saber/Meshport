package frp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"meshport/internal/auth"
	"meshport/internal/config"
	"meshport/internal/events"
	"meshport/internal/policy"
	"meshport/internal/store"
)

// Notifier pushes "your frpc config changed" to devices (implemented by the hub).
type Notifier interface {
	Send(deviceID, typ string, payload any) bool
}

// IPDenied reports whether an address is on the deny list (implemented by the API layer).
type IPDenied func(ip string) bool

type Manager struct {
	cfg    *config.Config
	base   *slog.Logger
	db     *store.DB
	bus    *events.Bus
	log    *slog.Logger
	notify Notifier
	denied IPDenied

	kickMu sync.Mutex
	kicks  map[string]bool // nodeID|deviceID -> reject next ping

	procMu sync.Mutex
	procs  map[string]*proc

	statMu        sync.Mutex
	pendingMu     sync.Mutex
	pendingLogins map[string]loginContent
	nodeStatsMu   sync.RWMutex
	nodeStats     map[string]NodeStats
	lastSeen      map[string]trafficSample // mapping id -> last cumulative traffic

	// TrafficBps is the aggregate FRP throughput (in+out) across nodes.
	TrafficBps atomic.Int64
	httpc      *http.Client
}

type trafficSample struct {
	in, out int64
	at      time.Time
}

func New(cfg *config.Config, db *store.DB, bus *events.Bus, log *slog.Logger, n Notifier, denied IPDenied) *Manager {
	return &Manager{cfg: cfg, base: log, db: db, bus: bus, log: log.With("src", "frp"), notify: n, denied: denied,
		kicks: map[string]bool{}, pendingLogins: map[string]loginContent{}, nodeStats: map[string]NodeStats{}, procs: map[string]*proc{}, lastSeen: map[string]trafficSample{},
		httpc: &http.Client{Timeout: 4 * time.Second}}
}

// NotifyDevice tells a device to re-fetch its frpc configuration.
func (m *Manager) NotifyDevice(deviceID string) {
	if m.notify != nil {
		m.notify.Send(deviceID, "frp.config", map[string]int64{"version": time.Now().UnixNano()})
	}
}

// Kick makes frps drop a device's control connection on its next heartbeat;
// frpc reconnects and re-registers only the proxies the plugin still allows.
func (m *Manager) Kick(nodeID, deviceID string) {
	m.kickMu.Lock()
	m.kicks[nodeID+"|"+deviceID] = true
	m.kickMu.Unlock()
}

func (m *Manager) takeKick(nodeID, deviceID string) bool {
	m.kickMu.Lock()
	defer m.kickMu.Unlock()
	k := nodeID + "|" + deviceID
	if m.kicks[k] {
		delete(m.kicks, k)
		return true
	}
	return false
}

// SetEnabled starts or stops a mapping and enforces it on frps.
func (m *Manager) SetEnabled(mp *store.Mapping, on bool) error {
	status := "stopped"
	if on {
		status = "pending"
	}
	_, err := m.db.Exec(`UPDATE mappings SET enabled=?, status=?, updated_at=?, conns=CASE WHEN ? THEN conns ELSE 0 END, rate_in=0, rate_out=0 WHERE id=?`, on, status, store.Now(), on, mp.ID)
	if err != nil {
		return err
	}
	m.NotifyDevice(mp.DeviceID)
	if !on {
		m.Kick(mp.NodeID, mp.DeviceID)
	}
	m.bus.Publish("frp.mapping."+map[bool]string{true: "started", false: "stopped"}[on], map[string]any{"id": mp.ID, "name": mp.Name, "device_id": mp.DeviceID})
	return nil
}

// StopAll stops every mapping (optionally on one node). Returns how many were stopped.
func (m *Manager) StopAll(nodeID string) (int, error) {
	where := `m.enabled=1`
	args := []any{}
	if nodeID != "" {
		where += ` AND m.node_id=?`
		args = append(args, nodeID)
	}
	list, err := m.db.Mappings(where, args...)
	if err != nil {
		return 0, err
	}
	for _, mp := range list {
		if err := m.SetEnabled(mp, false); err != nil {
			return 0, err
		}
	}
	return len(list), nil
}

// ---------- server plugin ----------

type pluginRequest struct {
	Version string          `json:"version"`
	Op      string          `json:"op"`
	Content json.RawMessage `json:"content"`
}

type pluginResponse struct {
	Reject       bool   `json:"reject"`
	RejectReason string `json:"reject_reason,omitempty"`
	Unchange     bool   `json:"unchange"`
}

type userInfo struct {
	User  string            `json:"user"`
	Metas map[string]string `json:"metas"`
	RunID string            `json:"run_id"`
}

type loginContent struct {
	Version       string            `json:"version"`
	Hostname      string            `json:"hostname"`
	OS            string            `json:"os"`
	Arch          string            `json:"arch"`
	User          string            `json:"user"`
	RunID         string            `json:"run_id"`
	Metas         map[string]string `json:"metas"`
	ClientAddress string            `json:"client_address"`
}

type newProxyContent struct {
	User          userInfo `json:"user"`
	ProxyName     string   `json:"proxy_name"`
	ProxyType     string   `json:"proxy_type"`
	RemotePort    int      `json:"remote_port"`
	CustomDomains []string `json:"custom_domains"`
	SubDomain     string   `json:"subdomain"`
	Sk            string   `json:"sk"`
}

type closeProxyContent struct {
	User      userInfo `json:"user"`
	ProxyName string   `json:"proxy_name"`
}

type pingContent struct {
	User userInfo `json:"user"`
}

type newUserConnContent struct {
	User       userInfo `json:"user"`
	ProxyName  string   `json:"proxy_name"`
	ProxyType  string   `json:"proxy_type"`
	RemoteAddr string   `json:"remote_addr"`
}

func allow() pluginResponse { return pluginResponse{Unchange: true} }
func reject(reason string) pluginResponse {
	return pluginResponse{Reject: true, RejectReason: reason, Unchange: true}
}

// PluginHandler serves POST /frps-plugin/{node}/{token}.
func (m *Manager) PluginHandler(w http.ResponseWriter, r *http.Request) {
	node, err := m.db.NodeByID(r.PathValue("node"))
	if err != nil || !auth.ConstantEq(node.PluginToken, r.PathValue("token")) {
		http.Error(w, "unknown node", http.StatusForbidden)
		return
	}
	var req pluginRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	op := req.Op
	if op == "" {
		op = r.URL.Query().Get("op")
	}
	resp := m.handleOp(node, op, req.Content)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// deviceFor authenticates frpc by the metadatas meshportd put in its config.
func (m *Manager) deviceFor(u userInfo) (*store.Device, string) {
	id := u.Metas["device_id"]
	if id == "" || id != u.User {
		return nil, "missing meshport device metadata"
	}
	d, err := m.db.DeviceByID(id)
	if err != nil {
		return nil, "unknown device"
	}
	if d.FRPSecret == "" || !auth.ConstantEq(d.FRPSecret, u.Metas["frp_secret"]) {
		return nil, "invalid device frp credential"
	}
	if !policy.CanCreateMapping(d) {
		return nil, "device is not allowed to publish services (status " + d.Status + ", level " + d.Level + ")"
	}
	return d, ""
}

func (m *Manager) handleOp(node *store.Node, op string, raw json.RawMessage) pluginResponse {
	now := store.Now()
	switch op {
	case "Login":
		var c loginContent
		if err := json.Unmarshal(raw, &c); err != nil {
			return reject("bad login content")
		}
		ip, _, _ := net.SplitHostPort(c.ClientAddress)
		if m.denied != nil && m.denied(ip) {
			m.loginRecord(c.User, "", ip, false, "ip denied")
			return reject("address denied by policy")
		}
		if !node.Enabled {
			return reject("node disabled")
		}
		d, why := m.deviceFor(userInfo{User: c.User, Metas: c.Metas})
		if d == nil {
			m.loginRecord(c.User, "", ip, false, why)
			m.failure(c.User, why, "frpc login on "+node.Name+" from "+c.ClientAddress)
			return reject(why)
		}
		m.loginRecord(d.ID, d.Name, ip, true, "frpc "+c.Version)
		m.log.Info("frpc login", "node", node.Name, "device", d.Name, "addr", c.ClientAddress, "version", c.Version)
		m.bus.Publish("frp.client.login", map[string]any{"node": node.ID, "device_id": d.ID, "addr": c.ClientAddress})
		if c.RunID != "" {
			m.upsertSession(c.RunID, node.ID, d.ID, c, now)
		} else {
			m.pendingLogin(node.ID, d.ID, c, now)
		}
		return allow()

	case "NewProxy":
		var c newProxyContent
		if err := json.Unmarshal(raw, &c); err != nil {
			return reject("bad proxy content")
		}
		d, why := m.deviceFor(c.User)
		if d == nil {
			return reject(why)
		}
		m.touchSession(c.User.RunID, node.ID, d.ID, now)
		name := strings.TrimPrefix(c.ProxyName, c.User.User+".")
		var mp *store.Mapping
		list, err := m.db.Mappings(`m.device_id=? AND m.name=?`, d.ID, name)
		if err == nil && len(list) == 1 {
			mp = list[0]
		}
		if mp == nil {
			m.failure(d.ID, "unknown proxy "+name, "node "+node.Name)
			return reject("proxy " + name + " is not registered in meshport; create the mapping first")
		}
		fail := func(reason string) pluginResponse {
			_, _ = m.db.Exec(`UPDATE mappings SET status='error', last_error=?, updated_at=? WHERE id=?`, reason, now, mp.ID)
			m.failure(d.ID, reason, "mapping "+mp.Name+" on "+node.Name)
			m.bus.Publish("frp.mapping.error", map[string]any{"id": mp.ID, "name": mp.Name, "reason": reason})
			return reject(reason)
		}
		switch {
		case mp.NodeID != node.ID:
			return fail("mapping belongs to node " + mp.NodeName)
		case !mp.Enabled:
			return reject("mapping is stopped by the administrator")
		case mp.Proto != c.ProxyType:
			return fail(fmt.Sprintf("type %s does not match registered %s", c.ProxyType, mp.Proto))
		case (mp.Proto == "tcp" || mp.Proto == "udp") && c.RemotePort != mp.RemotePort:
			return fail(fmt.Sprintf("remote port %d does not match registered %d", c.RemotePort, mp.RemotePort))
		case (mp.Proto == "http" || mp.Proto == "https") && (!sameSet(c.CustomDomains, mp.Domains.V) || !strings.EqualFold(c.SubDomain, mp.Subdomain)):
			return fail("domains do not match the registered mapping")
		case (mp.Proto == "stcp" || mp.Proto == "xtcp") && !auth.ConstantEq(c.Sk, mp.SecretKey):
			return fail("secret key does not match the registered mapping")
		}
		_, _ = m.db.Exec(`UPDATE mappings SET status='online', last_error='', updated_at=? WHERE id=?`, now, mp.ID)
		m.log.Info("proxy online", "node", node.Name, "device", d.Name, "proxy", mp.Name, "type", mp.Proto)
		m.bus.Publish("frp.mapping.online", map[string]any{"id": mp.ID, "name": mp.Name, "node": node.ID})
		return allow()

	case "CloseProxy":
		var c closeProxyContent
		if err := json.Unmarshal(raw, &c); err == nil {
			name := strings.TrimPrefix(c.ProxyName, c.User.User+".")
			_, _ = m.db.Exec(`UPDATE mappings SET status=CASE WHEN enabled=1 THEN 'offline' ELSE 'stopped' END, conns=0, rate_in=0, rate_out=0, updated_at=? WHERE device_id=? AND name=? AND node_id=? AND status!='error'`, now, c.User.User, name, node.ID)
			m.bus.Publish("frp.mapping.offline", map[string]any{"device_id": c.User.User, "name": name, "node": node.ID})
		}
		return allow()

	case "Ping":
		var c pingContent
		if err := json.Unmarshal(raw, &c); err != nil {
			return reject("bad ping")
		}
		d, why := m.deviceFor(c.User)
		if d == nil {
			_, _ = m.db.Exec(`DELETE FROM frpc_sessions WHERE run_id=?`, c.User.RunID)
			return reject(why)
		}
		if m.takeKick(node.ID, d.ID) {
			m.log.Info("kicking frpc so stopped proxies are released", "node", node.Name, "device", d.Name)
			_, _ = m.db.Exec(`DELETE FROM frpc_sessions WHERE run_id=?`, c.User.RunID)
			return reject("configuration changed; reconnect")
		}
		m.touchSession(c.User.RunID, node.ID, d.ID, now)
		return allow()

	case "NewUserConn":
		var c newUserConnContent
		if err := json.Unmarshal(raw, &c); err == nil && m.denied != nil {
			ip, _, _ := net.SplitHostPort(c.RemoteAddr)
			if m.denied(ip) {
				return reject("visitor address denied by policy")
			}
		}
		return allow()
	}
	return allow()
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, x := range a {
		seen[strings.ToLower(x)]++
	}
	for _, x := range b {
		seen[strings.ToLower(x)]--
	}
	for _, v := range seen {
		if v != 0 {
			return false
		}
	}
	return true
}

// Logins with an empty run id receive one from frps after the plugin call;
// remember them until the first ping/proxy reveals it.

func (m *Manager) pendingLogin(nodeID, deviceID string, c loginContent, now store.Time) {
	m.pendingMu.Lock()
	m.pendingLogins[nodeID+"|"+deviceID] = c
	m.pendingMu.Unlock()
}

func (m *Manager) upsertSession(runID, nodeID, deviceID string, c loginContent, now store.Time) {
	_, _ = m.db.Exec(`INSERT INTO frpc_sessions (run_id, node_id, device_id, version, client_addr, hostname, os, arch, login_at, last_ping_at) VALUES (?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(run_id) DO UPDATE SET last_ping_at=excluded.last_ping_at, client_addr=excluded.client_addr, version=excluded.version`,
		runID, nodeID, deviceID, c.Version, c.ClientAddress, c.Hostname, c.OS, c.Arch, now, now)
}

func (m *Manager) touchSession(runID, nodeID, deviceID string, now store.Time) {
	if runID == "" {
		return
	}
	res, err := m.db.Exec(`UPDATE frpc_sessions SET last_ping_at=? WHERE run_id=?`, now, runID)
	if err != nil {
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		m.pendingMu.Lock()
		c := m.pendingLogins[nodeID+"|"+deviceID]
		delete(m.pendingLogins, nodeID+"|"+deviceID)
		m.pendingMu.Unlock()
		m.upsertSession(runID, nodeID, deviceID, c, now)
	}
}

func (m *Manager) loginRecord(subjectID, subject, ip string, ok bool, reason string) {
	_, _ = m.db.Exec(`INSERT INTO login_records (kind, subject_id, subject, ip, success, method, reason, created_at) VALUES ('frpc',?,?,?,?, 'frp token', ?, ?)`,
		subjectID, subject, ip, ok, reason, store.Now())
}

func (m *Manager) failure(deviceID, reason, detail string) {
	_, _ = m.db.Exec(`INSERT INTO failures (kind, a_id, reason, detail, created_at) VALUES ('frp',?,?,?,?)`, deviceID, reason, detail, store.Now())
	m.bus.Publish("failure", map[string]any{"kind": "frp", "a": deviceID, "reason": reason})
}

// ---------- dashboard polling ----------

type serverInfo struct {
	Version         string `json:"version"`
	TotalTrafficIn  int64  `json:"totalTrafficIn"`
	TotalTrafficOut int64  `json:"totalTrafficOut"`
	CurConns        int64  `json:"curConns"`
	ClientCounts    int64  `json:"clientCounts"`
}

type proxyStats struct {
	Name            string `json:"name"`
	User            string `json:"user"`
	TodayTrafficIn  int64  `json:"todayTrafficIn"`
	TodayTrafficOut int64  `json:"todayTrafficOut"`
	CurConns        int64  `json:"curConns"`
	Status          string `json:"status"`
}

// NodeStats is the live view of one frps node.
type NodeStats struct {
	NodeID     string `json:"node_id"`
	Reachable  bool   `json:"reachable"`
	Version    string `json:"version"`
	Clients    int64  `json:"clients"`
	Conns      int64  `json:"conns"`
	TrafficIn  int64  `json:"traffic_in_today"`
	TrafficOut int64  `json:"traffic_out_today"`
	RateBps    int64  `json:"rate_bps"`
	Error      string `json:"error,omitempty"`
}

func (m *Manager) NodeStats(id string) (NodeStats, bool) {
	m.nodeStatsMu.RLock()
	defer m.nodeStatsMu.RUnlock()
	s, ok := m.nodeStats[id]
	return s, ok
}

func (m *Manager) dashGet(n *store.Node, path string, out any) error {
	req, _ := http.NewRequest("GET", "http://"+n.DashboardAddr+path, nil)
	req.SetBasicAuth(n.DashboardUser, n.DashboardPass)
	resp, err := m.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("dashboard %s: HTTP %d", path, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
}

// RunPoller polls every node's dashboard until ctx ends.
func (m *Manager) RunPoller(ctx context.Context) {
	// give managed frps processes a moment to start before the first poll
	select {
	case <-ctx.Done():
		return
	case <-time.After(3 * time.Second):
	}
	t := time.NewTicker(m.cfg.FRP.PollInterval)
	defer t.Stop()
	m.pollAll()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.pollAll()
		}
	}
}

func (m *Manager) pollAll() {
	nodes, err := m.db.Nodes()
	if err != nil {
		return
	}
	var total int64
	for _, n := range nodes {
		if !n.Enabled || n.DashboardAddr == "" {
			continue
		}
		total += m.pollNode(n)
	}
	m.TrafficBps.Store(total)
	// forget frpc sessions that stopped pinging
	_, _ = m.db.Exec(`DELETE FROM frpc_sessions WHERE last_ping_at < ?`, time.Now().Add(-3*time.Minute).Unix())
}

func (m *Manager) pollNode(n *store.Node) int64 {
	now := time.Now()
	st := NodeStats{NodeID: n.ID}
	var si serverInfo
	if err := m.dashGet(n, "/api/serverinfo", &si); err != nil {
		st.Error = err.Error()
		if n.Status != "offline" {
			m.log.Warn("frps node unreachable", "node", n.Name, "err", err)
			m.bus.Publish("frp.node.offline", map[string]any{"id": n.ID, "name": n.Name, "error": err.Error()})
		}
		_, _ = m.db.Exec(`UPDATE frps_nodes SET status='offline', last_error=? WHERE id=?`, err.Error(), n.ID)
		m.nodeStatsMu.Lock()
		m.nodeStats[n.ID] = st
		m.nodeStatsMu.Unlock()
		return 0
	}
	if n.Status != "online" {
		m.bus.Publish("frp.node.online", map[string]any{"id": n.ID, "name": n.Name})
	}
	_, _ = m.db.Exec(`UPDATE frps_nodes SET status='online', version=?, last_error='', last_seen_at=? WHERE id=?`, si.Version, store.Now(), n.ID)
	st.Reachable, st.Version, st.Clients, st.Conns, st.TrafficIn, st.TrafficOut = true, si.Version, si.ClientCounts, si.CurConns, si.TotalTrafficIn, si.TotalTrafficOut

	var rate int64
	for _, typ := range []string{"tcp", "udp", "http", "https", "stcp", "xtcp"} {
		var resp struct {
			Proxies []proxyStats `json:"proxies"`
		}
		if err := m.dashGet(n, "/api/proxy/"+typ, &resp); err != nil {
			continue
		}
		for _, p := range resp.Proxies {
			user := p.User
			name := p.Name
			if user == "" {
				user, name, _ = strings.Cut(p.Name, ".")
			} else {
				name = strings.TrimPrefix(p.Name, user+".")
			}
			var id string
			if err := m.db.QueryRow(`SELECT id FROM mappings WHERE device_id=? AND name=? AND node_id=?`, user, name, n.ID).Scan(&id); err != nil {
				continue
			}
			m.statMu.Lock()
			prev, ok := m.lastSeen[id]
			m.lastSeen[id] = trafficSample{p.TodayTrafficIn, p.TodayTrafficOut, now}
			m.statMu.Unlock()
			var rin, rout int64
			if ok && p.TodayTrafficIn >= prev.in && p.TodayTrafficOut >= prev.out {
				dt := now.Sub(prev.at).Seconds()
				if dt > 0 {
					rin = int64(float64(p.TodayTrafficIn-prev.in) / dt)
					rout = int64(float64(p.TodayTrafficOut-prev.out) / dt)
				}
			}
			rate += rin + rout
			status := "offline"
			if p.Status == "online" {
				status = "online"
			}
			_, _ = m.db.Exec(`UPDATE mappings SET conns=?, traffic_in=?, traffic_out=?, rate_in=?, rate_out=?,
				status=CASE WHEN enabled=0 THEN 'stopped' WHEN status='error' AND ?='offline' THEN 'error' ELSE ? END WHERE id=?`,
				p.CurConns, p.TodayTrafficIn, p.TodayTrafficOut, rin, rout, status, status, id)
		}
	}
	st.RateBps = rate
	m.nodeStatsMu.Lock()
	m.nodeStats[n.ID] = st
	m.nodeStatsMu.Unlock()
	return rate
}

// ---------- supervised local frps ----------

type proc struct {
	cancel   context.CancelFunc
	done     chan struct{}
	pid      atomic.Int64
	started  time.Time
	restarts int
	lastErr  string
}

// ProcStatus reports a managed frps process.
type ProcStatus struct {
	Running  bool      `json:"running"`
	PID      int64     `json:"pid"`
	Started  time.Time `json:"started"`
	Restarts int       `json:"restarts"`
	LastErr  string    `json:"last_error,omitempty"`
}

func (m *Manager) ProcStatus(nodeID string) (ProcStatus, bool) {
	m.procMu.Lock()
	defer m.procMu.Unlock()
	p := m.procs[nodeID]
	if p == nil {
		return ProcStatus{}, false
	}
	return ProcStatus{Running: p.pid.Load() > 0, PID: p.pid.Load(), Started: p.started, Restarts: p.restarts, LastErr: p.lastErr}, true
}

// StartManaged launches every enabled managed node.
func (m *Manager) StartManaged(ctx context.Context, pluginAddr string) {
	nodes, err := m.db.Nodes()
	if err != nil {
		return
	}
	for _, n := range nodes {
		if n.Managed && n.Enabled {
			if err := m.Start(ctx, n, pluginAddr); err != nil {
				m.log.Error("cannot start managed frps", "node", n.Name, "err", err)
			}
		}
	}
}

var ErrNotManaged = errors.New("node is not managed by meshportd")

// Start writes frps.toml and runs frps, restarting it if it exits.
func (m *Manager) Start(ctx context.Context, n *store.Node, pluginAddr string) error {
	if !n.Managed {
		return ErrNotManaged
	}
	if _, err := os.Stat(m.cfg.FRP.Binary); err != nil {
		return fmt.Errorf("frps binary %s: %w", m.cfg.FRP.Binary, err)
	}
	conf, err := ServerConfig(m.cfg.FRP.AuthToken, n, pluginAddr)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.cfg.FRP.ConfigDir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(m.cfg.FRP.ConfigDir, n.ID+".toml")
	if err := os.WriteFile(path, []byte(conf), 0o600); err != nil {
		return err
	}
	m.Stop(n.ID)
	pctx, cancel := context.WithCancel(ctx)
	p := &proc{cancel: cancel, done: make(chan struct{}), started: time.Now().UTC()}
	m.procMu.Lock()
	m.procs[n.ID] = p
	m.procMu.Unlock()
	go m.runProc(pctx, p, n.Name, path)
	return nil
}

func (m *Manager) runProc(ctx context.Context, p *proc, name, path string) {
	defer close(p.done)
	log := m.base.With("src", "frps:"+name)
	backoff := time.Second
	for {
		cmd := exec.CommandContext(ctx, m.cfg.FRP.Binary, "-c", path)
		out, _ := cmd.StdoutPipe()
		cmd.Stderr = cmd.Stdout
		start := time.Now()
		if err := cmd.Start(); err != nil {
			p.lastErr = err.Error()
			log.Error("frps failed to start", "err", err)
		} else {
			p.pid.Store(int64(cmd.Process.Pid))
			log.Info("frps started", "pid", cmd.Process.Pid)
			sc := bufio.NewScanner(out)
			for sc.Scan() {
				line := sc.Text()
				if strings.Contains(line, "[E]") || strings.Contains(line, "[W]") {
					log.Warn(line)
				} else {
					log.Debug(line)
				}
			}
			err := cmd.Wait()
			p.pid.Store(0)
			if ctx.Err() != nil {
				log.Info("frps stopped")
				return
			}
			p.lastErr = fmt.Sprint(err)
			log.Error("frps exited", "err", err)
			m.bus.Publish("frp.node.crashed", map[string]any{"node": name, "error": p.lastErr})
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		p.restarts++
		if backoff < time.Minute {
			backoff *= 2
		}
	}
}

// Stop terminates a managed frps process.
func (m *Manager) Stop(nodeID string) {
	m.procMu.Lock()
	p := m.procs[nodeID]
	delete(m.procs, nodeID)
	m.procMu.Unlock()
	if p != nil {
		p.cancel()
		select {
		case <-p.done:
		case <-time.After(5 * time.Second):
		}
	}
}

func (m *Manager) StopAllProcs() {
	m.procMu.Lock()
	ids := make([]string, 0, len(m.procs))
	for id := range m.procs {
		ids = append(ids, id)
	}
	m.procMu.Unlock()
	for _, id := range ids {
		m.Stop(id)
	}
}

// PollNow refreshes node and mapping statistics immediately.
func (m *Manager) PollNow() { m.pollAll() }
