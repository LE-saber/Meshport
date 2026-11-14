package api

import (
	"context"
	"errors"
	"strings"
	"time"

	"meshport/internal/auth"
	"meshport/internal/policy"
	"meshport/internal/store"
	"meshport/pkg/proto"
)

// ---------- devices ----------

func (s *Server) withOnline(list []*store.Device) []*store.Device {
	online := s.hub.OnlineIDs()
	for _, d := range list {
		d.Online = online[d.ID]
	}
	return list
}

func (s *Server) hAdminDevices(c *Ctx) error {
	f := store.DeviceFilter{Q: c.Query("q"), GroupID: c.Query("group"), UserID: c.Query("user"), Level: c.Query("level")}
	state := c.Query("state") // online | offline | disabled | pending | banned
	switch state {
	case "disabled", "pending", "banned":
		f.Status = state
	}
	list, err := s.db.Devices(f)
	if err != nil {
		return err
	}
	list = s.withOnline(list)
	if state == "online" || state == "offline" {
		out := []*store.Device{}
		for _, d := range list {
			if d.Online == (state == "online") && d.Status == "active" {
				out = append(out, d)
			}
		}
		list = out
	}
	counts := map[string]int{"all": 0, "online": 0, "offline": 0, "disabled": 0, "pending": 0, "banned": 0}
	all, _ := s.db.Devices(store.DeviceFilter{})
	online := s.hub.OnlineIDs()
	for _, d := range all {
		counts["all"]++
		switch {
		case d.Status != "active":
			counts[d.Status]++
		case online[d.ID]:
			counts["online"]++
		default:
			counts["offline"]++
		}
	}
	return c.OK(map[string]any{"items": list, "counts": counts})
}

func (s *Server) adminDevice(c *Ctx) (*store.Device, error) {
	d, err := s.db.DeviceByID(c.Param("id"))
	if err != nil {
		return nil, notFoundErr("device")
	}
	d.Online = s.hub.Online(d.ID)
	return d, nil
}

func (s *Server) hAdminDevice(c *Ctx) error {
	d, err := s.adminDevice(c)
	if err != nil {
		return err
	}
	sessions, _ := s.db.Sessions(`(s.a_id=? OR s.b_id=?) AND s.state!='closed'`, d.ID, d.ID)
	maps, _ := s.db.Mappings(`m.device_id=?`, d.ID)
	grants, _ := s.db.Grants(`g.target_id=? OR g.requester_id=?`, d.ID, d.ID)
	logins := []map[string]any{}
	rows, err := s.db.Query(`SELECT ip, success, reason, created_at FROM login_records WHERE kind IN ('device','frpc') AND subject_id=? ORDER BY id DESC LIMIT 20`, d.ID)
	if err == nil {
		for rows.Next() {
			var ip, reason string
			var ok bool
			var at store.Time
			if rows.Scan(&ip, &ok, &reason, &at) == nil {
				logins = append(logins, map[string]any{"ip": ip, "success": ok, "reason": reason, "at": at})
			}
		}
		rows.Close()
	}
	keyAge := int(time.Since(d.KeyCreatedAt.Time).Hours() / 24)
	return c.OK(map[string]any{
		"device": d, "connection": s.hub.Conn(d.ID), "sessions": sessions, "mappings": maps, "grants": grants,
		"logins": logins, "key_age_days": keyAge, "capabilities": levelCaps(d.Level),
	})
}

