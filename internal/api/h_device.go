package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"meshport/internal/auth"
	"meshport/internal/frp"
	"meshport/internal/hub"
	"meshport/internal/policy"
	"meshport/internal/store"
	"meshport/pkg/proto"
)

// ---------- own device ----------

func (s *Server) hMyDevice(c *Ctx) error {
	return c.OK(map[string]any{"device": c.P.Device, "capabilities": levelCaps(c.P.Device.Level)})
}

func (s *Server) hUpdateMyDevice(c *Ctx) error {
	var req struct {
		Name          *string `json:"name"`
		OS            *string `json:"os"`
		LanIP         *string `json:"lan_ip"`
		NATType       *string `json:"nat_type"`
		ClientVersion *string `json:"client_version"`
		BoxPubKey     *string `json:"box_pubkey"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	d := c.P.Device
	if req.Name != nil {
		n := strings.TrimSpace(*req.Name)
		if n == "" || len(n) > 64 {
			return badRequest("name must be 1-64 characters")
		}
		d.Name = n
	}
	if req.BoxPubKey != nil {
		if err := auth.ParseBoxKey(*req.BoxPubKey); err != nil {
			return badRequest("%v", err)
		}
		d.BoxPubKey = *req.BoxPubKey
	}
	set := func(dst *string, v *string, n int) {
		if v != nil {
			*dst = trimTo(*v, n)
		}
	}
	set(&d.OS, req.OS, 64)
	set(&d.LanIP, req.LanIP, 64)
	set(&d.NATType, req.NATType, 32)
	set(&d.ClientVersion, req.ClientVersion, 32)
	_, err := s.db.Exec(`UPDATE devices SET name=?, os=?, lan_ip=?, nat_type=?, client_version=?, box_pubkey=? WHERE id=?`, d.Name, d.OS, d.LanIP, d.NATType, d.ClientVersion, d.BoxPubKey, d.ID)
	if err != nil {
		return err
	}
	s.hub.RefreshDevice(d.ID)
	return c.OK(map[string]any{"device": d})
}

func (s *Server) hRotateKey(c *Ctx) error {
	var req struct {
		NewPubKey string `json:"new_pubkey"`
		OldSig    string `json:"old_sig"` // signature by the current key over RotateMessage
		NewSig    string `json:"new_sig"` // signature by the new key over RotateMessage
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	d := c.P.Device
	oldPub, err := auth.ParsePublicKey(d.PubKey)
	if err != nil {
		return err
	}
	newPub, err := auth.ParsePublicKey(req.NewPubKey)
	if err != nil {
		return badRequest("%v", err)
	}
	msg := auth.RotateMessage(d.ID, req.NewPubKey)
	if !auth.VerifySignature(oldPub, msg, req.OldSig) || !auth.VerifySignature(newPub, msg, req.NewSig) {
		return badRequest("both the current and the new key must sign the rotation message")
	}
	if s.db.Count(`SELECT COUNT(*) FROM devices WHERE pubkey=?`, req.NewPubKey) > 0 {
		return conflict("key already in use")
	}
	_, err = s.db.Exec(`UPDATE devices SET pubkey=?, fingerprint=?, key_created_at=?, key_rotate_required=0 WHERE id=?`, req.NewPubKey, auth.Fingerprint(newPub), store.Now(), d.ID)
	if err != nil {
		return err
	}
	c.Audit("device.key_rotated", "device", d.ID, map[string]string{"fingerprint": auth.Fingerprint(newPub)})
	s.resolveAlerts("keyrotate:" + d.ID)
	return c.OK(map[string]string{"fingerprint": auth.Fingerprint(newPub)})
}

// ---------- discovery ----------

type peerView struct {
	ID          string     `json:"id"`
	DID         string     `json:"did"`
	Name        string     `json:"name"`
	OS          string     `json:"os"`
	Kind        string     `json:"kind"`
	Level       string     `json:"level"`
	User        string     `json:"user"`
	Group       string     `json:"group"`
	Online      bool       `json:"online"`
	LastSeenAt  store.Time `json:"last_seen_at"`
	BoxPubKey   string     `json:"box_pubkey"`
	Fingerprint string     `json:"fingerprint"`
	LanIP       string     `json:"lan_ip"`
	NATType     string     `json:"nat_type"`
}

func (s *Server) hPeers(c *Ctx) error {
	all, err := s.db.Devices(store.DeviceFilter{Status: "active", Q: c.Query("q")})
	if err != nil {
		return err
	}
	online := s.hub.OnlineIDs()
	scope := s.db.Settings().DiscoveryScope
	out := []peerView{}
	for _, d := range all {
		if !policy.CanSee(c.P.Device, d, scope) {
			continue
		}
		out = append(out, peerView{d.ID, d.DID, d.Name, d.OS, d.Kind, d.Level, d.Username, d.GroupName, online[d.ID], d.LastSeenAt, d.BoxPubKey, d.Fingerprint, d.LanIP, d.NATType})
	}
	return c.Items(out)
}

// ---------- relay messages over REST (fallback when the socket is down) ----------

func (s *Server) hSendMessage(c *Ctx) error {
	var req proto.MsgSend
	if err := c.Bind(&req); err != nil {
		return err
	}
	ack, err := s.hub.RelayREST(c.P.Device, &req)
	if err != nil {
		return protoToAPI(err)
	}
	return c.JSON(201, ack)
}

func (s *Server) hQueuedMessages(c *Ctx) error {
	limit := c.QueryInt("limit", 200, 1000)
	rows, err := s.db.Query(`SELECT id, from_id, to_id, mode, content_type, body, created_at FROM messages WHERE to_id=? AND status='queued' ORDER BY created_at, rowid LIMIT ?`, c.P.Device.ID, limit)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []proto.Message{}
	for rows.Next() {
		var m proto.Message
		if err := rows.Scan(&m.ID, &m.From, &m.To, &m.Mode, &m.ContentType, &m.Body, &m.CreatedAt); err != nil {
			return err
		}
		out = append(out, m)
	}
	return c.Items(out)
}

func (s *Server) hReceipts(c *Ctx) error {
	var req proto.Receipt
	if err := c.Bind(&req); err != nil {
		return err
	}
	if req.Kind != "delivered" && req.Kind != "read" {
		return badRequest("kind must be delivered or read")
	}
	if len(req.IDs) == 0 || len(req.IDs) > 500 {
		return badRequest("1..500 ids")
	}
	s.hub.Receipt(c.P.Device, req.IDs, req.Kind)
	return c.Done()
}

func protoToAPI(err error) error {
	code, msg, ok := hub.ErrInfo(err)
	if !ok {
		return err
	}
	status := map[string]int{"forbidden": 403, "not_found": 404, "mode_violation": 422, "relay_disabled": 503, "peer_offline": 409}[code]
	if status == 0 {
		status = 400
	}
	return errf(status, code, "%s", msg)
}

// ---------- ICE / TURN ----------

func (s *Server) hICEServers(c *Ctx) error {
	type iceServer struct {
		URLs       []string `json:"urls"`
		Username   string   `json:"username,omitempty"`
		Credential string   `json:"credential,omitempty"`
	}
	var stun, turn []string
	for _, u := range s.cfg.TURN.URLs {
		if strings.HasPrefix(u, "stun:") {
			stun = append(stun, u)
		} else {
			turn = append(turn, u)
		}
	}
	out := []iceServer{}
	if len(stun) > 0 {
		out = append(out, iceServer{URLs: stun})
	}
	var exp time.Time
	if len(turn) > 0 && s.cfg.TURN.Secret != "" {
		user, cred, e := auth.TURNCredentials(s.cfg.TURN.Secret, c.P.Device.ID, s.cfg.TURN.TTL, time.Now())
		exp = e
		out = append(out, iceServer{URLs: turn, Username: user, Credential: cred})
	}
	resp := map[string]any{
		"ice_servers": out,
		// how clients map the user's delivery mode to WebRTC settings
		"policies": map[string]string{
			proto.ModeP2P:   "iceTransportPolicy=all, TURN servers removed: direct only, never relayed",
			proto.ModeAuto:  "iceTransportPolicy=all with TURN; if ICE fails, fall back to the WebSocket relay",
			proto.ModeRelay: "use the WebSocket relay (msg.send) and /files; or iceTransportPolicy=relay for real-time streams",
		},
	}
	if !exp.IsZero() {
		resp["expires_at"] = exp.UTC().Format(time.RFC3339)
	}
	return c.OK(resp)
}

// ---------- relay files ----------

const maxChunk = 16 << 20

func (s *Server) filesDir() string { return filepath.Join(s.cfg.DataDir, "files") }

type fileRow struct {
	ID        string     `json:"id"`
	From      string     `json:"from"`
	To        string     `json:"to"`
	Name      string     `json:"name"`
	Size      int64      `json:"size"`
	SHA256    string     `json:"sha256"`
	Received  int64      `json:"received"`
	Status    string     `json:"status"`
	CreatedAt store.Time `json:"created_at"`
	ExpiresAt store.Time `json:"expires_at"`
}

func (s *Server) loadFile(id string) (*fileRow, error) {
	f := &fileRow{}
	err := s.db.QueryRow(`SELECT id, from_id, to_id, name, size, sha256, received, status, created_at, expires_at FROM files WHERE id=?`, id).
		Scan(&f.ID, &f.From, &f.To, &f.Name, &f.Size, &f.SHA256, &f.Received, &f.Status, &f.CreatedAt, &f.ExpiresAt)
	if err != nil {
		return nil, notFoundErr("file")
	}
	return f, nil
}

func (s *Server) hCreateFile(c *Ctx) error {
	var req struct {
		To     string `json:"to"`
		Name   string `json:"name"`
		Size   int64  `json:"size"`
		SHA256 string `json:"sha256"`
	}
	if err := c.Bind(&req); err != nil {
		return err
	}
	st := s.db.Settings()
	if !st.RelayEnabled {
		return errf(503, "relay_disabled", "the server relay is disabled")
	}
	if req.Size <= 0 || req.Size > int64(st.RelayMaxFileMB)<<20 {
		return badRequest("size must be 1..%d MB", st.RelayMaxFileMB)
	}
	name := filepath.Base(strings.TrimSpace(req.Name))
	if name == "" || name == "." || len(name) > 200 {
		return badRequest("invalid file name")
	}
	if len(req.SHA256) != 64 {
		return badRequest("sha256 (hex) required")
	}
	to, err := s.db.DeviceByID(req.To)
	if err != nil || !policy.CanMessage(c.P.Device, to, st.DiscoveryScope) {
		return forbidden("you are not allowed to send files to this device")
	}
	if err := os.MkdirAll(s.filesDir(), 0o700); err != nil {
		return err
	}
	id := store.NewID("file")
	exp := time.Now().Add(time.Duration(st.FileRetentionHours) * time.Hour)
	if _, err := s.db.Exec(`INSERT INTO files (id, from_id, to_id, name, size, sha256, created_at, expires_at) VALUES (?,?,?,?,?,?,?,?)`,
		id, c.P.Device.ID, to.ID, name, req.Size, strings.ToLower(req.SHA256), store.Now(), exp.Unix()); err != nil {
		return err
	}
	f, _ := os.OpenFile(filepath.Join(s.filesDir(), id), os.O_CREATE|os.O_WRONLY, 0o600)
	if f != nil {
		f.Close()
	}
	return c.JSON(201, map[string]any{"id": id, "upload_url": "/api/v1/files/" + id, "offset": 0, "chunk_max": maxChunk})
}

// hUploadChunk: PATCH /files/{id} with Upload-Offset header; append-only, resumable.
func (s *Server) hUploadChunk(c *Ctx) error {
	f, err := s.loadFile(c.Param("id"))
	if err != nil {
		return err
	}
	if f.From != c.P.Device.ID {
		return forbidden("not your upload")
	}
	if f.Status != "uploading" {
		return conflict("upload already complete")
	}
	off, err := strconv.ParseInt(c.R.Header.Get("Upload-Offset"), 10, 64)
	if err != nil || off != f.Received {
		c.W.Header().Set("Upload-Offset", strconv.FormatInt(f.Received, 10))
		return errf(409, "offset_mismatch", "Upload-Offset must equal %d", f.Received)
	}
	path := filepath.Join(s.filesDir(), f.ID)
	fh, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer fh.Close()
	if err := fh.Truncate(off); err != nil {
		return err
	}
	if _, err := fh.Seek(off, io.SeekStart); err != nil {
		return err
	}
	limit := f.Size - off
	if limit > maxChunk {
		limit = maxChunk
	}
	n, err := io.Copy(fh, io.LimitReader(c.R.Body, limit+1))
	if err != nil {
		return badRequest("upload interrupted: %v", err)
	}
	if n > limit {
		_ = fh.Truncate(off)
		return badRequest("chunk exceeds remaining size or %d bytes", maxChunk)
	}
	newOff := off + n
	s.hub.RelayBytes.Add(n)
	_, _ = s.db.Exec(`UPDATE files SET received=? WHERE id=?`, newOff, f.ID)
	c.W.Header().Set("Upload-Offset", strconv.FormatInt(newOff, 10))
	if newOff < f.Size {
		return c.OK(map[string]any{"offset": newOff, "complete": false})
	}
	sum, err := fileSHA256(path)
	if err != nil {
		return err
	}
	if sum != f.SHA256 {
		_, _ = s.db.Exec(`UPDATE files SET received=0 WHERE id=?`, f.ID)
		_ = os.Truncate(path, 0)
		return errf(422, "checksum_mismatch", "sha256 mismatch; upload restarted from 0")
	}
	_, _ = s.db.Exec(`UPDATE files SET status='ready' WHERE id=?`, f.ID)
	s.hub.Send(f.To, proto.TFileOffer, proto.FileOffer{FileID: f.ID, From: f.From, Name: f.Name, Size: f.Size, SHA256: f.SHA256})
	s.bus.Publish("file.ready", map[string]any{"id": f.ID, "from": f.From, "to": f.To, "size": f.Size})
	return c.OK(map[string]any{"offset": newOff, "complete": true})
}

func fileSHA256(path string) (string, error) {
	fh, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer fh.Close()
	h := sha256.New()
	if _, err := io.Copy(h, fh); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (s *Server) hFileInfo(c *Ctx) error {
	f, err := s.loadFile(c.Param("id"))
	if err != nil {
		return err
	}
	if f.From != c.P.Device.ID && f.To != c.P.Device.ID {
		return notFoundErr("file")
	}
	c.W.Header().Set("Upload-Offset", strconv.FormatInt(f.Received, 10))
	return c.OK(f)
}

func (s *Server) hDownloadFile(c *Ctx) error {
	f, err := s.loadFile(c.Param("id"))
	if err != nil {
		return err
	}
	if f.To != c.P.Device.ID && f.From != c.P.Device.ID {
		return notFoundErr("file")
	}
	if f.Status != "ready" && f.Status != "downloaded" {
		return conflict("file is not ready")
	}
	fh, err := os.Open(filepath.Join(s.filesDir(), f.ID))
	if err != nil {
		return notFoundErr("file content")
	}
	defer fh.Close()
	c.W.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename*=UTF-8''%s`, urlPathEscape(f.Name)))
	c.W.Header().Set("Content-Type", "application/octet-stream")
	c.W.Header().Set("X-Content-SHA256", f.SHA256)
	cw := &countingWriter{ResponseWriter: c.W}
	http.ServeContent(cw, c.R, "", f.CreatedAt.Time, fh)
	s.hub.RelayBytes.Add(cw.n)
	if f.To == c.P.Device.ID && c.R.Header.Get("Range") == "" {
		_, _ = s.db.Exec(`UPDATE files SET status='downloaded' WHERE id=?`, f.ID)
	}
	return nil
}

