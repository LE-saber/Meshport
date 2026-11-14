package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"meshport/internal/auth"
	"meshport/internal/policy"
	"meshport/internal/store"
)

// ---------- login records & abnormal detection ----------

// loginRecord stores an authentication attempt and flags anomalies. It
// returns true when the attempt was flagged abnormal.
func (s *Server) loginRecord(kind, subjectID, subject string, c *Ctx, ok bool, reason, method string) bool {
	now := time.Now()
	abnormal, why := false, ""
	limit := s.db.Settings().AbnormalFailLimit
	if !ok {
		fails := s.db.Count(`SELECT COUNT(*) FROM login_records WHERE ip=? AND success=0 AND created_at>=?`, c.IP, now.Add(-10*time.Minute).Unix())
		if fails+1 >= limit {
			abnormal, why = true, "repeated failed logins from this address"
			s.acl.Block(c.IP, 15*time.Minute)
		}
	} else if kind == "user" && subjectID != "" {
		prior := s.db.Count(`SELECT COUNT(*) FROM login_records WHERE kind='user' AND subject_id=? AND success=1`, subjectID)
		fromIP := s.db.Count(`SELECT COUNT(*) FROM login_records WHERE kind='user' AND subject_id=? AND success=1 AND ip=? AND created_at>=?`, subjectID, c.IP, now.Add(-90*24*time.Hour).Unix())
		recentFails := s.db.Count(`SELECT COUNT(*) FROM login_records WHERE kind='user' AND subject=? AND success=0 AND created_at>=?`, subject, now.Add(-10*time.Minute).Unix())
		switch {
		case recentFails >= limit:
			abnormal, why = true, "successful login after repeated failures"
		case prior > 0 && fromIP == 0:
			abnormal, why = true, "first login from a new address"
		}
	}
	ua := c.R.UserAgent()
	if len(ua) > 200 {
		ua = ua[:200]
	}
	_, _ = s.db.Exec(`INSERT INTO login_records (kind, subject_id, subject, ip, user_agent, success, method, reason, abnormal, abnormal_reason, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		kind, subjectID, subject, c.IP, ua, ok, method, reason, abnormal, why, now.Unix())
	if abnormal {
		sev := "warn"
		if !ok {
			sev = "crit"
		}
		s.raiseAlert(sev, "security", "Abnormal login: "+why, subject+" from "+c.IP, "auth", "login:"+c.IP+":"+why)
		s.bus.Publish("security.abnormal_login", map[string]any{"kind": kind, "subject": subject, "ip": c.IP, "reason": why, "success": ok})
	}
	return abnormal
}

func (s *Server) loginGuard(c *Ctx) error {
	if s.acl.Blocked(c.IP) {
		return errf(http.StatusTooManyRequests, "too_many_attempts", "too many failed attempts; try again in 15 minutes")
	}
	return nil
}

// ---------- console users ----------

type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
	TOTP     string `json:"totp"`
}

func (s *Server) hLogin(c *Ctx) error {
	if err := s.loginGuard(c); err != nil {
		return err
	}
	var req loginReq
	if err := c.Bind(&req); err != nil {
		return err
	}
	u, err := s.db.UserByName(strings.TrimSpace(req.Username))
	hash := ""
	if err == nil {
		hash = u.PasswordHash
	}
	if !auth.CheckPassword(hash, req.Password) || u == nil || u.Disabled {
		id := ""
		if u != nil {
			id = u.ID
		}
		s.loginRecord("user", id, req.Username, c, false, "bad credentials", "password")
		return errf(401, "invalid_credentials", "invalid username or password")
	}
	if u.MFAEnabled {
		if req.TOTP == "" {
			return errf(401, "mfa_required", "enter the code from your authenticator app")
		}
		if !auth.CheckTOTP(u.MFASecret, req.TOTP, time.Now()) {
			s.loginRecord("user", u.ID, u.Username, c, false, "bad TOTP code", "password+totp")
			return errf(401, "invalid_mfa", "invalid authenticator code")
		}
	}
	method := "password"
	if u.MFAEnabled {
		method = "password+totp"
	}
	s.loginRecord("user", u.ID, u.Username, c, true, "", method)
	tok, h := auth.NewToken("mps")
	hours := s.db.Settings().SessionHours
	exp := time.Now().Add(time.Duration(hours) * time.Hour)
	ua := c.R.UserAgent()
	if len(ua) > 200 {
		ua = ua[:200]
	}
	if _, err := s.db.Exec(`INSERT INTO web_sessions (id, user_id, token_hash, ip, user_agent, created_at, expires_at, last_seen_at) VALUES (?,?,?,?,?,?,?,?)`,
		store.NewID("ws"), u.ID, h, c.IP, ua, store.Now(), exp.Unix(), store.Now()); err != nil {
		return err
	}
	_, _ = s.db.Exec(`UPDATE users SET last_login_at=? WHERE id=?`, store.Now(), u.ID)
	http.SetCookie(c.W, &http.Cookie{Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true, Secure: c.R.TLS != nil || strings.HasPrefix(s.cfg.PublicURL, "https"),
		SameSite: http.SameSiteStrictMode, Expires: exp})
	return c.OK(map[string]any{
		"token": tok, "expires_at": exp.UTC().Format(time.RFC3339), "user": u,
		"mfa_setup_required": s.db.Settings().MFARequired && !u.MFAEnabled,
	})
}

func (s *Server) hLogout(c *Ctx) error {
	if c.P.Session != "" {
		_, _ = s.db.Exec(`DELETE FROM web_sessions WHERE id=?`, c.P.Session)
	}
	http.SetCookie(c.W, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	return c.Done()
}

func (s *Server) hMe(c *Ctx) error {
	out := map[string]any{"kind": c.P.Kind, "role": c.P.Role}
	if c.P.User != nil {
		out["user"] = c.P.User
	}
	if c.P.Device != nil {
		out["device"] = c.P.Device
	}
	out["settings"] = map[string]any{"console_name": s.db.Settings().ConsoleName, "theme": s.db.Settings().Theme, "accent": s.db.Settings().Accent, "language": s.db.Settings().Language}
	return c.OK(out)
}

func (s *Server) hChangePassword(c *Ctx) error {
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if !auth.CheckPassword(c.P.User.PasswordHash, req.Current) {
		return errf(401, "invalid_credentials", "current password is wrong")
	}
	h, err := auth.HashPassword(req.New)
	if err != nil {
		return badRequest("%v", err)
	}
	_, err = s.db.Exec(`UPDATE users SET password_hash=? WHERE id=?`, h, c.P.User.ID)
	if err != nil {
		return err
	}
	// sign out other sessions
	_, _ = s.db.Exec(`DELETE FROM web_sessions WHERE user_id=? AND id!=?`, c.P.User.ID, c.P.Session)
	c.Audit("user.password_changed", "user", c.P.User.ID, nil)
	return c.Done()
}

func (s *Server) hMFASetup(c *Ctx) error {
	if c.P.User.MFAEnabled {
		return conflict("MFA is already enabled; disable it first")
	}
	secret := auth.NewTOTPSecret()
	if _, err := s.db.Exec(`UPDATE users SET mfa_secret=? WHERE id=?`, secret, c.P.User.ID); err != nil {
		return err
	}
	return c.OK(map[string]string{"secret": secret, "otpauth_uri": auth.TOTPURI(s.db.Settings().ConsoleName, c.P.User.Username, secret)})
}

func (s *Server) hMFAEnable(c *Ctx) error {
	var req struct {
		Code string `json:"code"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	u, err := s.db.UserByID(c.P.User.ID)
	if err != nil {
		return err
	}
	if u.MFASecret == "" {
		return badRequest("call /auth/mfa/setup first")
	}
	if !auth.CheckTOTP(u.MFASecret, req.Code, time.Now()) {
		return errf(400, "invalid_mfa", "invalid authenticator code")
	}
	_, err = s.db.Exec(`UPDATE users SET mfa_enabled=1 WHERE id=?`, u.ID)
	if err != nil {
		return err
	}
	c.Audit("user.mfa_enabled", "user", u.ID, nil)
	return c.Done()
}

func (s *Server) hMFADisable(c *Ctx) error {
	var req struct {
		Code string `json:"code"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if !c.P.User.MFAEnabled {
		return badRequest("MFA is not enabled")
	}
	if !auth.CheckTOTP(c.P.User.MFASecret, req.Code, time.Now()) {
		return errf(400, "invalid_mfa", "invalid authenticator code")
	}
	_, err := s.db.Exec(`UPDATE users SET mfa_enabled=0, mfa_secret='' WHERE id=?`, c.P.User.ID)
	if err != nil {
		return err
	}
	c.Audit("user.mfa_disabled", "user", c.P.User.ID, nil)
	return c.Done()
}

// ---------- device registration & login ----------

type registerReq struct {
	Name          string `json:"name"`
	OS            string `json:"os"`
	Kind          string `json:"kind"`
	PubKey        string `json:"pubkey"`
	BoxPubKey     string `json:"box_pubkey"`
	Timestamp     int64  `json:"ts"`
	Signature     string `json:"sig"`
	EnrollToken   string `json:"enroll_token"`
	Username      string `json:"username"`
	Password      string `json:"password"`
	TOTP          string `json:"totp"`
	LanIP         string `json:"lan_ip"`
	ClientVersion string `json:"client_version"`
}

var validKinds = map[string]bool{"laptop": true, "phone": true, "tablet": true, "nas": true, "server": true, "monitor": true, "chip": true}

func (s *Server) hRegister(c *Ctx) error {
	if err := s.loginGuard(c); err != nil {
		return err
	}
	var req registerReq
	if err := c.Bind(&req); err != nil {
		return err
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 64 {
		return badRequest("name must be 1-64 characters")
	}
	if req.Kind == "" {
		req.Kind = "laptop"
	}
	if !validKinds[req.Kind] {
		return badRequest("kind must be one of laptop, phone, tablet, nas, server, monitor, chip")
	}
	pub, err := auth.ParsePublicKey(req.PubKey)
	if err != nil {
		return badRequest("%v", err)
	}
	if err := auth.ParseBoxKey(req.BoxPubKey); err != nil {
		return badRequest("%v", err)
	}
	if d := time.Since(time.Unix(req.Timestamp, 0)); d > 5*time.Minute || d < -5*time.Minute {
		return badRequest("ts must be within 5 minutes of server time")
	}
	if !auth.VerifySignature(pub, auth.RegisterMessage(req.PubKey, req.Timestamp), req.Signature) {
		return badRequest("signature does not match pubkey")
	}
	if s.db.Count(`SELECT COUNT(*) FROM devices WHERE pubkey=?`, req.PubKey) > 0 {
		return conflict("this key is already registered")
	}
	st := s.db.Settings()
	d := &store.Device{
		ID: store.NewID("dev"), DID: auth.NewDID(), Name: req.Name, OS: trimTo(req.OS, 64), Kind: req.Kind,
		PubKey: req.PubKey, BoxPubKey: req.BoxPubKey, Fingerprint: auth.Fingerprint(pub), KeyCreatedAt: store.Now(),
		FRPSecret: auth.RandomHex(24), LastIP: c.IP, LanIP: trimTo(req.LanIP, 64), ClientVersion: trimTo(req.ClientVersion, 32), RegisteredAt: store.Now(),
	}
	method := ""
	switch {
	case req.EnrollToken != "":
		method = "enroll token"
		h := auth.HashToken(req.EnrollToken)
		var id, userID, groupID, level string
		var auto, tlsRequired bool
		var uses int
		var exp int64
		err := s.db.QueryRow(`SELECT id, COALESCE(user_id,''), COALESCE(group_id,''), level, auto_approve, uses_left, expires_at, tls_required FROM enroll_tokens WHERE token_hash=?`, h).
			Scan(&id, &userID, &groupID, &level, &auto, &uses, &exp, &tlsRequired)
		if err != nil || uses <= 0 || exp < time.Now().Unix() {
			s.loginRecord("device", "", req.Name, c, false, "invalid enrollment token", "enroll token")
			return errf(401, "invalid_enroll_token", "enrollment token is invalid, used up or expired")
		}
		if tlsRequired && !s.secureRequest(c.R) {
			return forbidden("this single-device invitation requires encrypted HTTPS")
		}
		res, err := s.db.Exec(`UPDATE enroll_tokens SET uses_left=uses_left-1 WHERE id=? AND uses_left>0`, id)
		if n, _ := res.RowsAffected(); err != nil || n == 0 {
			return errf(401, "invalid_enroll_token", "enrollment token is used up")
		}
		d.UserID, d.GroupID, d.Level = userID, groupID, level
		d.Status = "pending"
		if auto {
			d.Status = "active"
		}
	case req.Username != "":
		method = "user credentials"
		if !st.OpenRegistration {
			return forbidden("self-registration is closed; ask an administrator for an enrollment token")
		}
		u, err := s.db.UserByName(req.Username)
		hash := ""
		if err == nil {
			hash = u.PasswordHash
		}
		if !auth.CheckPassword(hash, req.Password) || u == nil || u.Disabled {
			s.loginRecord("user", "", req.Username, c, false, "bad credentials (device registration)", "password")
			return errf(401, "invalid_credentials", "invalid username or password")
		}
		if u.MFAEnabled && !auth.CheckTOTP(u.MFASecret, req.TOTP, time.Now()) {
			s.loginRecord("user", u.ID, u.Username, c, false, "bad TOTP (device registration)", "password+totp")
			return errf(401, "mfa_required", "valid authenticator code required")
		}
		d.UserID, d.Level = u.ID, st.DefaultLevel
		d.Status = "pending"
		if st.RegistrationApproval == "auto" {
			d.Status = "active"
		}
	default:
		return badRequest("provide enroll_token or username/password")
	}
	if err := s.db.CreateDevice(d); err != nil {
		return err
	}
	s.loginRecord("device", d.ID, d.Name, c, true, "registered via "+method, method)
	s.Audit(&Principal{Kind: "device", Device: d}, c.IP, "device.registered", "device", d.ID, map[string]string{"method": method, "status": d.Status})
	s.bus.Publish("device.registered", map[string]any{"id": d.ID, "name": d.Name, "status": d.Status})
	if d.Status == "pending" {
		s.raiseAlert("info", "device", "Device waiting for approval: "+d.Name, d.DID+" registered from "+c.IP, "registration", "pending:"+d.ID)
	}
	full, _ := s.db.DeviceByID(d.ID)
	return c.JSON(201, map[string]any{"device": full, "status": d.Status})
}

func trimTo(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (s *Server) hDeviceChallenge(c *Ctx) error {
	var req struct {
		DeviceID string `json:"device_id"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	if req.DeviceID == "" {
		return badRequest("device_id required")
	}
	nonce := auth.RandomHex(24)
	exp := time.Now().Add(2 * time.Minute)
	if _, err := s.db.Exec(`INSERT INTO device_challenges (nonce, device_id, expires_at) VALUES (?,?,?)`, nonce, req.DeviceID, exp.Unix()); err != nil {
		return err
	}
	_, _ = s.db.Exec(`DELETE FROM device_challenges WHERE expires_at < ?`, time.Now().Unix())
	return c.OK(map[string]any{"nonce": nonce, "expires_at": exp.UTC().Format(time.RFC3339), "message": string(auth.DeviceLoginMessage(req.DeviceID, nonce))})
}

func (s *Server) hDeviceLogin(c *Ctx) error {
	if err := s.loginGuard(c); err != nil {
		return err
	}
	var req struct {
		DeviceID  string `json:"device_id"`
		Nonce     string `json:"nonce"`
		Signature string `json:"signature"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	var did string
	var exp int64
	err := s.db.QueryRow(`SELECT device_id, expires_at FROM device_challenges WHERE nonce=?`, req.Nonce).Scan(&did, &exp)
	_, _ = s.db.Exec(`DELETE FROM device_challenges WHERE nonce=?`, req.Nonce)
	if err != nil || exp < time.Now().Unix() || did != req.DeviceID {
		return errf(401, "invalid_challenge", "challenge expired or unknown; request a new one")
	}
	d, err := s.db.DeviceByID(req.DeviceID)
	if errors.Is(err, store.ErrNotFound) {
		s.loginRecord("device", req.DeviceID, "", c, false, "unknown device", "signature")
		return errf(401, "invalid_signature", "device or signature invalid")
	}
	if err != nil {
		return err
	}
	pub, err := auth.ParsePublicKey(d.PubKey)
	if err != nil || !auth.VerifySignature(pub, auth.DeviceLoginMessage(d.ID, req.Nonce), req.Signature) {
		s.loginRecord("device", d.ID, d.Name, c, false, "bad signature", "signature")
		return errf(401, "invalid_signature", "device or signature invalid")
	}
	switch d.Status {
	case "pending":
		return errf(403, "device_pending", "device is waiting for administrator approval")
	case "disabled":
		s.loginRecord("device", d.ID, d.Name, c, false, "device disabled", "signature")
		return errf(403, "device_disabled", "device is disabled")
	case "banned":
		s.loginRecord("device", d.ID, d.Name, c, false, "device banned", "signature")
		return errf(403, "device_banned", "device is banned")
	}
	tok, h := auth.NewToken("mpd")
	hours := s.db.Settings().DeviceTokenHours
	expAt := time.Now().Add(time.Duration(hours) * time.Hour)
	if _, err := s.db.Exec(`INSERT INTO device_tokens (id, device_id, token_hash, ip, created_at, expires_at) VALUES (?,?,?,?,?,?)`,
		store.NewID("dt"), d.ID, h, c.IP, store.Now(), expAt.Unix()); err != nil {
		return err
	}
	_, _ = s.db.Exec(`DELETE FROM device_tokens WHERE device_id=? AND expires_at < ?`, d.ID, time.Now().Unix())
	_, _ = s.db.Exec(`UPDATE devices SET last_ip=? WHERE id=?`, c.IP, d.ID)
	s.loginRecord("device", d.ID, d.Name, c, true, "", "signature")
	return c.OK(map[string]any{"token": tok, "expires_at": expAt.UTC().Format(time.RFC3339), "device": d,
		"key_rotate_required": d.KeyRotateRequired, "capabilities": levelCaps(d.Level)})
}

func levelCaps(level string) []string {
	for _, l := range policy.LevelDefs() {
		if l.Level == level {
			return l.Capabilities
		}
	}
	return nil
}
