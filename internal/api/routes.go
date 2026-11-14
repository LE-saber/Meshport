package api

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
)

func (s *Server) registerRoutes() {
	const v = "/api/v1"
	A := s.add

	// ---- health & docs
	A("GET", "/healthz", AuthPublic, "system", "Liveness probe", func(c *Ctx) error { return c.OK(map[string]string{"status": "ok", "version": Version}) })
	A("GET", v+"/openapi.json", AuthPublic, "system", "OpenAPI 3.1 description of this API", s.hOpenAPI)
	A("GET", "/api/docs", AuthPublic, "system", "Human-readable API reference", s.hDocsHTML)
	A("GET", "/metrics", AuthViewer, "monitor", "Prometheus metrics (use an API token with read scope)", s.hPrometheus)

	// ---- console authentication
	A("POST", v+"/auth/login", AuthPublic, "auth", "Console login (password, plus TOTP when enabled)", s.hLogin, "{username, password, totp?}")
	A("POST", v+"/auth/logout", AuthSession, "auth", "End the current console session", s.hLogout)
	A("GET", v+"/auth/me", AuthSession, "auth", "Current principal and console preferences", s.hMe)
	A("POST", v+"/auth/password", AuthSession, "auth", "Change own password (signs out other sessions)", s.hChangePassword, "{current, new}")
	A("POST", v+"/auth/mfa/setup", AuthSession, "auth", "Start TOTP enrollment; returns secret and otpauth URI", s.hMFASetup)
	A("POST", v+"/auth/mfa/enable", AuthSession, "auth", "Confirm TOTP enrollment with a code", s.hMFAEnable, "{code}")
	A("POST", v+"/auth/mfa/disable", AuthSession, "auth", "Disable TOTP (requires a current code)", s.hMFADisable, "{code}")

	// ---- device registration & login (desktop / mobile clients)
	A("POST", v+"/devices/register", AuthPublic, "device", "Register a device with an ed25519 identity key", s.hRegister,
		"{name, os, kind, pubkey, box_pubkey?, ts, sig, enroll_token | username+password(+totp), lan_ip?, client_version?}")
	A("POST", v+"/devices/auth/challenge", AuthPublic, "device", "Get a nonce to sign for device login", s.hDeviceChallenge, "{device_id}")
	A("POST", v+"/devices/auth/login", AuthPublic, "device", "Exchange a signed nonce for a device token", s.hDeviceLogin, "{device_id, nonce, signature}")

	// ---- device self-service
	A("GET", v+"/ws", AuthDevice, "device", "Device WebSocket: presence, relay, signalling, telemetry, commands (see pkg/proto)", s.hDeviceWS)
	A("GET", v+"/me/device", AuthDevice, "device", "This device", s.hMyDevice)
	A("PATCH", v+"/me/device", AuthDevice, "device", "Update name / OS / LAN IP / NAT type / client version / box key", s.hUpdateMyDevice, "{name?, os?, lan_ip?, nat_type?, client_version?, box_pubkey?}")
	A("POST", v+"/me/device/rotate-key", AuthDevice, "device", "Rotate the identity key (signed by old and new key)", s.hRotateKey, "{new_pubkey, old_sig, new_sig}")
	A("GET", v+"/peers", AuthDevice, "device", "Discover devices this device may see and message", s.hPeers)
	A("POST", v+"/messages", AuthDevice, "messaging", "Relay a message over REST (same rules as msg.send)", s.hSendMessage, "{to, mode: relay|auto, cid, ct, body(base64 ciphertext), sid?}")
	A("GET", v+"/messages", AuthDevice, "messaging", "Fetch relay messages queued for this device", s.hQueuedMessages)
	A("POST", v+"/messages/receipts", AuthDevice, "messaging", "Acknowledge messages as delivered or read", s.hReceipts, "{ids[], kind: delivered|read}")
	A("GET", v+"/rtc/ice-servers", AuthDevice, "messaging", "STUN/TURN servers with short-lived TURN credentials", s.hICEServers)
	A("GET", v+"/files", AuthDevice, "messaging", "Relay files sent or received by this device", s.hListFiles)
	A("POST", v+"/files", AuthDevice, "messaging", "Create a resumable relay upload", s.hCreateFile, "{to, name, size, sha256}")
	A("GET", v+"/files/{id}", AuthDevice, "messaging", "Upload/download status (Upload-Offset header)", s.hFileInfo)
	A("PATCH", v+"/files/{id}", AuthDevice, "messaging", "Append a chunk (header Upload-Offset, raw body)", s.hUploadChunk)
	A("GET", v+"/files/{id}/content", AuthDevice, "messaging", "Download a relay file (supports Range)", s.hDownloadFile)
	A("DELETE", v+"/files/{id}", AuthDevice, "messaging", "Delete a relay file", s.hDeleteFile)
	A("GET", v+"/frp/nodes", AuthAny, "device", "frps nodes available for publishing services", s.hNodesPublic)
	A("GET", v+"/me/mappings", AuthDevice, "device", "This device's FRP mappings", s.hMyMappings)
	A("POST", v+"/me/mappings", AuthDevice, "device", "Create a mapping (member level or higher)", s.hCreateMyMapping, mappingBody)
	A("PATCH", v+"/me/mappings/{id}", AuthDevice, "device", "Edit a mapping", s.hUpdateMyMapping, mappingBody)
	A("DELETE", v+"/me/mappings/{id}", AuthDevice, "device", "Delete a mapping", s.hDeleteMyMapping)
	A("POST", v+"/me/mappings/{id}/start", AuthDevice, "device", "Start a mapping", s.hToggleMyMapping(true))
	A("POST", v+"/me/mappings/{id}/stop", AuthDevice, "device", "Stop a mapping", s.hToggleMyMapping(false))
	A("GET", v+"/me/frpc", AuthDevice, "device", "Generated frpc.toml per node (run one frpc per node)", s.hMyFRPC)
	A("GET", v+"/me/frpc/{node}", AuthDevice, "device", "Generated frpc.toml for one node (text/toml)", s.hMyFRPCNode)
	A("GET", v+"/me/grants", AuthDevice, "access", "Level requests made by or for this device", s.hMyGrants)
	A("POST", v+"/grants", AuthDevice, "access", "Request a level for another device (admin-level devices)", s.hRequestGrant, "{target, level, reason, duration: 24h|7d|30d|90d|perm}")

	// ---- console: overview & devices
	const ad = v + "/admin"
	A("GET", ad+"/overview", AuthViewer, "overview", "Dashboard: devices, sessions, bandwidth, FRP, resources, pending, alerts", s.hOverview)
	A("GET", ad+"/stream", AuthViewer, "monitor", "Console live event WebSocket (?events=filter)", s.hStream)
	A("GET", ad+"/devices", AuthViewer, "devices", "List devices (?q, state=online|offline|disabled|pending|banned, group, user, level)", s.hAdminDevices)
	A("GET", ad+"/devices/{id}", AuthViewer, "devices", "Device detail with sessions, mappings, grants, logins", s.hAdminDevice)
	A("PATCH", ad+"/devices/{id}", AuthOperator, "devices", "Edit name, kind, level, group, owner", s.hAdminUpdateDevice, "{name?, kind?, level?, group?, user?}")
	A("DELETE", ad+"/devices/{id}", AuthOperator, "devices", "Delete a device", s.hDeleteDevice)
	A("POST", ad+"/devices/{id}/approve", AuthOperator, "devices", "Approve a pending registration", s.hApproveDevice)
	A("POST", ad+"/devices/{id}/reject", AuthOperator, "devices", "Reject (delete) a pending registration", s.hRejectDevice)
	A("POST", ad+"/devices/{id}/disable", AuthOperator, "devices", "Disable a device (reversible)", s.hDisableDevice)
	A("POST", ad+"/devices/{id}/enable", AuthOperator, "devices", "Re-enable a disabled device", s.hEnableDevice)
	A("POST", ad+"/devices/{id}/ban", AuthOperator, "security", "Ban a device", s.hBanDevice, "{reason?}")
	A("POST", ad+"/devices/{id}/unban", AuthOperator, "security", "Lift a ban", s.hUnbanDevice)
	A("POST", ad+"/devices/{id}/rotate-key", AuthOperator, "security", "Require the device to rotate its key", s.hRequireKeyRotation)
	A("POST", ad+"/devices/{id}/command", AuthOperator, "devices", "Send a remote-management command and wait for the result", s.hDeviceCommand, "{name, args?, timeout_s?}")
	A("GET", ad+"/groups", AuthViewer, "devices", "Device groups", s.hGroups)
	A("POST", ad+"/groups", AuthOperator, "devices", "Create a group", s.hCreateGroup, "{name, description?}")
	A("PATCH", ad+"/groups/{id}", AuthOperator, "devices", "Rename / describe a group", s.hUpdateGroup, "{name?, description?}")
	A("DELETE", ad+"/groups/{id}", AuthOperator, "devices", "Delete a group", s.hDeleteGroup)
	A("GET", ad+"/enroll-tokens", AuthSession, "devices", "Active enrollment tokens", s.hEnrollTokens)
	A("POST", ad+"/enroll-tokens", AuthSession, "devices", "Create an enrollment token (\"register new device\")", s.hCreateEnrollToken, "{note?, user?, group?, level?, auto_approve?, uses?, ttl_hours?}")
	A("DELETE", ad+"/enroll-tokens/{id}", AuthOperator, "devices", "Delete an enrollment token", s.hDeleteEnrollToken)

	// ---- console: communication
	A("GET", ad+"/sessions", AuthViewer, "comms", "Communication sessions (?mode, path, state, device, q, include_closed)", s.hSessions)
	A("GET", ad+"/sessions/{id}", AuthViewer, "comms", "One session", s.hSession)
	A("POST", ad+"/sessions/{id}/disconnect", AuthOperator, "comms", "Ask both devices to close the session", s.hDisconnectSession)

	// ---- console: FRP
	A("GET", ad+"/frp/nodes", AuthViewer, "frp", "frps nodes with live stats", s.hNodes)
	A("POST", ad+"/frp/nodes", AuthAdmin, "frp", "Add an frps node (managed locally or remote)", s.hCreateNode, nodeBody)
	A("PATCH", ad+"/frp/nodes/{id}", AuthAdmin, "frp", "Edit an frps node", s.hUpdateNode, nodeBody)
	A("DELETE", ad+"/frp/nodes/{id}", AuthAdmin, "frp", "Remove an frps node and its mappings", s.hDeleteNode)
	A("POST", ad+"/frp/nodes/{id}/restart", AuthOperator, "frp", "Restart a managed frps", s.hRestartNode)
	A("GET", ad+"/frp/nodes/{id}/config", AuthAdmin, "frp", "frps.toml for this node (includes the plugin hook)", s.hNodeConfig)
	A("GET", ad+"/frp/clients", AuthViewer, "frp", "Connected frpc clients", s.hFRPClients)
	A("GET", ad+"/frp/mappings", AuthViewer, "frp", "All mappings (?node, device, proto, state, q)", s.hMappings)
	A("POST", ad+"/frp/mappings", AuthOperator, "frp", "Create a mapping for a device", s.hCreateMapping, "{device, "+strings.TrimPrefix(mappingBody, "{"))
	A("PATCH", ad+"/frp/mappings/{id}", AuthOperator, "frp", "Edit a mapping", s.hUpdateMapping, mappingBody)
	A("DELETE", ad+"/frp/mappings/{id}", AuthOperator, "frp", "Delete a mapping", s.hDeleteMapping)
	A("POST", ad+"/frp/mappings/{id}/start", AuthOperator, "frp", "Start a mapping", s.hToggleMapping(true))
	A("POST", ad+"/frp/mappings/{id}/stop", AuthOperator, "frp", "Stop a mapping (enforced on frps)", s.hToggleMapping(false))
	A("POST", ad+"/frp/stop-all", AuthOperator, "frp", "Stop every mapping (optionally on one node)", s.hStopAll, "{node?}")

	// ---- console: access & approvals
	A("GET", ad+"/grants", AuthViewer, "access", "Level requests (?status)", s.hGrants)
	A("POST", ad+"/grants", AuthOperator, "access", "Grant a level directly (optionally temporary)", s.hCreateGrant, "{target, level, reason?, duration?}")
	A("POST", ad+"/grants/{id}/approve", AuthOperator, "access", "Approve a request (applies the level)", s.hApproveGrant, "{duration?, note?}")
	A("POST", ad+"/grants/{id}/reject", AuthOperator, "access", "Reject a request", s.hRejectGrant, "{note?}")
	A("POST", ad+"/grants/{id}/revoke", AuthOperator, "access", "Revoke an approved grant (restores previous level)", s.hRevokeGrant)
	A("GET", ad+"/levels", AuthViewer, "access", "Level definitions with device counts", s.hLevels)
	A("GET", ad+"/users", AuthViewer, "access", "Users with roles, MFA and device levels", s.hUsers)
	A("POST", ad+"/users", AuthAdmin, "access", "Create a user (password generated if omitted)", s.hCreateUser, "{username, password?, role, display_name?, email?}")
	A("PATCH", ad+"/users/{id}", AuthAdmin, "access", "Edit role, disable, reset password", s.hUpdateUser, "{role?, disabled?, password?, display_name?, email?}")
	A("DELETE", ad+"/users/{id}", AuthAdmin, "access", "Delete a user", s.hDeleteUser)
	A("POST", ad+"/users/{id}/reset-mfa", AuthAdmin, "security", "Remove a user's TOTP enrollment", s.hResetUserMFA)

	// ---- console: security
	A("GET", ad+"/security/logins", AuthViewer, "security", "Login records (?abnormal=1, failed=1, kind, q)", s.hLogins)
	A("POST", ad+"/security/unblock", AuthOperator, "security", "Lift a temporary brute-force block", s.hUnblockIP, "{ip}")
	A("GET", ad+"/security/keys", AuthViewer, "security", "Device key status and age", s.hKeys)
	A("GET", ad+"/security/mfa", AuthViewer, "security", "MFA policy and enrollment", s.hMFAPolicy)
	A("PUT", ad+"/security/mfa", AuthAdmin, "security", "Require MFA for console users", s.hSetMFAPolicy, "{required}")
	A("POST", ad+"/security/mfa/remind", AuthOperator, "security", "Remind users without MFA (emits user.mfa_reminder)", s.hRemindMFA, "{users?[]}")
	A("GET", ad+"/security/tokens", AuthSession, "security", "Personal API tokens", s.hTokens("token"))
	A("POST", ad+"/security/tokens", AuthSession, "security", "Create a personal API token", s.hCreateToken("token"), "{name, scopes[read|write|admin], ttl_days?}")
	A("DELETE", ad+"/security/tokens/{id}", AuthSession, "security", "Revoke an API token or key", s.hRevokeToken)
	A("GET", ad+"/security/acl", AuthViewer, "security", "Allow / deny lists", s.hACL)
	A("POST", ad+"/security/acl", AuthAdmin, "security", "Add an allow or deny rule", s.hAddACL, "{action: allow|deny, cidr, note?}")
	A("DELETE", ad+"/security/acl/{id}", AuthAdmin, "security", "Remove a rule", s.hDeleteACL)
	A("GET", ad+"/security/bans", AuthViewer, "security", "Banned devices", s.hBans)
	A("GET", ad+"/alerts", AuthViewer, "security", "Alerts (?status, category, severity)", s.hAlerts)
	A("POST", ad+"/alerts/{id}/ack", AuthOperator, "security", "Acknowledge an alert", s.hAlertStatus("acked"))
	A("POST", ad+"/alerts/{id}/resolve", AuthOperator, "security", "Resolve an alert", s.hAlertStatus("resolved"))

	// ---- console: monitoring & logs
	A("GET", ad+"/metrics", AuthViewer, "monitor", "Time series (?range=live|5m|1h|24h|7d, keys, points)", s.hMetrics)
	A("GET", ad+"/metrics/p2p", AuthViewer, "monitor", "P2P success rate per hour", s.hP2PStats)
	A("GET", ad+"/failures", AuthViewer, "monitor", "Connection failures (?kind=p2p|relay|frp|auth)", s.hFailures)
	A("GET", ad+"/logs", AuthViewer, "monitor", "Service logs (?level, source, q, before, limit)", s.hLogs)
	A("GET", ad+"/audit", AuthViewer, "monitor", "Audit trail of every change", s.hAudit)

	// ---- console: system & API
	A("GET", ad+"/system/status", AuthViewer, "system", "Service status", s.hSystemStatus)
	A("POST", ad+"/system/services/{name}/restart", AuthAdmin, "system", "Restart hub or a managed frps (frps:<node>)", s.hRestartService)
	A("GET", ad+"/system/versions", AuthViewer, "system", "Component versions", s.hVersions)
	A("GET", ad+"/system/db", AuthViewer, "system", "Database status (?check=1 runs quick_check)", s.hDBStatus)
	A("GET", ad+"/system/backups", AuthViewer, "system", "Backups and schedule", s.hBackups)
	A("POST", ad+"/system/backups", AuthAdmin, "system", "Back up now", s.hCreateBackup)
	A("GET", ad+"/system/backups/{id}/download", AuthAdmin, "system", "Download a backup", s.hDownloadBackup)
	A("DELETE", ad+"/system/backups/{id}", AuthAdmin, "system", "Delete a backup", s.hDeleteBackup)
	A("GET", ad+"/system/webhooks", AuthViewer, "system", "Webhooks", s.hWebhooks)
	A("POST", ad+"/system/webhooks", AuthAdmin, "system", "Create a webhook (returns its signing secret)", s.hCreateWebhook, "{url, name?, events?[], enabled?}")
	A("PATCH", ad+"/system/webhooks/{id}", AuthAdmin, "system", "Edit / enable / disable a webhook", s.hUpdateWebhook, "{url?, name?, events?, enabled?}")
	A("DELETE", ad+"/system/webhooks/{id}", AuthAdmin, "system", "Delete a webhook", s.hDeleteWebhook)
	A("POST", ad+"/system/webhooks/{id}/test", AuthAdmin, "system", "Send a test event", s.hTestWebhook)
	A("GET", ad+"/system/api-keys", AuthAdmin, "system", "Service API keys", s.hTokens("key"))
	A("POST", ad+"/system/api-keys", AuthAdmin, "system", "Create a service API key", s.hCreateToken("key"), "{name, scopes[], ttl_days?}")
	A("DELETE", ad+"/system/api-keys/{id}", AuthAdmin, "system", "Revoke a service API key", s.hRevokeToken)
	A("GET", ad+"/system/update", AuthViewer, "system", "Check for updates on the configured channel", s.hUpdateCheck)
	A("POST", ad+"/system/update/install", AuthAdmin, "system", "Install a signed update and restart", s.hUpdateInstall)
	A("GET", ad+"/system/settings", AuthViewer, "system", "Runtime settings", s.hSettings)
	A("PATCH", ad+"/system/settings", AuthAdmin, "system", "Change runtime settings (partial)", s.hUpdateSettings, "{console_name?, open_registration?, registration_approval?, ...}")
	A("GET", ad+"/system/endpoints", AuthViewer, "system", "Route table (for the API docs card)", s.hEndpoints)

	// ---- frps plugin (also reachable on the internal listener)
	s.mux.HandleFunc("POST /internal/frps-plugin/{node}/{token}", s.frp.PluginHandler)

	// ---- static console (optional)
	s.mux.HandleFunc("GET /", s.hStatic)
}

