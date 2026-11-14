package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"meshport/internal/auth"
	"meshport/internal/policy"
	"meshport/internal/store"
)

// Auth requirements a route can declare.
const (
	AuthPublic   = "public"
	AuthDevice   = "device"
	AuthViewer   = "viewer"
	AuthOperator = "operator"
	AuthAdmin    = "admin"
	AuthAny      = "any"     // a device or a console viewer
	AuthSession  = "session" // any logged-in console user, even role "user"
)

type Route struct {
	Method  string
	Path    string
	Auth    string
	Tag     string
	Summary string
	Body    string // short description of the JSON request body
	h       func(*Ctx) error
}

// Principal is the authenticated caller.
type Principal struct {
	Kind    string        `json:"kind"` // user | device | token
	User    *store.User   `json:"user,omitempty"`
	Device  *store.Device `json:"device,omitempty"`
	Role    string        `json:"role"`
	TokenID string        `json:"token_id,omitempty"`
	Session string        `json:"-"`
}

func (p *Principal) Name() string {
	switch {
	case p == nil:
		return ""
	case p.Device != nil:
		return p.Device.Name
	case p.User != nil:
		return p.User.Username
	}
	return ""
}

func (p *Principal) ID() string {
	switch {
	case p == nil:
		return ""
	case p.Device != nil:
		return p.Device.ID
	case p.User != nil:
		return p.User.ID
	}
	return ""
}

type Ctx struct {
	W  http.ResponseWriter
	R  *http.Request
	S  *Server
	P  *Principal
	IP string
}

// ---------- errors ----------

type Error struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

func errf(status int, code, format string, a ...any) *Error {
	return &Error{Status: status, Code: code, Message: fmt.Sprintf(format, a...)}
}

func badRequest(format string, a ...any) *Error { return errf(400, "bad_request", format, a...) }
func notFoundErr(what string) *Error            { return errf(404, "not_found", "%s not found", what) }
func forbidden(format string, a ...any) *Error  { return errf(403, "forbidden", format, a...) }
func conflict(format string, a ...any) *Error   { return errf(409, "conflict", format, a...) }

// ---------- helpers ----------

func (c *Ctx) JSON(status int, v any) error {
	c.W.Header().Set("Content-Type", "application/json; charset=utf-8")
	c.W.WriteHeader(status)
	if v == nil {
		return nil
	}
	enc := json.NewEncoder(c.W)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func (c *Ctx) OK(v any) error { return c.JSON(200, v) }

func (c *Ctx) Items(items any) error { return c.JSON(200, map[string]any{"items": items}) }

func (c *Ctx) Done() error { return c.JSON(200, map[string]bool{"ok": true}) }

func (c *Ctx) Bind(v any) error {
	dec := json.NewDecoder(io.LimitReader(c.R.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return badRequest("request body required")
		}
		return badRequest("invalid JSON body: %v", err)
	}
	return nil
}

func (c *Ctx) Param(name string) string { return c.R.PathValue(name) }
func (c *Ctx) Query(name string) string { return strings.TrimSpace(c.R.URL.Query().Get(name)) }

func (c *Ctx) QueryInt(name string, def, max int) int {
	v, err := strconv.Atoi(c.Query(name))
	if err != nil || v <= 0 {
		return def
	}
	if v > max {
		return max
	}
	return v
}

// Audit records a mutation by the current principal.
func (c *Ctx) Audit(action, targetType, targetID string, detail any) {
	c.S.Audit(c.P, c.IP, action, targetType, targetID, detail)
}

// ---------- routing ----------

func (s *Server) add(method, path, authReq, tag, summary string, h func(*Ctx) error, body ...string) {
	r := Route{Method: method, Path: path, Auth: authReq, Tag: tag, Summary: summary, h: h}
	if len(body) > 0 {
		r.Body = body[0]
	}
	s.routes = append(s.routes, r)
	s.mux.HandleFunc(method+" "+path, s.wrap(r))
}

func (s *Server) wrap(rt Route) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		c := &Ctx{W: w, R: r, S: s, IP: s.clientIP(r)}
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic in handler", "path", r.URL.Path, "panic", rec, "stack", string(debug.Stack()))
				writeErr(w, errf(500, "internal", "internal error"))
			}
		}()
		if s.acl.Denied(c.IP) {
			writeErr(w, forbidden("your address is blocked"))
			return
		}
		if rt.Auth != AuthPublic {
			p, err := s.authenticate(c)
			if err != nil {
				writeErr(w, err)
				return
			}
			c.P = p
			if err := s.authorize(c, rt); err != nil {
				writeErr(w, err)
				return
			}
		}
		if err := rt.h(c); err != nil {
			var e *Error
			if errors.As(err, &e) {
				writeErr(w, e)
			} else if errors.Is(err, store.ErrNotFound) {
				writeErr(w, notFoundErr("resource"))
			} else {
				s.log.Error("handler error", "method", r.Method, "path", r.URL.Path, "err", err)
				writeErr(w, errf(500, "internal", "internal error"))
			}
		}
		s.log.Debug("request", "method", r.Method, "path", r.URL.Path, "ip", c.IP, "dur", time.Since(start).String())
	}
}

