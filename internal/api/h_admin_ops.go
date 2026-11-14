package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"meshport/internal/auth"
	"meshport/internal/frp"
	"meshport/internal/policy"
	"meshport/internal/store"
	"meshport/pkg/proto"
)

// ---------- overview ----------

func (s *Server) hOverview(c *Ctx) error {
	all, err := s.db.Devices(store.DeviceFilter{})
	if err != nil {
		return err
	}
	online := s.hub.OnlineIDs()
	dev := map[string]int{"total": len(all), "online": 0, "offline": 0, "pending": 0, "disabled": 0, "banned": 0}
	for _, d := range all {
		switch {
		case d.Status != "active":
			dev[d.Status]++
		case online[d.ID]:
			dev["online"]++
		default:
			dev["offline"]++
		}
	}
	sess := map[string]int{"p2p": 0, "relay": 0, "nego": 0, "fail": 0, "idle": 0}
	rows, err := s.db.Query(`SELECT path, COUNT(*) FROM comm_sessions WHERE state!='closed' AND updated_at>=? GROUP BY path`, time.Now().Add(-24*time.Hour).Unix())
	if err == nil {
		for rows.Next() {
			var p string
			var n int
			if rows.Scan(&p, &n) == nil {
				sess[p] = n
			}
		}
		rows.Close()
	}
	m := s.mon.Latest()
	nodes, _ := s.db.Nodes()
	nodesOnline := 0
	for _, n := range nodes {
		if n.Status == "online" {
			nodesOnline++
		}
	}
	pendingGrants, _ := s.db.Grants(`g.status='pending'`)
	pendingDevices, _ := s.db.Devices(store.DeviceFilter{Status: "pending"})
	alerts, _ := s.alertList(`1=1`, 6)
	return c.OK(map[string]any{
		"devices":  dev,
		"sessions": sess,
		"bandwidth": map[string]float64{
			"p2p_bps": m["p2p_bps"], "relay_bps": m["relay_bps"], "frp_bps": m["frp_bps"],
		},
		"frp": map[string]int{
			"active":       s.db.Count(`SELECT COUNT(*) FROM mappings WHERE enabled=1 AND status='online'`),
			"enabled":      s.db.Count(`SELECT COUNT(*) FROM mappings WHERE enabled=1`),
			"total":        s.db.Count(`SELECT COUNT(*) FROM mappings`),
			"nodes_online": nodesOnline, "nodes_total": len(nodes),
		},
		"resources": map[string]float64{
			"cpu": m["cpu"], "mem": m["mem"], "disk": m["disk"], "net_rx_bps": m["net_rx_bps"], "net_tx_bps": m["net_tx_bps"],
			"load1": m["load1"], "uptime_s": s.mon.Uptime().Seconds(),
		},
		"pending": map[string]any{"grants": pendingGrants, "devices": pendingDevices},
		"alerts":  alerts,
		"series": map[string]any{
			"p2p_bps":   s.mon.Series("p2p_bps", "live", 150),
			"relay_bps": s.mon.Series("relay_bps", "live", 150),
		},
	})
}

// ---------- communication sessions ----------

func (s *Server) hSessions(c *Ctx) error {
	where := []string{"s.updated_at >= ?"}
	args := []any{time.Now().Add(-24 * time.Hour).Unix()}
	if c.Query("include_closed") != "1" {
		where = append(where, "s.state != 'closed'")
	}
	if m := c.Query("mode"); m != "" {
		where = append(where, "s.mode = ?")
		args = append(args, m)
	}
	if p := c.Query("path"); p != "" {
		where = append(where, "s.path = ?")
		args = append(args, p)
	}
	if st := c.Query("state"); st != "" {
		where = append(where, "s.state = ?")
		args = append(args, st)
	}
	if d := c.Query("device"); d != "" {
		where = append(where, "(s.a_id = ? OR s.b_id = ?)")
		args = append(args, d, d)
	}
	if q := c.Query("q"); q != "" {
		where = append(where, `(a.name LIKE ? ESCAPE '\' OR b.name LIKE ? ESCAPE '\' OR s.via LIKE ? ESCAPE '\')`)
		l := store.Like(q)
		args = append(args, l, l, l)
	}
	list, err := s.db.Sessions(strings.Join(where, " AND "), args...)
	if err != nil {
		return err
	}
	var msgs struct{ Queued, Delivered, Read int }
	_ = s.db.QueryRow(`SELECT
		COALESCE(SUM(status='queued'),0), COALESCE(SUM(status='delivered'),0), COALESCE(SUM(status='read'),0)
		FROM messages WHERE created_at >= ?`, time.Now().Add(-24*time.Hour).Unix()).Scan(&msgs.Queued, &msgs.Delivered, &msgs.Read)
	return c.OK(map[string]any{"items": list, "relay_messages_24h": map[string]int{"queued": msgs.Queued, "delivered": msgs.Delivered, "read": msgs.Read}})
}