func (s *Server) hAdminUpdateDevice(c *Ctx) error {
	d, err := s.adminDevice(c)
	if err != nil {
		return err
	}
	var req struct {
		Name  *string `json:"name"`
		Level *string `json:"level"`
		Group *string `json:"group"` // group id or name ("" clears)
		User  *string `json:"user"`  // user id or username ("" clears)
		Kind  *string `json:"kind"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	changes := map[string]any{}
	if req.Name != nil {
		n := strings.TrimSpace(*req.Name)
		if n == "" || len(n) > 64 {
			return badRequest("name must be 1-64 characters")
		}
		d.Name, changes["name"] = n, n
	}
	if req.Kind != nil {
		if !validKinds[*req.Kind] {
			return badRequest("invalid kind")
		}
		d.Kind, changes["kind"] = *req.Kind, *req.Kind
	}
	levelChanged := false
	if req.Level != nil && *req.Level != d.Level {
		if !policy.ValidLevel(*req.Level) {
			return badRequest("level must be admin, member or guest")
		}
		changes["level"] = map[string]string{"from": d.Level, "to": *req.Level}
		d.Level, levelChanged = *req.Level, true
	}
	if req.Group != nil {
		gid := ""
		if *req.Group != "" {
			if err := s.db.QueryRow(`SELECT id FROM groups WHERE id=? OR name=?`, *req.Group, *req.Group).Scan(&gid); err != nil {
				return badRequest("unknown group %q", *req.Group)
			}
		}
		d.GroupID, changes["group"] = gid, *req.Group
	}
	if req.User != nil {
		uid := ""
		if *req.User != "" {
			if err := s.db.QueryRow(`SELECT id FROM users WHERE id=? OR username=?`, *req.User, *req.User).Scan(&uid); err != nil {
				return badRequest("unknown user %q", *req.User)
			}
		}
		d.UserID, changes["user"] = uid, *req.User
	}
	_, err = s.db.Exec(`UPDATE devices SET name=?, kind=?, level=?, group_id=?, user_id=? WHERE id=?`, d.Name, d.Kind, d.Level, nullable(d.GroupID), nullable(d.UserID), d.ID)
	if err != nil {
		return err
	}
	s.hub.RefreshDevice(d.ID)
	if levelChanged {
		s.afterLevelChange(d.ID)
	}
	c.Audit("device.updated", "device", d.ID, changes)
	s.bus.Publish("device.updated", map[string]any{"id": d.ID, "changes": changes})
	out, _ := s.db.DeviceByID(d.ID)
	out.Online = s.hub.Online(d.ID)
	return c.OK(out)
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// afterLevelChange re-evaluates FRP access when a device's level drops.
func (s *Server) afterLevelChange(deviceID string) {
	d, err := s.db.DeviceByID(deviceID)
	if err != nil {
		return
	}
	if !policy.CanCreateMapping(d) {
		maps, _ := s.db.Mappings(`m.device_id=?`, d.ID)
		for _, m := range maps {
			s.frp.Kick(m.NodeID, d.ID)
		}
	}
	s.frp.NotifyDevice(d.ID)
}

func (s *Server) setDeviceStatus(c *Ctx, status, reason, action string) error {
	d, err := s.adminDevice(c)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(`UPDATE devices SET status=?, ban_reason=? WHERE id=?`, status, reason, d.ID); err != nil {
		return err
	}
	if status != "active" {
		_, _ = s.db.Exec(`DELETE FROM device_tokens WHERE device_id=?`, d.ID)
		s.hub.Kick(d.ID, "device "+status)
		maps, _ := s.db.Mappings(`m.device_id=?`, d.ID)
		for _, m := range maps {
			s.frp.Kick(m.NodeID, d.ID)
		}
	} else {
		s.resolveAlerts("pending:" + d.ID)
	}
	c.Audit(action, "device", d.ID, map[string]string{"reason": reason})
	s.bus.Publish(action, map[string]any{"id": d.ID, "name": d.Name, "reason": reason})
	out, _ := s.db.DeviceByID(d.ID)
	out.Online = s.hub.Online(d.ID)
	return c.OK(out)
}

func (s *Server) hApproveDevice(c *Ctx) error {
	d, err := s.adminDevice(c)
	if err != nil {
		return err
	}
	if d.Status != "pending" {
		return conflict("device is %s, not pending", d.Status)
	}
	return s.setDeviceStatus(c, "active", "", "device.approved")
}

func (s *Server) hRejectDevice(c *Ctx) error {
	d, err := s.adminDevice(c)
	if err != nil {
		return err
	}
	if d.Status != "pending" {
		return conflict("device is %s, not pending", d.Status)
	}
	if _, err := s.db.Exec(`DELETE FROM devices WHERE id=?`, d.ID); err != nil {
		return err
	}
	s.resolveAlerts("pending:" + d.ID)
	c.Audit("device.rejected", "device", d.ID, map[string]string{"name": d.Name})
	return c.Done()
}

func (s *Server) hDisableDevice(c *Ctx) error {
	return s.setDeviceStatus(c, "disabled", "", "device.disabled")
}
func (s *Server) hEnableDevice(c *Ctx) error {
	return s.setDeviceStatus(c, "active", "", "device.enabled")
}

func (s *Server) hBanDevice(c *Ctx) error {
	var req struct {
		Reason string `json:"reason"`
	}
	_ = c.Bind(&req)
	return s.setDeviceStatus(c, "banned", trimTo(req.Reason, 200), "device.banned")
}

func (s *Server) hUnbanDevice(c *Ctx) error {
	d, err := s.adminDevice(c)
	if err != nil {
		return err
	}
	if d.Status != "banned" {
		return conflict("device is not banned")
	}
	return s.setDeviceStatus(c, "active", "", "device.unbanned")
}

func (s *Server) hDeleteDevice(c *Ctx) error {
	d, err := s.adminDevice(c)
	if err != nil {
		return err
	}
	maps, _ := s.db.Mappings(`m.device_id=?`, d.ID)
	s.hub.Kick(d.ID, "device deleted")
	if _, err := s.db.Exec(`DELETE FROM devices WHERE id=?`, d.ID); err != nil {
		return err
	}
	for _, m := range maps {
		s.frp.Kick(m.NodeID, d.ID)
	}
	_, _ = s.db.Exec(`DELETE FROM messages WHERE to_id=? OR from_id=?`, d.ID, d.ID)
	c.Audit("device.deleted", "device", d.ID, map[string]string{"name": d.Name, "did": d.DID})
	s.bus.Publish("device.deleted", map[string]any{"id": d.ID, "name": d.Name})
	return c.Done()
}

func (s *Server) hRequireKeyRotation(c *Ctx) error {
	d, err := s.adminDevice(c)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(`UPDATE devices SET key_rotate_required=1 WHERE id=?`, d.ID); err != nil {
		return err
	}
	pushed := s.hub.Send(d.ID, proto.TCmd, proto.Cmd{ID: store.NewID("cmd"), Name: "rotate-key"})
	c.Audit("device.key_rotation_required", "device", d.ID, nil)
	return c.OK(map[string]any{"ok": true, "pushed": pushed})
}

func (s *Server) hDeviceCommand(c *Ctx) error {
	d, err := s.adminDevice(c)
	if err != nil {
		return err
	}
	var req struct {
		Name    string            `json:"name"`
		Args    map[string]string `json:"args"`
		Timeout int               `json:"timeout_s"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	allowed := map[string]bool{"reload-frpc": true, "rotate-key": true, "refresh-peers": true, "disconnect": true, "collect-logs": true, "restart-client": true, "ping": true}
	if !allowed[req.Name] {
		return badRequest("unknown command; allowed: reload-frpc, rotate-key, refresh-peers, disconnect, collect-logs, restart-client, ping")
	}
	if req.Timeout <= 0 || req.Timeout > 60 {
		req.Timeout = 15
	}
	c.Audit("device.command", "device", d.ID, map[string]any{"name": req.Name, "args": req.Args})
	ctx, cancel := context.WithTimeout(c.R.Context(), time.Duration(req.Timeout)*time.Second)
	defer cancel()
	res, err := s.hub.Command(ctx, d.ID, req.Name, req.Args)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return errf(504, "timeout", "device did not answer within %d s", req.Timeout)
		}
		return errf(409, "offline", "%v", err)
	}
	return c.OK(res)
}

