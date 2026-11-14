package api

import (
	"encoding/json"
	"strings"
	"time"

	"meshport/internal/auth"
	"meshport/internal/policy"
	"meshport/internal/store"
)

// ---------- login records ----------

func (s *Server) hLogins(c *Ctx) error {
	where := []string{"1=1"}
	args := []any{}
	if c.Query("abnormal") == "1" {
		where = append(where, "abnormal=1")
	}
	if c.Query("failed") == "1" {
		where = append(where, "success=0")
	}
	if k := c.Query("kind"); k != "" {
		where = append(where, "kind=?")
		args = append(args, k)
	}
	if q := c.Query("q"); q != "" {
		l := store.Like(q)
		where = append(where, `(subject LIKE ? ESCAPE '\' OR ip LIKE ? ESCAPE '\')`)
		args = append(args, l, l)
	}
	limit := c.QueryInt("limit", 100, 1000)
	offset := c.QueryInt("offset", 0, 1_000_000)
	args = append(args, limit, offset)
	rows, err := s.db.Query(`SELECT id, kind, subject_id, subject, ip, user_agent, success, method, reason, abnormal, abnormal_reason, created_at FROM login_records WHERE `+strings.Join(where, " AND ")+` ORDER BY id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var kind, sid, subject, ip, ua, method, reason, abReason string
		var ok, ab bool
		var at store.Time
		if err := rows.Scan(&id, &kind, &sid, &subject, &ip, &ua, &ok, &method, &reason, &ab, &abReason, &at); err != nil {
			return err
		}
		out = append(out, map[string]any{"id": id, "kind": kind, "subject_id": sid, "subject": subject, "ip": ip, "user_agent": ua, "success": ok,
			"method": method, "reason": reason, "abnormal": ab, "abnormal_reason": abReason, "at": at})
	}
	day := time.Now().Add(-24 * time.Hour).Unix()
	summary := map[string]int{
		"total_24h":    s.db.Count(`SELECT COUNT(*) FROM login_records WHERE created_at>=?`, day),
		"failed_24h":   s.db.Count(`SELECT COUNT(*) FROM login_records WHERE created_at>=? AND success=0`, day),
		"abnormal_24h": s.db.Count(`SELECT COUNT(*) FROM login_records WHERE created_at>=? AND abnormal=1`, day),
	}
	blocks := []map[string]any{}
	for ip, until := range s.acl.TempBlocks() {
		blocks = append(blocks, map[string]any{"ip": ip, "until": until.UTC().Format(time.RFC3339)})
	}
	return c.OK(map[string]any{"items": out, "summary": summary, "temp_blocks": blocks})
}

func (s *Server) hUnblockIP(c *Ctx) error {
	var req struct {
		IP string `json:"ip"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	s.acl.Unblock(req.IP)
	c.Audit("security.ip_unblocked", "ip", req.IP, nil)
	return c.Done()
}

// ---------- device keys ----------

func (s *Server) hKeys(c *Ctx) error {
	list, err := s.db.Devices(store.DeviceFilter{})
	if err != nil {
		return err
	}
	maxAge := c.QueryInt("max_age_days", 180, 3650)
	out := []map[string]any{}
	for _, d := range list {
		age := int(time.Since(d.KeyCreatedAt.Time).Hours() / 24)
		state := "ok"
		switch {
		case d.KeyRotateRequired:
			state = "rotation_pending"
		case age > maxAge:
			state = "stale"
		}
		out = append(out, map[string]any{"device_id": d.ID, "device": d.Name, "did": d.DID, "fingerprint": d.Fingerprint, "key_created_at": d.KeyCreatedAt,
			"age_days": age, "state": state, "status": d.Status, "has_box_key": d.BoxPubKey != ""})
	}
	return c.OK(map[string]any{"items": out, "max_age_days": maxAge})
}

// ---------- MFA policy ----------

func (s *Server) hMFAPolicy(c *Ctx) error {
	users, err := s.db.Users()
	if err != nil {
		return err
	}
	enabled, total := 0, 0
	missing := []map[string]any{}
	for _, u := range users {
		if u.Disabled || u.Role == policy.RoleUser {
			continue
		}
		total++
		if u.MFAEnabled {
			enabled++
		} else {
			missing = append(missing, map[string]any{"id": u.ID, "username": u.Username, "role": u.Role, "reminded_at": u.MFARemindedAt})
		}
	}
	return c.OK(map[string]any{"required": s.db.Settings().MFARequired, "console_users": total, "enabled": enabled, "missing": missing})
}

func (s *Server) hSetMFAPolicy(c *Ctx) error {
	var req struct {
		Required bool `json:"required"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if req.Required && !c.P.User.MFAEnabled {
		return conflict("enable MFA on your own account before requiring it")
	}
	st := s.db.Settings()
	st.MFARequired = req.Required
	if err := s.db.SaveSettings(st); err != nil {
		return badRequest("%v", err)
	}
	c.Audit("security.mfa_policy", "settings", "mfa_required", map[string]bool{"required": req.Required})
	return c.Done()
}

// ---------- API tokens & keys ----------

var validScopes = map[string]bool{"read": true, "write": true, "admin": true}

func (s *Server) hTokens(kind string) func(*Ctx) error {
	return func(c *Ctx) error {
		q := `SELECT t.id, t.kind, t.name, t.prefix, COALESCE(u.username,''), t.scopes, t.created_at, t.expires_at, t.last_used_at, t.last_used_ip, t.revoked_at
			FROM api_tokens t LEFT JOIN users u ON u.id=t.user_id WHERE t.kind=?`
		args := []any{kind}
		if !policy.RoleAtLeast(c.P.Role, policy.RoleAdmin) && c.P.User != nil {
			q += ` AND t.user_id=?`
			args = append(args, c.P.User.ID)
		}
		rows, err := s.db.Query(q+` ORDER BY t.created_at DESC`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var id, k, name, prefix, user, scopes, lastIP string
			var created, exp, used, revoked store.Time
			if err := rows.Scan(&id, &k, &name, &prefix, &user, &scopes, &created, &exp, &used, &lastIP, &revoked); err != nil {
				return err
			}
			var sc []string
			_ = json.Unmarshal([]byte(scopes), &sc)
			state := "active"
			if !revoked.IsZero() {
				state = "revoked"
			} else if !exp.IsZero() && exp.Before(time.Now()) {
				state = "expired"
			}
			out = append(out, map[string]any{"id": id, "kind": k, "name": name, "prefix": prefix, "owner": user, "scopes": sc, "created_at": created,
				"expires_at": exp, "last_used_at": used, "last_used_ip": lastIP, "revoked_at": revoked, "state": state})
		}
		return c.Items(out)
	}
}

func (s *Server) hCreateToken(kind string) func(*Ctx) error {
	return func(c *Ctx) error {
		var req struct {
			Name    string   `json:"name"`
			Scopes  []string `json:"scopes"`
			TTLDays int      `json:"ttl_days"`
		}
		if err := c.Bind(&req); err != nil {
			return err
		}
		req.Name = strings.TrimSpace(req.Name)
		if req.Name == "" || len(req.Name) > 64 {
			return badRequest("name must be 1-64 characters")
		}
		if len(req.Scopes) == 0 {
			req.Scopes = []string{"read"}
		}
		for _, sc := range req.Scopes {
			if !validScopes[sc] {
				return badRequest("scopes must be read, write or admin")
			}
		}
		if policy.RoleRank(scopeRole(req.Scopes)) > policy.RoleRank(c.P.Role) {
			return forbidden("a token cannot exceed your own role (%s)", c.P.Role)
		}
		var exp int64
		if req.TTLDays > 0 {
			exp = time.Now().Add(time.Duration(req.TTLDays) * 24 * time.Hour).Unix()
		}
		prefixTag := "mpt"
		if kind == "key" {
			prefixTag = "mpk"
		}
		tok, h := auth.NewToken(prefixTag)
		id := store.NewID("tok")
		sc, _ := json.Marshal(req.Scopes)
		if _, err := s.db.Exec(`INSERT INTO api_tokens (id, kind, name, prefix, token_hash, user_id, scopes, created_at, expires_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			id, kind, req.Name, tok[:12], h, c.P.User.ID, string(sc), store.Now(), exp); err != nil {
			return err
		}
		c.Audit("security."+kind+".created", "api_token", id, map[string]any{"name": req.Name, "scopes": req.Scopes})
		return c.JSON(201, map[string]any{"id": id, "token": tok, "prefix": tok[:12], "note": "store this value now; it is not shown again"})
	}
}

func (s *Server) hRevokeToken(c *Ctx) error {
	id := c.Param("id")
	q := `UPDATE api_tokens SET revoked_at=? WHERE id=? AND revoked_at=0`
	args := []any{store.Now(), id}
	if !policy.RoleAtLeast(c.P.Role, policy.RoleAdmin) {
		q += ` AND user_id=?`
		args = append(args, c.P.User.ID)
	}
	res, err := s.db.Exec(q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notFoundErr("active token")
	}
	c.Audit("security.token.revoked", "api_token", id, nil)
	return c.Done()
}

// ---------- allow / deny lists ----------

func (s *Server) hACL(c *Ctx) error {
	rows, err := s.db.Query(`SELECT id, action, cidr, note, created_by, created_at FROM acl_rules ORDER BY action, created_at`)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, action, cidr, note, by string
		var at store.Time
		if err := rows.Scan(&id, &action, &cidr, &note, &by, &at); err != nil {
			return err
		}
		out = append(out, map[string]any{"id": id, "action": action, "cidr": cidr, "note": note, "created_by": by, "created_at": at})
	}
	return c.OK(map[string]any{"items": out, "your_ip": c.IP,
		"semantics": "deny blocks all traffic from the range (API, devices, frpc, visitors of published services); allow, when any exist, limits console and API-token access to those ranges"})
}