func (s *Server) hSession(c *Ctx) error {
	ses, err := s.db.SessionByID(c.Param("id"))
	if err != nil {
		return notFoundErr("session")
	}
	return c.OK(ses)
}

func (s *Server) hDisconnectSession(c *Ctx) error {
	ses, err := s.db.SessionByID(c.Param("id"))
	if err != nil {
		return notFoundErr("session")
	}
	msg := proto.SessionClose{Session: ses.ID, Reason: "closed by administrator"}
	a := s.hub.Send(ses.AID, proto.TSessionClose, msg)
	b := s.hub.Send(ses.BID, proto.TSessionClose, msg)
	now := store.Now()
	_, _ = s.db.Exec(`UPDATE comm_sessions SET state='closed', rate_bps=0, ended_at=?, updated_at=? WHERE id=?`, now, now, ses.ID)
	c.Audit("session.disconnected", "session", ses.ID, map[string]string{"a": ses.AName, "b": ses.BName})
	s.bus.Publish("session.update", map[string]any{"id": ses.ID, "state": "closed"})
	return c.OK(map[string]any{"ok": true, "notified": map[string]bool{ses.AID: a, ses.BID: b}})
}

// ---------- FRP: nodes ----------

type nodeView struct {
	*store.Node
	Stats    *frp.NodeStats  `json:"stats,omitempty"`
	Process  *frp.ProcStatus `json:"process,omitempty"`
	Mappings int             `json:"mappings"`
	Online   int             `json:"mappings_online"`
	Clients  int             `json:"frpc_clients"`
}

func (s *Server) nodeView(n *store.Node) nodeView {
	v := nodeView{Node: n}
	if st, ok := s.frp.NodeStats(n.ID); ok {
		v.Stats = &st
	}
	if ps, ok := s.frp.ProcStatus(n.ID); ok {
		v.Process = &ps
	}
	v.Mappings = s.db.Count(`SELECT COUNT(*) FROM mappings WHERE node_id=?`, n.ID)
	v.Online = s.db.Count(`SELECT COUNT(*) FROM mappings WHERE node_id=? AND status='online'`, n.ID)
	v.Clients = s.db.Count(`SELECT COUNT(DISTINCT device_id) FROM frpc_sessions WHERE node_id=?`, n.ID)
	return v
}

func (s *Server) hNodes(c *Ctx) error {
	nodes, err := s.db.Nodes()
	if err != nil {
		return err
	}
	out := []nodeView{}
	for _, n := range nodes {
		out = append(out, s.nodeView(n))
	}
	return c.Items(out)
}

type nodeReq struct {
	Name              *string `json:"name"`
	Region            *string `json:"region"`
	PublicAddr        *string `json:"public_addr"`
	BindPort          *int    `json:"bind_port"`
	VhostHTTPPort     *int    `json:"vhost_http_port"`
	VhostHTTPSPort    *int    `json:"vhost_https_port"`
	SubdomainHost     *string `json:"subdomain_host"`
	AllowPorts        *string `json:"allow_ports"`
	DashboardAddr     *string `json:"dashboard_addr"`
	DashboardUser     *string `json:"dashboard_user"`
	DashboardPassword *string `json:"dashboard_password"`
	Managed           *bool   `json:"managed"`
	Enabled           *bool   `json:"enabled"`
}