// ---------- groups ----------

func (s *Server) hGroups(c *Ctx) error {
	list, err := s.db.Groups()
	if err != nil {
		return err
	}
	return c.Items(list)
}

func (s *Server) hCreateGroup(c *Ctx) error {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 48 {
		return badRequest("name must be 1-48 characters")
	}
	id := store.NewID("grp")
	if _, err := s.db.Exec(`INSERT INTO groups (id, name, description, created_at) VALUES (?,?,?,?)`, id, req.Name, trimTo(req.Description, 200), store.Now()); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return conflict("group %s exists", req.Name)
		}
		return err
	}
	c.Audit("group.created", "group", id, map[string]string{"name": req.Name})
	return c.JSON(201, map[string]string{"id": id, "name": req.Name})
}

func (s *Server) hUpdateGroup(c *Ctx) error {
	var req struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	id := c.Param("id")
	if s.db.Count(`SELECT COUNT(*) FROM groups WHERE id=?`, id) == 0 {
		return notFoundErr("group")
	}
	if req.Name != nil {
		if _, err := s.db.Exec(`UPDATE groups SET name=? WHERE id=?`, strings.TrimSpace(*req.Name), id); err != nil {
			return conflict("name already used")
		}
	}
	if req.Description != nil {
		_, _ = s.db.Exec(`UPDATE groups SET description=? WHERE id=?`, trimTo(*req.Description, 200), id)
	}
	c.Audit("group.updated", "group", id, req)
	return c.Done()
}