func (s *Server) hAddACL(c *Ctx) error {
	var req struct {
		Action string `json:"action"`
		CIDR   string `json:"cidr"`
		Note   string `json:"note"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if req.Action != "allow" && req.Action != "deny" {
		return badRequest("action must be allow or deny")
	}
	n, err := ParseCIDR(req.CIDR)
	if err != nil {
		return badRequest("invalid IP or CIDR %q", req.CIDR)
	}
	cidr := n.String()
	if req.Action == "deny" && n.Contains(parseIP(c.IP)) {
		return conflict("this rule would block your own address %s", c.IP)
	}
	if req.Action == "allow" && s.db.Count(`SELECT COUNT(*) FROM acl_rules WHERE action='allow'`) == 0 && !n.Contains(parseIP(c.IP)) && !isLoopback(c.IP) {
		return conflict("the first allow rule must include your own address %s, or you would lock yourself out", c.IP)
	}
	id := store.NewID("acl")
	if _, err := s.db.Exec(`INSERT INTO acl_rules (id, action, cidr, note, created_by, created_at) VALUES (?,?,?,?,?,?)`, id, req.Action, cidr, trimTo(req.Note, 120), c.P.Name(), store.Now()); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return conflict("rule already exists")
		}
		return err
	}
	if err := s.acl.Reload(); err != nil {
		return err
	}
	c.Audit("security.acl.added", "acl", id, map[string]string{"action": req.Action, "cidr": cidr})
	return c.JSON(201, map[string]string{"id": id, "cidr": cidr})
}

func (s *Server) hDeleteACL(c *Ctx) error {
	res, err := s.db.Exec(`DELETE FROM acl_rules WHERE id=?`, c.Param("id"))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notFoundErr("rule")
	}
	if err := s.acl.Reload(); err != nil {
		return err
	}
	c.Audit("security.acl.deleted", "acl", c.Param("id"), nil)
	return c.Done()
}

// ---------- bans ----------

func (s *Server) hBans(c *Ctx) error {
	list, err := s.db.Devices(store.DeviceFilter{Status: "banned"})
	if err != nil {
		return err
	}
	return c.Items(list)
}

// ---------- alerts ----------

type alertRow struct {
	ID         string     `json:"id"`
	Severity   string     `json:"severity"`
	Category   string     `json:"category"`
	Title      string     `json:"title"`
	Detail     string     `json:"detail"`
	Source     string     `json:"source"`
	Status     string     `json:"status"`
	Count      int        `json:"count"`
	CreatedAt  store.Time `json:"created_at"`
	UpdatedAt  store.Time `json:"updated_at"`
	ResolvedAt store.Time `json:"resolved_at"`
}

func (s *Server) alertList(where string, limit int, args ...any) ([]alertRow, error) {
	args = append(args, limit)
	rows, err := s.db.Query(`SELECT id, severity, category, title, detail, source, status, count, created_at, updated_at, resolved_at FROM alerts WHERE `+where+` ORDER BY updated_at DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []alertRow{}
	for rows.Next() {
		var a alertRow
		if err := rows.Scan(&a.ID, &a.Severity, &a.Category, &a.Title, &a.Detail, &a.Source, &a.Status, &a.Count, &a.CreatedAt, &a.UpdatedAt, &a.ResolvedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Server) hAlerts(c *Ctx) error {
	where := []string{"1=1"}
	args := []any{}
	if st := c.Query("status"); st != "" {
		where = append(where, "status=?")
		args = append(args, st)
	}
	if cat := c.Query("category"); cat != "" {
		where = append(where, "category=?")
		args = append(args, cat)
	}
	if sev := c.Query("severity"); sev != "" {
		where = append(where, "severity=?")
		args = append(args, sev)
	}
	list, err := s.alertList(strings.Join(where, " AND "), c.QueryInt("limit", 100, 1000), args...)
	if err != nil {
		return err
	}
	return c.OK(map[string]any{"items": list, "open": s.db.Count(`SELECT COUNT(*) FROM alerts WHERE status='open'`)})
}

func (s *Server) hAlertStatus(status string) func(*Ctx) error {
	return func(c *Ctx) error {
		resolved := int64(0)
		if status == "resolved" {
			resolved = time.Now().Unix()
		}
		res, err := s.db.Exec(`UPDATE alerts SET status=?, resolved_at=?, updated_at=? WHERE id=?`, status, resolved, store.Now(), c.Param("id"))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return notFoundErr("alert")
		}
		c.Audit("alert."+status, "alert", c.Param("id"), nil)
		return c.Done()
	}
}

// raiseAlert opens an alert or bumps the count of an open one with the same dedup key.
func (s *Server) raiseAlert(severity, category, title, detail, source, dedup string) {
	now := store.Now()
	if dedup != "" {
		res, err := s.db.Exec(`UPDATE alerts SET count=count+1, updated_at=?, detail=? WHERE dedup_key=? AND status!='resolved'`, now, detail, dedup)
		if err == nil {
			if n, _ := res.RowsAffected(); n > 0 {
				return
			}
		}
	}
	id := store.NewID("alr")
	_, err := s.db.Exec(`INSERT INTO alerts (id, severity, category, title, detail, source, dedup_key, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		id, severity, category, title, detail, source, dedup, now, now)
	if err != nil {
		s.log.Error("alert insert failed", "err", err)
		return
	}
	s.bus.Publish("alert.raised", map[string]any{"id": id, "severity": severity, "category": category, "title": title, "detail": detail})
}

// resolveAlerts closes open alerts with a dedup key (condition cleared).
func (s *Server) resolveAlerts(dedup string) {
	now := store.Now()
	res, err := s.db.Exec(`UPDATE alerts SET status='resolved', resolved_at=?, updated_at=? WHERE dedup_key=? AND status!='resolved'`, now, now, dedup)
	if err == nil {
		if n, _ := res.RowsAffected(); n > 0 {
			s.bus.Publish("alert.resolved", map[string]any{"dedup": dedup})
		}
	}
}