func (r *nodeReq) apply(n *store.Node) error {
	str := func(dst *string, v *string) {
		if v != nil {
			*dst = strings.TrimSpace(*v)
		}
	}
	str(&n.Name, r.Name)
	str(&n.Region, r.Region)
	str(&n.PublicAddr, r.PublicAddr)
	str(&n.SubdomainHost, r.SubdomainHost)
	str(&n.AllowPorts, r.AllowPorts)
	str(&n.DashboardAddr, r.DashboardAddr)
	str(&n.DashboardUser, r.DashboardUser)
	if r.DashboardPassword != nil {
		n.DashboardPass = *r.DashboardPassword
	}
	for _, p := range []struct {
		dst *int
		v   *int
	}{{&n.BindPort, r.BindPort}, {&n.VhostHTTPPort, r.VhostHTTPPort}, {&n.VhostHTTPSPort, r.VhostHTTPSPort}} {
		if p.v != nil {
			*p.dst = *p.v
		}
	}
	if r.Managed != nil {
		n.Managed = *r.Managed
	}
	if r.Enabled != nil {
		n.Enabled = *r.Enabled
	}
	if n.Name == "" || len(n.Name) > 32 {
		return fmt.Errorf("name must be 1-32 characters")
	}
	if n.PublicAddr == "" {
		return fmt.Errorf("public_addr is required")
	}
	for _, p := range []int{n.BindPort, n.VhostHTTPPort, n.VhostHTTPSPort} {
		if p < 0 || p > 65535 {
			return fmt.Errorf("ports must be 0-65535")
		}
	}
	if n.BindPort == 0 {
		return fmt.Errorf("bind_port is required")
	}
	if _, err := frp.ParsePorts(n.AllowPorts); err != nil {
		return fmt.Errorf("allow_ports: %v", err)
	}
	if n.Managed {
		if n.DashboardAddr == "" {
			n.DashboardAddr = "127.0.0.1:7500"
		}
		if n.DashboardUser == "" {
			n.DashboardUser = "meshport"
		}
		if n.DashboardPass == "" {
			n.DashboardPass = auth.RandomHex(16)
		}
	}
	return nil
}

func (s *Server) hCreateNode(c *Ctx) error {
	var req nodeReq
	if err := c.Bind(&req); err != nil {
		return err
	}
	n := &store.Node{BindPort: 7000, Enabled: true, PluginToken: auth.RandomHex(24)}
	if err := req.apply(n); err != nil {
		return badRequest("%v", err)
	}
	if err := s.db.CreateNode(n); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return conflict("node name already used")
		}
		return err
	}
	c.Audit("frp.node.created", "node", n.ID, map[string]any{"name": n.Name, "managed": n.Managed})
	if n.Managed && n.Enabled {
		if err := s.frp.Start(s.ctxOr(c), n, s.pluginAddr()); err != nil {
			s.log.Warn("managed frps did not start", "node", n.Name, "err", err)
		}
	}
	return c.JSON(201, map[string]any{"node": s.nodeView(n), "frps_toml": s.remoteNodeConfig(n)})
}

// remoteNodeConfig is the frps.toml an operator installs on a remote node.
func (s *Server) remoteNodeConfig(n *store.Node) string {
	addr := strings.TrimSuffix(s.cfg.PublicURL, "/") + "/internal"
	if n.Managed {
		addr = s.pluginAddr()
	}
	nn := *n
	if nn.DashboardAddr == "" {
		nn.DashboardAddr = "127.0.0.1:7500"
	}
	conf, err := frp.ServerConfig(s.cfg.FRP.AuthToken, &nn, addr)
	if err != nil {
		return "# " + err.Error()
	}
	if !n.Managed {
		conf += "# For remote nodes expose webServer to meshportd (e.g. over a private network or SSH tunnel)\n# and set dashboard_addr accordingly. If the plugin URL is https, add: tlsVerify = true\n"
	}
	return conf
}