func (s *Server) hDeleteGroup(c *Ctx) error {
	res, err := s.db.Exec(`DELETE FROM groups WHERE id=?`, c.Param("id"))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notFoundErr("group")
	}
	c.Audit("group.deleted", "group", c.Param("id"), nil)
	return c.Done()
}

// ---------- users ----------

type userView struct {
	*store.User
	Devices int            `json:"devices"`
	Levels  map[string]int `json:"levels"`
}

func (s *Server) hUsers(c *Ctx) error {
	users, err := s.db.Users()
	if err != nil {
		return err
	}
	out := []userView{}
	for _, u := range users {
		v := userView{User: u, Levels: map[string]int{}}
		rows, err := s.db.Query(`SELECT level, COUNT(*) FROM devices WHERE user_id=? GROUP BY level`, u.ID)
		if err == nil {
			for rows.Next() {
				var l string
				var n int
				if rows.Scan(&l, &n) == nil {
					v.Levels[l] = n
					v.Devices += n
				}
			}
			rows.Close()
		}
		out = append(out, v)
	}
	return c.Items(out)
}

func (s *Server) hCreateUser(c *Ctx) error {
	var req struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Email       string `json:"email"`
		Password    string `json:"password"`
		Role        string `json:"role"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	req.Username = strings.TrimSpace(req.Username)
	if len(req.Username) < 2 || len(req.Username) > 48 || strings.ContainsAny(req.Username, " /\\:@") {
		return badRequest("username must be 2-48 characters without spaces, '/', ':' or '@'")
	}
	if req.Role == "" {
		req.Role = policy.RoleUser
	}
	if !policy.ValidRole(req.Role) {
		return badRequest("role must be admin, operator, viewer or user")
	}
	generated := ""
	if req.Password == "" {
		generated = auth.RandomPassword()
		req.Password = generated
	}
	h, err := auth.HashPassword(req.Password)
	if err != nil {
		return badRequest("%v", err)
	}
	u := &store.User{Username: req.Username, DisplayName: trimTo(req.DisplayName, 64), Email: trimTo(req.Email, 120), PasswordHash: h, Role: req.Role}
	if err := s.db.CreateUser(u); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return conflict("username already exists")
		}
		return err
	}
	c.Audit("user.created", "user", u.ID, map[string]string{"username": u.Username, "role": u.Role})
	out := map[string]any{"user": u}
	if generated != "" {
		out["password"] = generated
	}
	return c.JSON(201, out)
}

func (s *Server) hUpdateUser(c *Ctx) error {
	u, err := s.db.UserByID(c.Param("id"))
	if err != nil {
		return notFoundErr("user")
	}
	var req struct {
		DisplayName *string `json:"display_name"`
		Email       *string `json:"email"`
		Role        *string `json:"role"`
		Disabled    *bool   `json:"disabled"`
		Password    *string `json:"password"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if req.Role != nil {
		if !policy.ValidRole(*req.Role) {
			return badRequest("invalid role")
		}
		if u.Role == policy.RoleAdmin && *req.Role != policy.RoleAdmin && s.db.Count(`SELECT COUNT(*) FROM users WHERE role='admin' AND disabled=0`) <= 1 {
			return conflict("cannot demote the last admin")
		}
		u.Role = *req.Role
	}
	if req.Disabled != nil {
		if *req.Disabled && u.ID == c.P.ID() {
			return conflict("you cannot disable yourself")
		}
		u.Disabled = *req.Disabled
	}
	if req.DisplayName != nil {
		u.DisplayName = trimTo(*req.DisplayName, 64)
	}
	if req.Email != nil {
		u.Email = trimTo(*req.Email, 120)
	}
	if req.Password != nil {
		h, err := auth.HashPassword(*req.Password)
		if err != nil {
			return badRequest("%v", err)
		}
		u.PasswordHash = h
		_, _ = s.db.Exec(`DELETE FROM web_sessions WHERE user_id=?`, u.ID)
	}
	_, err = s.db.Exec(`UPDATE users SET display_name=?, email=?, role=?, disabled=?, password_hash=? WHERE id=?`, u.DisplayName, u.Email, u.Role, u.Disabled, u.PasswordHash, u.ID)
	if err != nil {
		return err
	}
	if u.Disabled {
		_, _ = s.db.Exec(`DELETE FROM web_sessions WHERE user_id=?`, u.ID)
	}
	c.Audit("user.updated", "user", u.ID, map[string]any{"role": u.Role, "disabled": u.Disabled, "password_reset": req.Password != nil})
	return c.OK(u)
}