func writeErr(w http.ResponseWriter, e *Error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if e.Status == 401 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="meshport"`)
	}
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(map[string]*Error{"error": e})
}

// ---------- authentication ----------

const sessionCookie = "meshport_session"

func bearer(r *http.Request) (string, bool) {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(h[7:]), false
	}
	// WebSocket clients in browsers cannot set headers.
	if t := r.URL.Query().Get("access_token"); t != "" && (strings.HasSuffix(r.URL.Path, "/ws") || strings.HasSuffix(r.URL.Path, "/stream")) {
		return t, false
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		return c.Value, true
	}
	return "", false
}

func (s *Server) authenticate(c *Ctx) (*Principal, *Error) {
	tok, fromCookie := bearer(c.R)
	if tok == "" {
		return nil, errf(401, "unauthorized", "authentication required")
	}
	if fromCookie && c.R.Method != "GET" && c.R.Method != "HEAD" && c.R.Header.Get("X-Requested-With") == "" {
		return nil, forbidden("cookie-authenticated requests must send X-Requested-With")
	}
	now := time.Now().Unix()
	h := auth.HashToken(tok)
	prefix, _, _ := strings.Cut(tok, "_")
	switch prefix {
	case "mps": // console session
		var sid, uid string
		var exp int64
		err := s.db.QueryRow(`SELECT id, user_id, expires_at FROM web_sessions WHERE token_hash=?`, h).Scan(&sid, &uid, &exp)
		if err != nil || exp < now {
			return nil, errf(401, "unauthorized", "session expired")
		}
		u, err := s.db.UserByID(uid)
		if err != nil || u.Disabled {
			return nil, errf(401, "unauthorized", "account disabled")
		}
		_, _ = s.db.Exec(`UPDATE web_sessions SET last_seen_at=? WHERE id=?`, now, sid)
		return &Principal{Kind: "user", User: u, Role: u.Role, Session: sid}, nil
	case "mpd": // device token
		var did string
		var exp int64
		err := s.db.QueryRow(`SELECT device_id, expires_at FROM device_tokens WHERE token_hash=?`, h).Scan(&did, &exp)
		if err != nil || exp < now {
			return nil, errf(401, "unauthorized", "device token expired; log in again")
		}
		d, err := s.db.DeviceByID(did)
		if err != nil {
			return nil, errf(401, "unauthorized", "device not found")
		}
		if !policy.Active(d) {
			return nil, errf(403, "device_"+d.Status, "device is %s", d.Status)
		}
		d.Online = s.hub.Online(d.ID)
		return &Principal{Kind: "device", Device: d, Role: ""}, nil
	case "mpt", "mpk": // API tokens and service keys
		var id, uid, scopesRaw string
		var exp, revoked int64
		err := s.db.QueryRow(`SELECT id, COALESCE(user_id,''), scopes, expires_at, revoked_at FROM api_tokens WHERE token_hash=?`, h).Scan(&id, &uid, &scopesRaw, &exp, &revoked)
		if err != nil || revoked > 0 || (exp > 0 && exp < now) {
			s.loginRecord("api", "", "", c, false, "invalid or revoked API token", "token")
			return nil, errf(401, "unauthorized", "invalid or revoked API token")
		}
		u, err := s.db.UserByID(uid)
		if err != nil || u.Disabled {
			return nil, errf(401, "unauthorized", "token owner disabled")
		}
		var scopes []string
		_ = json.Unmarshal([]byte(scopesRaw), &scopes)
		role := scopeRole(scopes)
		if policy.RoleRank(role) > policy.RoleRank(u.Role) {
			role = u.Role
		}
		_, _ = s.db.Exec(`UPDATE api_tokens SET last_used_at=?, last_used_ip=? WHERE id=?`, now, c.IP, id)
		return &Principal{Kind: "token", User: u, Role: role, TokenID: id}, nil
	}
	return nil, errf(401, "unauthorized", "unrecognised credential")
}

func scopeRole(scopes []string) string {
	role := policy.RoleUser
	for _, sc := range scopes {
		r := map[string]string{"read": policy.RoleViewer, "write": policy.RoleOperator, "admin": policy.RoleAdmin}[sc]
		if policy.RoleRank(r) > policy.RoleRank(role) {
			role = r
		}
	}
	return role
}

func (s *Server) authorize(c *Ctx, rt Route) *Error {
	p := c.P
	switch rt.Auth {
	case AuthDevice:
		if p.Kind != "device" {
			return forbidden("this endpoint is for devices")
		}
		return nil
	case AuthAny:
		if p.Kind == "device" || policy.RoleAtLeast(p.Role, policy.RoleViewer) {
			return s.consoleNetCheck(c)
		}
		return forbidden("console access required")
	case AuthSession:
		if p.Kind == "device" {
			return forbidden("this endpoint is for console users")
		}
		return nil
	case AuthViewer, AuthOperator, AuthAdmin:
		if p.Kind == "device" {
			// Admin-level devices may use the FRP management endpoints (client "Servers" tab).
			if rt.Tag == "frp" && policy.CanManageFRP(p.Device) && rt.Auth != AuthAdmin {
				return nil
			}
			return forbidden("console access required")
		}
		if !policy.RoleAtLeast(p.Role, rt.Auth) {
			return forbidden("requires the %s role", rt.Auth)
		}
		if p.Kind == "user" && s.db.Settings().MFARequired && !p.User.MFAEnabled && !mfaExempt(rt.Path) {
			return errf(403, "mfa_required", "multi-factor authentication must be enabled before using the console")
		}
		return s.consoleNetCheck(c)
	}
	return nil
}

func mfaExempt(path string) bool {
	return strings.HasPrefix(path, "/api/v1/auth/")
}

// consoleNetCheck applies the allow list (if any) to console/API users.
func (s *Server) consoleNetCheck(c *Ctx) *Error {
	if c.P.Kind == "device" {
		return nil
	}
	if !s.acl.AllowedConsole(c.IP) {
		return forbidden("console access is not allowed from %s", c.IP)
	}
	return nil
}

// ---------- client IP ----------

func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil || !s.trusted(ip) {
		return host
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			cand := strings.TrimSpace(parts[i])
			cip := net.ParseIP(cand)
			if cip == nil {
				continue
			}
			if !s.trusted(cip) || i == 0 {
				return cand
			}
		}
	}
	if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); net.ParseIP(xr) != nil {
		return xr
	}
	return host
}

func (s *Server) secureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && s.trusted(ip) && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (s *Server) trusted(ip net.IP) bool {
	for _, n := range s.trustedNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ---------- audit ----------

func (s *Server) Audit(p *Principal, ip, action, targetType, targetID string, detail any) {
	actorType, actorID, actor := "system", "", "system"
	if p != nil {
		actorType, actorID, actor = p.Kind, p.ID(), p.Name()
	}
	b, _ := json.Marshal(detail)
	if detail == nil {
		b = []byte("{}")
	}
	_, err := s.db.Exec(`INSERT INTO audit_log (actor_type, actor_id, actor, action, target_type, target_id, detail, ip, created_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		actorType, actorID, actor, action, targetType, targetID, string(b), ip, store.Now())
	if err != nil {
		s.log.Error("audit write failed", "err", err)
	}
	s.bus.Publish("audit", map[string]any{"actor": actor, "action": action, "target_type": targetType, "target_id": targetID})
	s.log.Info("audit", "actor", actor, "action", action, "target", targetType+":"+targetID, slog.String("src", "audit"))
}