func (s *Server) hUpdateNode(c *Ctx) error {
	n, err := s.db.NodeByID(c.Param("id"))
	if err != nil {
		return notFoundErr("node")
	}
	var req nodeReq
	if err := c.Bind(&req); err != nil {
		return err
	}
	if err := req.apply(n); err != nil {
		return badRequest("%v", err)
	}
	_, err = s.db.Exec(`UPDATE frps_nodes SET name=?, region=?, public_addr=?, bind_port=?, vhost_http_port=?, vhost_https_port=?, subdomain_host=?, allow_ports=?, dashboard_addr=?, dashboard_user=?, dashboard_pass=?, managed=?, enabled=? WHERE id=?`,
		n.Name, n.Region, n.PublicAddr, n.BindPort, n.VhostHTTPPort, n.VhostHTTPSPort, n.SubdomainHost, n.AllowPorts, n.DashboardAddr, n.DashboardUser, n.DashboardPass, n.Managed, n.Enabled, n.ID)
	if err != nil {
		return conflict("%v", err)
	}
	devs := []string{}
	rows, err := s.db.Query(`SELECT DISTINCT device_id FROM mappings WHERE node_id=?`, n.ID)
	if err == nil {
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				devs = append(devs, id)
			}
		}
		rows.Close()
	}
	for _, d := range devs {
		s.frp.NotifyDevice(d)
	}
	if n.Managed {
		if n.Enabled {
			_ = s.frp.Start(s.ctxOr(c), n, s.pluginAddr())
		} else {
			s.frp.Stop(n.ID)
		}
	}
	c.Audit("frp.node.updated", "node", n.ID, map[string]string{"name": n.Name})
	return c.OK(s.nodeView(n))
}

func (s *Server) hDeleteNode(c *Ctx) error {
	n, err := s.db.NodeByID(c.Param("id"))
	if err != nil {
		return notFoundErr("node")
	}
	maps, _ := s.db.Mappings(`m.node_id=?`, n.ID)
	s.frp.Stop(n.ID)
	if _, err := s.db.Exec(`DELETE FROM frps_nodes WHERE id=?`, n.ID); err != nil {
		return err
	}
	for _, m := range maps {
		s.frp.NotifyDevice(m.DeviceID)
	}
	c.Audit("frp.node.deleted", "node", n.ID, map[string]any{"name": n.Name, "mappings_removed": len(maps)})
	return c.Done()
}

func (s *Server) hRestartNode(c *Ctx) error {
	n, err := s.db.NodeByID(c.Param("id"))
	if err != nil {
		return notFoundErr("node")
	}
	if !n.Managed {
		return conflict("node %s is not managed by meshportd; restart it on its host", n.Name)
	}
	if err := s.frp.Start(s.ctxOr(c), n, s.pluginAddr()); err != nil {
		return errf(500, "start_failed", "%v", err)
	}
	c.Audit("frp.node.restarted", "node", n.ID, nil)
	return c.OK(s.nodeView(n))
}

func (s *Server) hNodeConfig(c *Ctx) error {
	n, err := s.db.NodeByID(c.Param("id"))
	if err != nil {
		return notFoundErr("node")
	}
	c.W.Header().Set("Content-Type", "application/toml; charset=utf-8")
	_, err = c.W.Write([]byte(s.remoteNodeConfig(n)))
	return err
}

func (s *Server) hFRPClients(c *Ctx) error {
	rows, err := s.db.Query(`SELECT f.run_id, f.node_id, COALESCE(n.name,''), f.device_id, COALESCE(d.name,''), f.version, f.client_addr, f.hostname, f.os, f.arch, f.login_at, f.last_ping_at,
		(SELECT COUNT(*) FROM mappings m WHERE m.device_id=f.device_id AND m.node_id=f.node_id AND m.status='online')
		FROM frpc_sessions f LEFT JOIN frps_nodes n ON n.id=f.node_id LEFT JOIN devices d ON d.id=f.device_id ORDER BY f.login_at DESC`)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var runID, nodeID, node, devID, dev, ver, addr, host, os_, arch string
		var login, ping store.Time
		var proxies int
		if err := rows.Scan(&runID, &nodeID, &node, &devID, &dev, &ver, &addr, &host, &os_, &arch, &login, &ping, &proxies); err != nil {
			return err
		}
		out = append(out, map[string]any{"run_id": runID, "node_id": nodeID, "node": node, "device_id": devID, "device": dev, "version": ver, "addr": addr,
			"hostname": host, "os": os_, "arch": arch, "login_at": login, "last_ping_at": ping, "proxies_online": proxies})
	}
	return c.Items(out)
}

// ---------- FRP: mappings ----------