func (s *Server) hDeleteUser(c *Ctx) error {
	u, err := s.db.UserByID(c.Param("id"))
	if err != nil {
		return notFoundErr("user")
	}
	if u.ID == c.P.ID() {
		return conflict("you cannot delete yourself")
	}
	if u.Role == policy.RoleAdmin && s.db.Count(`SELECT COUNT(*) FROM users WHERE role='admin'`) <= 1 {
		return conflict("cannot delete the last admin")
	}
	if _, err := s.db.Exec(`DELETE FROM users WHERE id=?`, u.ID); err != nil {
		return err
	}
	c.Audit("user.deleted", "user", u.ID, map[string]string{"username": u.Username})
	return c.Done()
}

func (s *Server) hResetUserMFA(c *Ctx) error {
	u, err := s.db.UserByID(c.Param("id"))
	if err != nil {
		return notFoundErr("user")
	}
	_, err = s.db.Exec(`UPDATE users SET mfa_enabled=0, mfa_secret='' WHERE id=?`, u.ID)
	if err != nil {
		return err
	}
	c.Audit("user.mfa_reset", "user", u.ID, nil)
	return c.Done()
}

// hRemindMFA marks users without MFA as reminded and emits an event that
// webhooks (mail/chat integrations) can turn into a notification.
func (s *Server) hRemindMFA(c *Ctx) error {
	var req struct {
		Users []string `json:"users"` // empty = everyone without MFA
	}
	_ = c.Bind(&req)
	users, err := s.db.Users()
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, u := range req.Users {
		want[u] = true
	}
	reminded := []string{}
	for _, u := range users {
		if u.MFAEnabled || u.Disabled || (len(want) > 0 && !want[u.ID] && !want[u.Username]) {
			continue
		}
		_, _ = s.db.Exec(`UPDATE users SET mfa_reminded_at=? WHERE id=?`, store.Now(), u.ID)
		reminded = append(reminded, u.Username)
		s.bus.Publish("user.mfa_reminder", map[string]any{"user_id": u.ID, "username": u.Username, "email": u.Email})
	}
	c.Audit("user.mfa_reminded", "user", "", map[string]any{"users": reminded})
	return c.OK(map[string]any{"reminded": reminded})
}

// ---------- enrollment tokens ----------