type countingWriter struct {
	http.ResponseWriter
	n int64
}

func (w *countingWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.n += int64(n)
	return n, err
}

func urlPathEscape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.IndexByte("-._~", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func (s *Server) hDeleteFile(c *Ctx) error {
	f, err := s.loadFile(c.Param("id"))
	if err != nil {
		return err
	}
	if f.From != c.P.Device.ID && f.To != c.P.Device.ID {
		return notFoundErr("file")
	}
	_ = os.Remove(filepath.Join(s.filesDir(), f.ID))
	_, err = s.db.Exec(`DELETE FROM files WHERE id=?`, f.ID)
	if err != nil {
		return err
	}
	return c.Done()
}

func (s *Server) hListFiles(c *Ctx) error {
	rows, err := s.db.Query(`SELECT id, from_id, to_id, name, size, sha256, received, status, created_at, expires_at FROM files WHERE from_id=? OR to_id=? ORDER BY created_at DESC LIMIT 200`, c.P.Device.ID, c.P.Device.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []fileRow{}
	for rows.Next() {
		var f fileRow
		if err := rows.Scan(&f.ID, &f.From, &f.To, &f.Name, &f.Size, &f.SHA256, &f.Received, &f.Status, &f.CreatedAt, &f.ExpiresAt); err != nil {
			return err
		}
		out = append(out, f)
	}
	return c.Items(out)
}

// ---------- own FRP mappings ----------

type mappingReq struct {
	Name           *string   `json:"name"`
	Node           *string   `json:"node"`
	Device         *string   `json:"device"` // admin endpoints only
	Proto          *string   `json:"proto"`
	LocalIP        *string   `json:"local_ip"`
	LocalPort      *int      `json:"local_port"`
	RemotePort     *int      `json:"remote_port"`
	Domains        *[]string `json:"domains"`
	Subdomain      *string   `json:"subdomain"`
	SecretKey      *string   `json:"secret_key"`
	UseEncryption  *bool     `json:"use_encryption"`
	UseCompression *bool     `json:"use_compression"`
	BandwidthLimit *string   `json:"bandwidth_limit"`
	Enabled        *bool     `json:"enabled"`
}

func (r *mappingReq) apply(m *store.Mapping) {
	if r.Name != nil {
		m.Name = strings.TrimSpace(*r.Name)
	}
	if r.Proto != nil {
		m.Proto = strings.ToLower(*r.Proto)
	}
	if r.LocalIP != nil {
		m.LocalIP = strings.TrimSpace(*r.LocalIP)
	}
	if r.LocalPort != nil {
		m.LocalPort = *r.LocalPort
	}
	if r.RemotePort != nil {
		m.RemotePort = *r.RemotePort
	}
	if r.Domains != nil {
		m.Domains.V = *r.Domains
	}
	if r.Subdomain != nil {
		m.Subdomain = *r.Subdomain
	}
	if r.SecretKey != nil {
		m.SecretKey = *r.SecretKey
	}
	if r.UseEncryption != nil {
		m.UseEncryption = *r.UseEncryption
	}
	if r.UseCompression != nil {
		m.UseCompression = *r.UseCompression
	}
	if r.BandwidthLimit != nil {
		m.BandwidthLimit = *r.BandwidthLimit
	}
	if r.Enabled != nil {
		m.Enabled = *r.Enabled
	}
}

// saveMapping validates and inserts/updates a mapping, then notifies the device.
func (s *Server) saveMapping(c *Ctx, m *store.Mapping, req *mappingReq, isNew bool) (*store.Mapping, error) {
	oldNode := m.NodeID
	if req.Node != nil {
		m.NodeID = *req.Node
	}
	node, err := s.db.NodeByID(m.NodeID)
	if err != nil {
		return nil, badRequest("unknown frps node %q", m.NodeID)
	}
	m.NodeID = node.ID
	req.apply(m)
	if m.Proto == "stcp" || m.Proto == "xtcp" {
		if m.SecretKey == "" {
			m.SecretKey = auth.RandomHex(12)
		}
	}
	if err := frp.ValidateMapping(s.db, m, node); err != nil {
		return nil, badRequest("%v", err)
	}
	now := store.Now()
	if isNew {
		m.ID = store.NewID("map")
		m.CreatedAt, m.UpdatedAt = now, now
		m.CreatedBy = c.P.Name()
		status := "pending"
		if !m.Enabled {
			status = "stopped"
		}
		_, err = s.db.Exec(`INSERT INTO mappings (id, name, device_id, node_id, proto, local_ip, local_port, remote_port, domains, subdomain, secret_key, use_encryption, use_compression, bandwidth_limit, enabled, status, created_by, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			m.ID, m.Name, m.DeviceID, m.NodeID, m.Proto, m.LocalIP, m.LocalPort, m.RemotePort, m.Domains, m.Subdomain, m.SecretKey, m.UseEncryption, m.UseCompression, m.BandwidthLimit, m.Enabled, status, m.CreatedBy, now, now)
	} else {
		_, err = s.db.Exec(`UPDATE mappings SET name=?, node_id=?, proto=?, local_ip=?, local_port=?, remote_port=?, domains=?, subdomain=?, secret_key=?, use_encryption=?, use_compression=?, bandwidth_limit=?, enabled=?,
			status=CASE WHEN ? THEN 'pending' ELSE 'stopped' END, last_error='', updated_at=? WHERE id=?`,
			m.Name, m.NodeID, m.Proto, m.LocalIP, m.LocalPort, m.RemotePort, m.Domains, m.Subdomain, m.SecretKey, m.UseEncryption, m.UseCompression, m.BandwidthLimit, m.Enabled, m.Enabled, now, m.ID)
	}
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, conflict("mapping name already used on this device")
		}
		return nil, err
	}
	s.frp.NotifyDevice(m.DeviceID)
	if !isNew {
		// make frps drop the old definition; frpc re-registers the new one
		s.frp.Kick(m.NodeID, m.DeviceID)
		if oldNode != "" && oldNode != m.NodeID {
			s.frp.Kick(oldNode, m.DeviceID)
		}
	}
	action := "frp.mapping.updated"
	if isNew {
		action = "frp.mapping.created"
	}
	c.Audit(action, "mapping", m.ID, map[string]any{"name": m.Name, "proto": m.Proto, "node": node.Name, "device": m.DeviceID})
	s.bus.Publish(action, map[string]any{"id": m.ID, "name": m.Name})
	return s.db.MappingByID(m.ID)
}

func (s *Server) myMapping(c *Ctx) (*store.Mapping, error) {
	m, err := s.db.MappingByID(c.Param("id"))
	if err != nil || m.DeviceID != c.P.Device.ID {
		return nil, notFoundErr("mapping")
	}
	return m, nil
}

func (s *Server) hMyMappings(c *Ctx) error {
	list, err := s.db.Mappings(`m.device_id=?`, c.P.Device.ID)
	if err != nil {
		return err
	}
	return c.Items(list)
}

func (s *Server) hCreateMyMapping(c *Ctx) error {
	if !policy.CanCreateMapping(c.P.Device) {
		return forbidden("your device level (%s) cannot publish services; request member level", c.P.Device.Level)
	}
	var req mappingReq
	if err := c.Bind(&req); err != nil {
		return err
	}
	if req.Node == nil {
		return badRequest("node is required")
	}
	m := &store.Mapping{DeviceID: c.P.Device.ID, Enabled: true, UseEncryption: true}
	out, err := s.saveMapping(c, m, &req, true)
	if err != nil {
		return err
	}
	return c.JSON(201, out)
}

func (s *Server) hUpdateMyMapping(c *Ctx) error {
	if !policy.CanCreateMapping(c.P.Device) {
		return forbidden("your device level cannot manage tunnels")
	}
	m, err := s.myMapping(c)
	if err != nil {
		return err
	}
	var req mappingReq
	if err := c.Bind(&req); err != nil {
		return err
	}
	out, err := s.saveMapping(c, m, &req, false)
	if err != nil {
		return err
	}
	return c.OK(out)
}

func (s *Server) hDeleteMyMapping(c *Ctx) error {
	m, err := s.myMapping(c)
	if err != nil {
		return err
	}
	return s.deleteMapping(c, m)
}

func (s *Server) deleteMapping(c *Ctx, m *store.Mapping) error {
	if _, err := s.db.Exec(`DELETE FROM mappings WHERE id=?`, m.ID); err != nil {
		return err
	}
	s.frp.NotifyDevice(m.DeviceID)
	s.frp.Kick(m.NodeID, m.DeviceID)
	c.Audit("frp.mapping.deleted", "mapping", m.ID, map[string]string{"name": m.Name})
	s.bus.Publish("frp.mapping.deleted", map[string]any{"id": m.ID, "name": m.Name})
	return c.Done()
}

func (s *Server) hToggleMyMapping(on bool) func(*Ctx) error {
	return func(c *Ctx) error {
		m, err := s.myMapping(c)
		if err != nil {
			return err
		}
		if on && !policy.CanCreateMapping(c.P.Device) {
			return forbidden("your device level cannot start tunnels")
		}
		if err := s.frp.SetEnabled(m, on); err != nil {
			return err
		}
		c.Audit(map[bool]string{true: "frp.mapping.started", false: "frp.mapping.stopped"}[on], "mapping", m.ID, nil)
		out, _ := s.db.MappingByID(m.ID)
		return c.OK(out)
	}
}

// frpcConfigs returns one frpc.toml per node the device has mappings on.
func (s *Server) hMyFRPC(c *Ctx) error {
	d, err := s.db.DeviceByID(c.P.Device.ID)
	if err != nil {
		return err
	}
	maps, err := s.db.Mappings(`m.device_id=?`, d.ID)
	if err != nil {
		return err
	}
	type cfg struct {
		NodeID string `json:"node_id"`
		Node   string `json:"node"`
		TOML   string `json:"toml"`
		Active int    `json:"active_proxies"`
	}
	nodes, err := s.db.Nodes()
	if err != nil {
		return err
	}
	out := []cfg{}
	for _, n := range nodes {
		if !n.Enabled {
			continue
		}
		active, any := 0, false
		for _, m := range maps {
			if m.NodeID == n.ID {
				any = true
				if m.Enabled {
					active++
				}
			}
		}
		if !any && c.Query("all") != "1" {
			continue
		}
		out = append(out, cfg{NodeID: n.ID, Node: n.Name, TOML: frp.ClientConfig(s.cfg.FRP.AuthToken, d, n, maps), Active: active})
	}
	return c.Items(out)
}

func (s *Server) hMyFRPCNode(c *Ctx) error {
	d, err := s.db.DeviceByID(c.P.Device.ID)
	if err != nil {
		return err
	}
	n, err := s.db.NodeByID(strings.TrimSuffix(c.Param("node"), ".toml"))
	if err != nil {
		return notFoundErr("node")
	}
	maps, err := s.db.Mappings(`m.device_id=? AND m.node_id=?`, d.ID, n.ID)
	if err != nil {
		return err
	}
	c.W.Header().Set("Content-Type", "application/toml; charset=utf-8")
	_, err = io.WriteString(c.W, frp.ClientConfig(s.cfg.FRP.AuthToken, d, n, maps))
	return err
}

// hNodesPublic lists frps nodes for clients choosing where to publish.
func (s *Server) hNodesPublic(c *Ctx) error {
	nodes, err := s.db.Nodes()
	if err != nil {
		return err
	}
	type view struct {
		ID             string `json:"id"`
		Name           string `json:"name"`
		Region         string `json:"region"`
		PublicAddr     string `json:"public_addr"`
		BindPort       int    `json:"bind_port"`
		VhostHTTPPort  int    `json:"vhost_http_port"`
		VhostHTTPSPort int    `json:"vhost_https_port"`
		SubdomainHost  string `json:"subdomain_host"`
		AllowPorts     string `json:"allow_ports"`
		Status         string `json:"status"`
	}
	out := []view{}
	for _, n := range nodes {
		if n.Enabled {
			out = append(out, view{n.ID, n.Name, n.Region, n.PublicAddr, n.BindPort, n.VhostHTTPPort, n.VhostHTTPSPort, n.SubdomainHost, n.AllowPorts, n.Status})
		}
	}
	return c.Items(out)
}

// ---------- grants requested by devices ----------

var validDurations = map[string]time.Duration{"1h": time.Hour, "6h": 6 * time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour, "perm": 0}

func (s *Server) hMyGrants(c *Ctx) error {
	list, err := s.db.Grants(`g.requester_id=? OR g.target_id=?`, c.P.Device.ID, c.P.Device.ID)
	if err != nil {
		return err
	}
	return c.Items(list)
}

func (s *Server) hRequestGrant(c *Ctx) error {
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
		return badRequest("duration must be 24h, 7d, 30d, 90d or perm")
	}
	target, err := s.db.DeviceByID(req.Target)
	if errors.Is(err, store.ErrNotFound) {
		target = nil
	} else if err != nil {
		return err
	}
	if ok, why := policy.CanRequestGrant(c.P.Device, target, req.Level); !ok {
		return forbidden("%s", why)
	}
	if s.db.Count(`SELECT COUNT(*) FROM grants WHERE target_id=? AND status='pending'`, target.ID) > 0 {
		return conflict("there is already a pending request for this device")
	}
	g := &store.Grant{ID: store.NewID("grt")}
	_, err = s.db.Exec(`INSERT INTO grants (id, requester_id, target_id, level, reason, duration, status, created_at) VALUES (?,?,?,?,?,?, 'pending', ?)`,
		g.ID, c.P.Device.ID, target.ID, req.Level, trimTo(req.Reason, 300), req.Duration, store.Now())
	if err != nil {
		return err
	}
	c.Audit("grant.requested", "grant", g.ID, map[string]string{"target": target.ID, "level": req.Level})
	s.bus.Publish("grant.requested", map[string]any{"id": g.ID, "requester": c.P.Device.Name, "target": target.Name, "level": req.Level})
	s.hub.Send(target.ID, proto.TGrantUpdate, proto.GrantUpdate{GrantID: g.ID, Status: "pending", Target: target.ID, Level: req.Level})
	out, _ := s.db.GrantByID(g.ID)
	return c.JSON(201, out)
}

// ---------- device websocket ----------

func (s *Server) hDeviceWS(c *Ctx) error {
	ws, err := websocket.Accept(c.W, c.R, &websocket.AcceptOptions{InsecureSkipVerify: true}) // devices are not browsers; auth is the bearer token
	if err != nil {
		return nil
	}
	s.hub.Serve(c.R.Context(), ws, c.P.Device, c.IP)
	return nil
}