func (s *Server) hMappings(c *Ctx) error {
	where := []string{"1=1"}
	args := []any{}
	if n := c.Query("node"); n != "" {
		where = append(where, "(m.node_id=? OR n.name=?)")
		args = append(args, n, n)
	}
	if d := c.Query("device"); d != "" {
		where = append(where, "m.device_id=?")
		args = append(args, d)
	}
	if p := c.Query("proto"); p != "" {
		where = append(where, "m.proto=?")
		args = append(args, p)
	}
	switch st := c.Query("state"); st {
	case "on":
		where = append(where, "m.enabled=1")
	case "off":
		where = append(where, "m.enabled=0")
	case "online", "offline", "error", "pending", "stopped":
		where = append(where, "m.status=?")
		args = append(args, st)
	}
	if q := c.Query("q"); q != "" {
		l := store.Like(q)
		where = append(where, `(m.name LIKE ? ESCAPE '\' OR d.name LIKE ? ESCAPE '\' OR m.domains LIKE ? ESCAPE '\' OR CAST(m.remote_port AS TEXT) LIKE ? ESCAPE '\')`)
		args = append(args, l, l, l, l)
	}
	list, err := s.db.Mappings(strings.Join(where, " AND "), args...)
	if err != nil {
		return err
	}
	return c.Items(list)
}

func (s *Server) hCreateMapping(c *Ctx) error {
	var req mappingReq
	if err := c.Bind(&req); err != nil {
		return err
	}
	if req.Device == nil || req.Node == nil {
		return badRequest("device and node are required")
	}
	d, err := s.db.DeviceByID(*req.Device)
	if err != nil {
		return badRequest("unknown device")
	}
	if !policy.CanCreateMapping(d) {
		return badRequest("device %s (level %s, %s) cannot publish services; raise it to member first", d.Name, d.Level, d.Status)
	}
	m := &store.Mapping{DeviceID: d.ID, Enabled: true, UseEncryption: true}
	out, err := s.saveMapping(c, m, &req, true)
	if err != nil {
		return err
	}
	return c.JSON(201, out)
}

func (s *Server) hUpdateMapping(c *Ctx) error {
	m, err := s.db.MappingByID(c.Param("id"))
	if err != nil {
		return notFoundErr("mapping")
	}
	var req mappingReq
	if err := c.Bind(&req); err != nil {
		return err
	}
	if req.Device != nil && *req.Device != m.DeviceID {
		return badRequest("a mapping cannot move to another device; delete and recreate it")
	}
	out, err := s.saveMapping(c, m, &req, false)
	if err != nil {
		return err
	}
	return c.OK(out)
}

func (s *Server) hDeleteMapping(c *Ctx) error {
	m, err := s.db.MappingByID(c.Param("id"))
	if err != nil {
		return notFoundErr("mapping")
	}
	return s.deleteMapping(c, m)
}

func (s *Server) hToggleMapping(on bool) func(*Ctx) error {
	return func(c *Ctx) error {
		m, err := s.db.MappingByID(c.Param("id"))
		if err != nil {
			return notFoundErr("mapping")
		}
		if err := s.frp.SetEnabled(m, on); err != nil {
			return err
		}
		c.Audit(map[bool]string{true: "frp.mapping.started", false: "frp.mapping.stopped"}[on], "mapping", m.ID, map[string]string{"name": m.Name})
		out, _ := s.db.MappingByID(m.ID)
		return c.OK(out)
	}
}

func (s *Server) hStopAll(c *Ctx) error {
	var req struct {
		Node string `json:"node"`
	}
	_ = c.Bind(&req)
	nodeID := ""
	if req.Node != "" {
		n, err := s.db.NodeByID(req.Node)
		if err != nil {
			return badRequest("unknown node")
		}
		nodeID = n.ID
	}
	count, err := s.frp.StopAll(nodeID)
	if err != nil {
		return err
	}
	c.Audit("frp.stop_all", "node", nodeID, map[string]int{"stopped": count})
	return c.OK(map[string]int{"stopped": count})
}

// ---------- access: grants & levels ----------