func (s *Server) hEnrollTokens(c *Ctx) error {
	rows, err := s.db.Query(`SELECT e.id, e.note, COALESCE(u.username,''), COALESCE(g.name,''), e.level, e.auto_approve, e.uses_left, e.created_by, e.created_at, e.expires_at
		FROM enroll_tokens e LEFT JOIN users u ON u.id=e.user_id LEFT JOIN groups g ON g.id=e.group_id WHERE e.expires_at > ? AND e.uses_left > 0 ORDER BY e.created_at DESC`, time.Now().Unix())
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, note, user, group, level, by string
		var auto bool
		var uses int
		var created, exp store.Time
		if err := rows.Scan(&id, &note, &user, &group, &level, &auto, &uses, &by, &created, &exp); err != nil {
			return err
		}
		out = append(out, map[string]any{"id": id, "note": note, "user": user, "group": group, "level": level, "auto_approve": auto, "uses_left": uses, "created_by": by, "created_at": created, "expires_at": exp})
	}
	return c.Items(out)
}

func (s *Server) hCreateEnrollToken(c *Ctx) error {
	var req struct {
		Note        string `json:"note"`
		User        string `json:"user"`
		Group       string `json:"group"`
		Level       string `json:"level"`
		AutoApprove *bool  `json:"auto_approve"`
		Uses        int    `json:"uses"`
		TTLHours    int    `json:"ttl_hours"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if req.Level == "" {
		req.Level = policy.LevelGuest
	}
	if !policy.ValidLevel(req.Level) {
		return badRequest("invalid level")
	}
	if req.Uses <= 0 {
		req.Uses = 1
	}
	if req.Uses > 1000 {
		return badRequest("uses must be <= 1000")
	}
	if req.TTLHours <= 0 {
		req.TTLHours = 24
	}
	if req.TTLHours > 24*90 {
		return badRequest("ttl_hours must be <= 2160")
	}
	auto := true
	if req.AutoApprove != nil {
		auto = *req.AutoApprove
	}
	var uid, gid any
	if c.P.Role == policy.RoleUser || req.User == "" {
		if c.P.User != nil {
			uid = c.P.User.ID
		}
	}
	if req.User != "" && policy.RoleAtLeast(c.P.Role, policy.RoleOperator) {
		var id string
		if err := s.db.QueryRow(`SELECT id FROM users WHERE id=? OR username=?`, req.User, req.User).Scan(&id); err != nil {
			return badRequest("unknown user")
		}
		uid = id
	}
	if req.Group != "" {
		id, err := s.db.GroupIDByName(req.Group, false)
		if err != nil {
			if err2 := s.db.QueryRow(`SELECT id FROM groups WHERE id=?`, req.Group).Scan(&id); err2 != nil {
				return badRequest("unknown group")
			}
		}
		gid = id
	}
	// plain users may only enroll guest devices for themselves, pending approval
	if c.P.Role == policy.RoleUser {
		req.Level, auto, gid = policy.LevelGuest, false, nil
	}
	tok, h := auth.NewToken("mpe")
	id := store.NewID("enr")
	exp := time.Now().Add(time.Duration(req.TTLHours) * time.Hour)
	if _, err := s.db.Exec(`INSERT INTO enroll_tokens (id, token_hash, note, user_id, group_id, level, auto_approve, uses_left, created_by, created_at, expires_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		id, h, trimTo(req.Note, 120), uid, gid, req.Level, auto, req.Uses, c.P.Name(), store.Now(), exp.Unix()); err != nil {
		return err
	}
	c.Audit("enroll_token.created", "enroll_token", id, map[string]any{"level": req.Level, "uses": req.Uses})
	return c.JSON(201, map[string]any{"id": id, "token": tok, "expires_at": exp.UTC().Format(time.RFC3339),
		"enroll_url": strings.TrimSuffix(s.cfg.PublicURL, "/") + "/enroll#" + tok})
}

func (s *Server) hDeleteEnrollToken(c *Ctx) error {
	res, err := s.db.Exec(`DELETE FROM enroll_tokens WHERE id=?`, c.Param("id"))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notFoundErr("enrollment token")
	}
	c.Audit("enroll_token.deleted", "enroll_token", c.Param("id"), nil)
	return c.Done()
}