const mappingBody = "{name, node, proto: tcp|udp|http|https|stcp|xtcp, local_ip?, local_port, remote_port?, domains?[], subdomain?, secret_key?, use_encryption?, use_compression?, bandwidth_limit?, enabled?}"
const nodeBody = "{name, region?, public_addr, bind_port, vhost_http_port?, vhost_https_port?, subdomain_host?, allow_ports?, dashboard_addr?, dashboard_user?, dashboard_password?, managed?, enabled?}"

func (s *Server) hEndpoints(c *Ctx) error {
	type ep struct {
		Method  string `json:"method"`
		Path    string `json:"path"`
		Auth    string `json:"auth"`
		Tag     string `json:"tag"`
		Summary string `json:"summary"`
	}
	out := []ep{}
	for _, r := range s.routes {
		out = append(out, ep{r.Method, r.Path, r.Auth, r.Tag, r.Summary})
	}
	return c.Items(out)
}

// hStatic serves the web console: the build in web_dir when configured,
// otherwise the console embedded in the binary. web_dir "off" disables it.
// Unknown paths fall back to index.html (client-side routing).
func (s *Server) hStatic(w http.ResponseWriter, r *http.Request) {
	if s.acl.Denied(s.clientIP(r)) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/internal/") {
		writeErr(w, notFoundErr("endpoint"))
		return
	}
	if s.cfg.WebDir == "off" {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"name":"meshportd","version":%q,"docs":"/api/docs","openapi":"/api/v1/openapi.json"}`+"\n", Version)
		return
	}
	var fsys fs.FS = s.consoleFS
	if s.cfg.WebDir != "" {
		fsys = os.DirFS(s.cfg.WebDir)
	}
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("X-Frame-Options", "DENY")
	if s.cfg.WebDir == "" {
		// the embedded console uses no inline scripts or third-party resources
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self' ws: wss:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	if st, err := fs.Stat(fsys, name); err != nil || st.IsDir() {
		name = "index.html"
	}
	if name == "index.html" {
		h.Set("Cache-Control", "no-cache")
	}
	f, err := fsys.Open(name)
	if err != nil {
		http.Error(w, "console not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		b, _ := io.ReadAll(f)
		rs = bytes.NewReader(b)
	}
	http.ServeContent(w, r, name, st.ModTime(), rs)
}

// hPrometheus exposes the latest gauges in Prometheus text format.
func (s *Server) hPrometheus(c *Ctx) error {
	latest := s.mon.Latest()
	keys := make([]string, 0, len(latest))
	for k := range latest {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	c.W.Header().Set("Content-Type", "text/plain; version=0.0.4")
	for _, k := range keys {
		fmt.Fprintf(c.W, "# TYPE meshport_%s gauge\nmeshport_%s %g\n", k, k, latest[k])
	}
	fmt.Fprintf(c.W, "# TYPE meshport_open_alerts gauge\nmeshport_open_alerts %d\n", s.db.Count(`SELECT COUNT(*) FROM alerts WHERE status='open'`))
	fmt.Fprintf(c.W, "# TYPE meshport_pending_grants gauge\nmeshport_pending_grants %d\n", s.db.Count(`SELECT COUNT(*) FROM grants WHERE status='pending'`))
	return nil
}