func (s *Server) hGrants(c *Ctx) error {
	where := "1=1"
	args := []any{}
	if st := c.Query("status"); st != "" {
		where = "g.status=?"
		args = append(args, st)
	}
	list, err := s.db.Grants(where, args...)
	if err != nil {
		return err
	}
	counts := map[string]int{}
	rows, err := s.db.Query(`SELECT status, COUNT(*) FROM grants GROUP BY status`)
	if err == nil {
		for rows.Next() {
			var st string
			var n int
			if rows.Scan(&st, &n) == nil {
				counts[st] = n
			}
		}
		rows.Close()
	}
	return c.OK(map[string]any{"items": list, "counts": counts})
}

// applyGrant activates an approved grant on its target device.
func (s *Server) applyGrant(g *store.Grant, by string) error {
	target, err := s.db.DeviceByID(g.TargetID)
	if err != nil {
		return notFoundErr("target device")
	}
	prev := target.Level
	// a newer grant supersedes active ones; keep the original base level
	var basePrev string
	err = s.db.QueryRow(`SELECT prev_level FROM grants WHERE target_id=? AND status='approved' AND id!=? ORDER BY decided_at LIMIT 1`, g.TargetID, g.ID).Scan(&basePrev)
	if err == nil && basePrev != "" {
		prev = basePrev
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	now := time.Now()
	var exp int64
	if d := validDurations[g.Duration]; d > 0 {
		exp = now.Add(d).Unix()
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE grants SET status='revoked', decision_note='superseded by '||?, decided_at=? WHERE target_id=? AND status='approved' AND id!=?`, g.ID, now.Unix(), g.TargetID, g.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE grants SET status='approved', prev_level=?, decided_at=?, decided_by=?, expires_at=? WHERE id=?`, prev, now.Unix(), by, exp, g.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE devices SET level=? WHERE id=?`, g.Level, g.TargetID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.levelNotify(g.TargetID, g.Level, "grant "+g.ID+" approved")
	return nil
}

func (s *Server) levelNotify(deviceID, level, reason string) {
	s.hub.RefreshDevice(deviceID)
	s.hub.Send(deviceID, proto.TLevelChanged, proto.LevelChanged{Level: level, Reason: reason})
	s.afterLevelChange(deviceID)
}

func (s *Server) loadGrant(c *Ctx) (*store.Grant, error) {
	g, err := s.db.GrantByID(c.Param("id"))
	if err != nil {
		return nil, notFoundErr("grant")
	}
	return g, nil
}

func (s *Server) notifyGrant(g *store.Grant, status string) {
	u := proto.GrantUpdate{GrantID: g.ID, Status: status, Target: g.TargetID, Level: g.Level}
	s.hub.Send(g.TargetID, proto.TGrantUpdate, u)
	if g.RequesterID != "" {
		s.hub.Send(g.RequesterID, proto.TGrantUpdate, u)
	}
	s.bus.Publish("grant."+status, map[string]any{"id": g.ID, "target": g.TargetName, "requester": g.RequesterName, "level": g.Level})
}

func (s *Server) hApproveGrant(c *Ctx) error {
	g, err := s.loadGrant(c)
	if err != nil {
		return err
	}
	if g.Status != "pending" {
		return conflict("grant is %s", g.Status)
	}
	var req struct {
		Duration string `json:"duration"`
		Note     string `json:"note"`
	}
	_ = c.Bind(&req)
	if req.Duration != "" {
		if _, ok := validDurations[req.Duration]; !ok {
			return badRequest("invalid duration")
		}
		g.Duration = req.Duration
		_, _ = s.db.Exec(`UPDATE grants SET duration=? WHERE id=?`, g.Duration, g.ID)
	}
	if req.Note != "" {
		_, _ = s.db.Exec(`UPDATE grants SET decision_note=? WHERE id=?`, trimTo(req.Note, 200), g.ID)
	}
	if err := s.applyGrant(g, c.P.Name()); err != nil {
		return err
	}
	c.Audit("grant.approved", "grant", g.ID, map[string]string{"target": g.TargetName, "level": g.Level, "duration": g.Duration})
	s.notifyGrant(g, "approved")
	out, _ := s.db.GrantByID(g.ID)
	return c.OK(out)
}

func (s *Server) hRejectGrant(c *Ctx) error {
	g, err := s.loadGrant(c)
	if err != nil {
		return err
	}
	if g.Status != "pending" {
		return conflict("grant is %s", g.Status)
	}
	var req struct {
		Note string `json:"note"`
	}
	_ = c.Bind(&req)
	_, err = s.db.Exec(`UPDATE grants SET status='rejected', decided_at=?, decided_by=?, decision_note=? WHERE id=?`, store.Now(), c.P.Name(), trimTo(req.Note, 200), g.ID)
	if err != nil {
		return err
	}
	c.Audit("grant.rejected", "grant", g.ID, map[string]string{"target": g.TargetName})
	s.notifyGrant(g, "rejected")
	out, _ := s.db.GrantByID(g.ID)
	return c.OK(out)
}

// endGrant revokes or expires an approved grant and restores the previous level.
func (s *Server) endGrant(g *store.Grant, status, by string) error {
	prev := g.PrevLevel
	if !policy.ValidLevel(prev) {
		prev = policy.LevelGuest
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE grants SET status=?, decided_at=?, decided_by=? WHERE id=? AND status='approved'`, status, store.Now(), by, g.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE devices SET level=? WHERE id=?`, prev, g.TargetID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.levelNotify(g.TargetID, prev, "grant "+g.ID+" "+status)
	s.notifyGrant(g, status)
	return nil
}

func (s *Server) hRevokeGrant(c *Ctx) error {
	g, err := s.loadGrant(c)
	if err != nil {
		return err
	}
	if g.Status != "approved" {
		return conflict("only approved grants can be revoked (this one is %s)", g.Status)
	}
	if err := s.endGrant(g, "revoked", c.P.Name()); err != nil {
		return err
	}
	c.Audit("grant.revoked", "grant", g.ID, map[string]string{"target": g.TargetName, "restored": g.PrevLevel})
	out, _ := s.db.GrantByID(g.ID)
	return c.OK(out)
}

// hCreateGrant lets an operator grant a (possibly temporary) level directly.
func (s *Server) hCreateGrant(c *Ctx) error {
	var req struct {
		Target   string `json:"target"`
		Level    string `json:"level"`
		Reason   string `json:"reason"`
		Duration string `json:"duration"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if req.Duration == "" {
		req.Duration = "perm"
	}
	if _, ok := validDurations[req.Duration]; !ok {
		return badRequest("invalid duration")
	}
	if !policy.ValidLevel(req.Level) {
		return badRequest("invalid level")
	}
	t, err := s.db.DeviceByID(req.Target)
	if err != nil {
		return badRequest("unknown target device")
	}
	g := &store.Grant{ID: store.NewID("grt"), TargetID: t.ID, TargetName: t.Name, Level: req.Level, Duration: req.Duration}
	if _, err := s.db.Exec(`INSERT INTO grants (id, target_id, level, reason, duration, status, created_at) VALUES (?,?,?,?,?, 'pending', ?)`,
		g.ID, t.ID, req.Level, trimTo(req.Reason, 300), req.Duration, store.Now()); err != nil {
		return err
	}
	if err := s.applyGrant(g, c.P.Name()); err != nil {
		return err
	}
	c.Audit("grant.created", "grant", g.ID, map[string]string{"target": t.Name, "level": req.Level, "duration": req.Duration})
	s.notifyGrant(g, "approved")
	out, _ := s.db.GrantByID(g.ID)
	return c.JSON(201, out)
}

func (s *Server) hLevels(c *Ctx) error {
	counts := map[string]int{}
	rows, err := s.db.Query(`SELECT level, COUNT(*) FROM devices WHERE status='active' GROUP BY level`)
	if err == nil {
		for rows.Next() {
			var l string
			var n int
			if rows.Scan(&l, &n) == nil {
				counts[l] = n
			}
		}
		rows.Close()
	}
	type lv struct {
		policy.LevelDef
		Devices int `json:"devices"`
	}
	out := []lv{}
	for _, d := range policy.LevelDefs() {
		out = append(out, lv{d, counts[d.Level]})
	}
	return c.Items(out)
}

// ctxOr returns the server context (for long-running processes started from a request).
func (s *Server) ctxOr(c *Ctx) context.Context {
	if s.ctx != nil {
		return s.ctx
	}
	return c.R.Context()
}
