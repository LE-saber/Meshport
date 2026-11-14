package store

import (
	"database/sql"
	"strings"
)

// ---------- users ----------

type User struct {
	ID            string `json:"id"`
	Username      string `json:"username"`
	DisplayName   string `json:"display_name"`
	Email         string `json:"email"`
	PasswordHash  string `json:"-"`
	Role          string `json:"role"`
	MFASecret     string `json:"-"`
	MFAEnabled    bool   `json:"mfa_enabled"`
	Disabled      bool   `json:"disabled"`
	CreatedAt     Time   `json:"created_at"`
	LastLoginAt   Time   `json:"last_login_at"`
	MFARemindedAt Time   `json:"mfa_reminded_at"`
}

const userCols = `id, username, display_name, email, password_hash, role, mfa_secret, mfa_enabled, disabled, created_at, last_login_at, mfa_reminded_at`

func scanUser(r interface{ Scan(...any) error }) (*User, error) {
	u := &User{}
	err := r.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.PasswordHash, &u.Role, &u.MFASecret, &u.MFAEnabled, &u.Disabled, &u.CreatedAt, &u.LastLoginAt, &u.MFARemindedAt)
	return u, notFound(err)
}

func (db *DB) UserByID(id string) (*User, error) {
	return scanUser(db.QueryRow(`SELECT `+userCols+` FROM users WHERE id=?`, id))
}

func (db *DB) UserByName(name string) (*User, error) {
	return scanUser(db.QueryRow(`SELECT `+userCols+` FROM users WHERE username=?`, name))
}

func (db *DB) Users() ([]*User, error) {
	rows, err := db.Query(`SELECT ` + userCols + ` FROM users ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (db *DB) CreateUser(u *User) error {
	if u.ID == "" {
		u.ID = NewID("usr")
	}
	if u.CreatedAt.IsZero() {
		u.CreatedAt = Now()
	}
	_, err := db.Exec(`INSERT INTO users (`+userCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		u.ID, u.Username, u.DisplayName, u.Email, u.PasswordHash, u.Role, u.MFASecret, u.MFAEnabled, u.Disabled, u.CreatedAt, u.LastLoginAt, u.MFARemindedAt)
	return err
}

// ---------- devices ----------

type Device struct {
	ID                string `json:"id"`
	DID               string `json:"did"`
	Name              string `json:"name"`
	OS                string `json:"os"`
	Kind              string `json:"kind"`
	UserID            string `json:"user_id"`
	Username          string `json:"user"`
	GroupID           string `json:"group_id"`
	GroupName         string `json:"group"`
	Level             string `json:"level"`
	Status            string `json:"status"`
	BanReason         string `json:"ban_reason,omitempty"`
	PubKey            string `json:"pubkey"`
	BoxPubKey         string `json:"box_pubkey"`
	Fingerprint       string `json:"fingerprint"`
	KeyCreatedAt      Time   `json:"key_created_at"`
	KeyRotateRequired bool   `json:"key_rotate_required"`
	FRPSecret         string `json:"-"`
	LastIP            string `json:"ip"`
	LanIP             string `json:"lan_ip"`
	NATType           string `json:"nat_type"`
	ClientVersion     string `json:"client_version"`
	LastSeenAt        Time   `json:"last_seen_at"`
	RegisteredAt      Time   `json:"registered_at"`

	// filled in by the API layer from the hub
	Online bool `json:"online"`
}

const deviceSelect = `SELECT d.id, d.did, d.name, d.os, d.kind, COALESCE(d.user_id,''), COALESCE(u.username,''), COALESCE(d.group_id,''), COALESCE(g.name,''),
 d.level, d.status, d.ban_reason, d.pubkey, d.box_pubkey, d.fingerprint, d.key_created_at, d.key_rotate_required, d.frp_secret,
 d.last_ip, d.lan_ip, d.nat_type, d.client_version, d.last_seen_at, d.registered_at
 FROM devices d LEFT JOIN users u ON u.id=d.user_id LEFT JOIN groups g ON g.id=d.group_id`

func scanDevice(r interface{ Scan(...any) error }) (*Device, error) {
	d := &Device{}
	err := r.Scan(&d.ID, &d.DID, &d.Name, &d.OS, &d.Kind, &d.UserID, &d.Username, &d.GroupID, &d.GroupName,
		&d.Level, &d.Status, &d.BanReason, &d.PubKey, &d.BoxPubKey, &d.Fingerprint, &d.KeyCreatedAt, &d.KeyRotateRequired, &d.FRPSecret,
		&d.LastIP, &d.LanIP, &d.NATType, &d.ClientVersion, &d.LastSeenAt, &d.RegisteredAt)
	return d, notFound(err)
}

func (db *DB) DeviceByID(id string) (*Device, error) {
	return scanDevice(db.QueryRow(deviceSelect+` WHERE d.id=? OR d.did=?`, id, id))
}

type DeviceFilter struct {
	Q       string
	Status  string
	GroupID string
	UserID  string
	Level   string
}

func (db *DB) Devices(f DeviceFilter) ([]*Device, error) {
	var where []string
	var args []any
	if f.Q != "" {
		where = append(where, `(d.name LIKE ? ESCAPE '\' OR d.did LIKE ? ESCAPE '\' OR d.last_ip LIKE ? ESCAPE '\' OR d.os LIKE ? ESCAPE '\' OR u.username LIKE ? ESCAPE '\')`)
		l := Like(f.Q)
		args = append(args, l, l, l, l, l)
	}
	if f.Status != "" {
		where = append(where, `d.status=?`)
		args = append(args, f.Status)
	}
	if f.GroupID != "" {
		where = append(where, `d.group_id=?`)
		args = append(args, f.GroupID)
	}
	if f.UserID != "" {
		where = append(where, `d.user_id=?`)
		args = append(args, f.UserID)
	}
	if f.Level != "" {
		where = append(where, `d.level=?`)
		args = append(args, f.Level)
	}
	q := deviceSelect
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY d.registered_at"
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Device{}
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (db *DB) CreateDevice(d *Device) error {
	_, err := db.Exec(`INSERT INTO devices (id, did, name, os, kind, user_id, group_id, level, status, pubkey, box_pubkey, fingerprint, key_created_at, frp_secret, last_ip, lan_ip, client_version, registered_at)
	VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		d.ID, d.DID, d.Name, d.OS, d.Kind, nullStr(d.UserID), nullStr(d.GroupID), d.Level, d.Status, d.PubKey, d.BoxPubKey, d.Fingerprint, d.KeyCreatedAt, d.FRPSecret, d.LastIP, d.LanIP, d.ClientVersion, d.RegisteredAt)
	return err
}

// ---------- groups ----------

type Group struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	CreatedAt   Time   `json:"created_at"`
	Devices     int    `json:"devices"`
}

func (db *DB) Groups() ([]*Group, error) {
	rows, err := db.Query(`SELECT g.id, g.name, g.description, g.created_at, (SELECT COUNT(*) FROM devices d WHERE d.group_id=g.id) FROM groups g ORDER BY g.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Group{}
	for rows.Next() {
		g := &Group{}
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.CreatedAt, &g.Devices); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// GroupIDByName returns the id of a group, creating it when create is true.
func (db *DB) GroupIDByName(name string, create bool) (string, error) {
	var id string
	err := db.QueryRow(`SELECT id FROM groups WHERE name=?`, name).Scan(&id)
	if err == sql.ErrNoRows && create {
		id = NewID("grp")
		_, err = db.Exec(`INSERT INTO groups (id, name, created_at) VALUES (?,?,?)`, id, name, Now())
	}
	return id, notFound(err)
}

// ---------- grants ----------

type Grant struct {
	ID            string `json:"id"`
	RequesterID   string `json:"requester_id"`
	RequesterName string `json:"requester"`
	TargetID      string `json:"target_id"`
	TargetName    string `json:"target"`
	Level         string `json:"level"`
	PrevLevel     string `json:"prev_level"`
	Reason        string `json:"reason"`
	Duration      string `json:"duration"`
	Status        string `json:"status"`
	CreatedAt     Time   `json:"created_at"`
	DecidedAt     Time   `json:"decided_at"`
	DecidedBy     string `json:"decided_by"`
	DecisionNote  string `json:"decision_note"`
	ExpiresAt     Time   `json:"expires_at"`
}

const grantSelect = `SELECT g.id, COALESCE(g.requester_id,''), COALESCE(r.name,'admin'), g.target_id, COALESCE(t.name,''), g.level, g.prev_level, g.reason, g.duration, g.status,
 g.created_at, g.decided_at, g.decided_by, g.decision_note, g.expires_at
 FROM grants g LEFT JOIN devices r ON r.id=g.requester_id LEFT JOIN devices t ON t.id=g.target_id`

func scanGrant(r interface{ Scan(...any) error }) (*Grant, error) {
	g := &Grant{}
	err := r.Scan(&g.ID, &g.RequesterID, &g.RequesterName, &g.TargetID, &g.TargetName, &g.Level, &g.PrevLevel, &g.Reason, &g.Duration, &g.Status,
		&g.CreatedAt, &g.DecidedAt, &g.DecidedBy, &g.DecisionNote, &g.ExpiresAt)
	return g, notFound(err)
}

func (db *DB) GrantByID(id string) (*Grant, error) {
	return scanGrant(db.QueryRow(grantSelect+` WHERE g.id=?`, id))
}

func (db *DB) Grants(where string, args ...any) ([]*Grant, error) {
	q := grantSelect
	if where != "" {
		q += " WHERE " + where
	}
	q += " ORDER BY g.created_at DESC"
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Grant{}
	for rows.Next() {
		g, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ---------- FRP ----------

type Node struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Region         string `json:"region"`
	PublicAddr     string `json:"public_addr"`
	BindPort       int    `json:"bind_port"`
	VhostHTTPPort  int    `json:"vhost_http_port"`
	VhostHTTPSPort int    `json:"vhost_https_port"`
	SubdomainHost  string `json:"subdomain_host"`
	AllowPorts     string `json:"allow_ports"`
	DashboardAddr  string `json:"dashboard_addr"`
	DashboardUser  string `json:"dashboard_user"`
	DashboardPass  string `json:"-"`
	PluginToken    string `json:"-"`
	Managed        bool   `json:"managed"`
	Enabled        bool   `json:"enabled"`
	Status         string `json:"status"`
	Version        string `json:"version"`
	LastError      string `json:"last_error"`
	LastSeenAt     Time   `json:"last_seen_at"`
	CreatedAt      Time   `json:"created_at"`
}

const nodeCols = `id, name, region, public_addr, bind_port, vhost_http_port, vhost_https_port, subdomain_host, allow_ports, dashboard_addr, dashboard_user, dashboard_pass, plugin_token, managed, enabled, status, version, last_error, last_seen_at, created_at`

func scanNode(r interface{ Scan(...any) error }) (*Node, error) {
	n := &Node{}
	err := r.Scan(&n.ID, &n.Name, &n.Region, &n.PublicAddr, &n.BindPort, &n.VhostHTTPPort, &n.VhostHTTPSPort, &n.SubdomainHost, &n.AllowPorts, &n.DashboardAddr, &n.DashboardUser, &n.DashboardPass, &n.PluginToken, &n.Managed, &n.Enabled, &n.Status, &n.Version, &n.LastError, &n.LastSeenAt, &n.CreatedAt)
	return n, notFound(err)
}

func (db *DB) NodeByID(id string) (*Node, error) {
	return scanNode(db.QueryRow(`SELECT `+nodeCols+` FROM frps_nodes WHERE id=? OR name=?`, id, id))
}

func (db *DB) Nodes() ([]*Node, error) {
	rows, err := db.Query(`SELECT ` + nodeCols + ` FROM frps_nodes ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Node{}
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (db *DB) CreateNode(n *Node) error {
	if n.ID == "" {
		n.ID = NewID("frps")
	}
	if n.CreatedAt.IsZero() {
		n.CreatedAt = Now()
	}
	if n.Status == "" {
		n.Status = "unknown"
	}
	_, err := db.Exec(`INSERT INTO frps_nodes (`+nodeCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		n.ID, n.Name, n.Region, n.PublicAddr, n.BindPort, n.VhostHTTPPort, n.VhostHTTPSPort, n.SubdomainHost, n.AllowPorts, n.DashboardAddr, n.DashboardUser, n.DashboardPass, n.PluginToken, n.Managed, n.Enabled, n.Status, n.Version, n.LastError, n.LastSeenAt, n.CreatedAt)
	return err
}

type Mapping struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	DeviceID       string         `json:"device_id"`
	DeviceName     string         `json:"device"`
	DeviceDID      string         `json:"device_did"`
	NodeID         string         `json:"node_id"`
	NodeName       string         `json:"node"`
	Proto          string         `json:"proto"`
	LocalIP        string         `json:"local_ip"`
	LocalPort      int            `json:"local_port"`
	RemotePort     int            `json:"remote_port"`
	Domains        JSON[[]string] `json:"domains"`
	Subdomain      string         `json:"subdomain"`
	SecretKey      string         `json:"secret_key,omitempty"`
	UseEncryption  bool           `json:"use_encryption"`
	UseCompression bool           `json:"use_compression"`
	BandwidthLimit string         `json:"bandwidth_limit"`
	Enabled        bool           `json:"enabled"`
	Status         string         `json:"status"`
	LastError      string         `json:"last_error"`
	Conns          int            `json:"conns"`
	TrafficIn      int64          `json:"traffic_in"`
	TrafficOut     int64          `json:"traffic_out"`
	RateIn         int64          `json:"rate_in"`
	RateOut        int64          `json:"rate_out"`
	CreatedBy      string         `json:"created_by"`
	CreatedAt      Time           `json:"created_at"`
	UpdatedAt      Time           `json:"updated_at"`
	PublicAddr     string         `json:"public_addr"`
}

const mappingSelect = `SELECT m.id, m.name, m.device_id, COALESCE(d.name,''), COALESCE(d.did,''), m.node_id, COALESCE(n.name,''), m.proto, m.local_ip, m.local_port, m.remote_port,
 m.domains, m.subdomain, m.secret_key, m.use_encryption, m.use_compression, m.bandwidth_limit, m.enabled, m.status, m.last_error, m.conns, m.traffic_in, m.traffic_out,
 m.rate_in, m.rate_out, m.created_by, m.created_at, m.updated_at, COALESCE(n.public_addr,''), COALESCE(n.subdomain_host,''), COALESCE(n.vhost_http_port,0), COALESCE(n.vhost_https_port,0)
 FROM mappings m LEFT JOIN devices d ON d.id=m.device_id LEFT JOIN frps_nodes n ON n.id=m.node_id`

func scanMapping(r interface{ Scan(...any) error }) (*Mapping, error) {
	m := &Mapping{}
	var nodeAddr, subHost string
	var httpPort, httpsPort int
	err := r.Scan(&m.ID, &m.Name, &m.DeviceID, &m.DeviceName, &m.DeviceDID, &m.NodeID, &m.NodeName, &m.Proto, &m.LocalIP, &m.LocalPort, &m.RemotePort,
		&m.Domains, &m.Subdomain, &m.SecretKey, &m.UseEncryption, &m.UseCompression, &m.BandwidthLimit, &m.Enabled, &m.Status, &m.LastError, &m.Conns, &m.TrafficIn, &m.TrafficOut,
		&m.RateIn, &m.RateOut, &m.CreatedBy, &m.CreatedAt, &m.UpdatedAt, &nodeAddr, &subHost, &httpPort, &httpsPort)
	if err != nil {
		return nil, notFound(err)
	}
	if m.Domains.V == nil {
		m.Domains.V = []string{}
	}
	m.PublicAddr = PublicAddress(m, nodeAddr, subHost, httpPort, httpsPort)
	return m, nil
}

// PublicAddress is what the outside world uses to reach a mapping.
func PublicAddress(m *Mapping, nodeAddr, subHost string, httpPort, httpsPort int) string {
	switch m.Proto {
	case "tcp", "udp":
		return nodeAddr + ":" + itoa(m.RemotePort)
	case "http", "https":
		host := ""
		if len(m.Domains.V) > 0 {
			host = m.Domains.V[0]
		} else if m.Subdomain != "" && subHost != "" {
			host = m.Subdomain + "." + subHost
		}
		if host == "" {
			return ""
		}
		port := httpPort
		if m.Proto == "https" {
			port = httpsPort
		}
		if (m.Proto == "http" && port == 80) || (m.Proto == "https" && port == 443) || port == 0 {
			return m.Proto + "://" + host
		}
		return m.Proto + "://" + host + ":" + itoa(port)
	default: // stcp / xtcp: reachable only through a visitor holding the secret key
		return "visitor:" + m.Name
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}

func (db *DB) MappingByID(id string) (*Mapping, error) {
	return scanMapping(db.QueryRow(mappingSelect+` WHERE m.id=?`, id))
}

func (db *DB) Mappings(where string, args ...any) ([]*Mapping, error) {
	q := mappingSelect
	if where != "" {
		q += " WHERE " + where
	}
	q += " ORDER BY m.created_at"
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Mapping{}
	for rows.Next() {
		m, err := scanMapping(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ---------- communication sessions ----------

type FileProgress struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Done      int64  `json:"done"`
	Percent   int    `json:"percent"`
	RateBps   int64  `json:"rate_bps"`
	Direction string `json:"direction,omitempty"`
}

type CommSession struct {
	ID            string              `json:"id"`
	AID           string              `json:"a_id"`
	AName         string              `json:"a"`
	BID           string              `json:"b_id"`
	BName         string              `json:"b"`
	Mode          string              `json:"mode"`
	Path          string              `json:"path"`
	Via           string              `json:"via"`
	State         string              `json:"state"`
	LatencyMS     *int                `json:"latency_ms"`
	RateBps       int64               `json:"rate_bps"`
	MsgsSent      int                 `json:"msgs_sent"`
	MsgsDelivered int                 `json:"msgs_delivered"`
	MsgsRead      int                 `json:"msgs_read"`
	MsgsFailed    int                 `json:"msgs_failed"`
	File          JSON[*FileProgress] `json:"file"`
	P2PAttempted  bool                `json:"p2p_attempted"`
	P2POK         bool                `json:"p2p_ok"`
	FailReason    string              `json:"fail_reason"`
	StartedAt     Time                `json:"started_at"`
	UpdatedAt     Time                `json:"updated_at"`
	EndedAt       Time                `json:"ended_at"`
}

const sessionSelect = `SELECT s.id, s.a_id, COALESCE(a.name,''), s.b_id, COALESCE(b.name,''), s.mode, s.path, s.via, s.state, s.latency_ms, s.rate_bps,
 s.msgs_sent, s.msgs_delivered, s.msgs_read, s.msgs_failed, s.file, s.p2p_attempted, s.p2p_ok, s.fail_reason, s.started_at, s.updated_at, s.ended_at
 FROM comm_sessions s LEFT JOIN devices a ON a.id=s.a_id LEFT JOIN devices b ON b.id=s.b_id`

func scanSession(r interface{ Scan(...any) error }) (*CommSession, error) {
	s := &CommSession{}
	var lat sql.NullInt64
	err := r.Scan(&s.ID, &s.AID, &s.AName, &s.BID, &s.BName, &s.Mode, &s.Path, &s.Via, &s.State, &lat, &s.RateBps,
		&s.MsgsSent, &s.MsgsDelivered, &s.MsgsRead, &s.MsgsFailed, &s.File, &s.P2PAttempted, &s.P2POK, &s.FailReason, &s.StartedAt, &s.UpdatedAt, &s.EndedAt)
	if lat.Valid {
		v := int(lat.Int64)
		s.LatencyMS = &v
	}
	return s, notFound(err)
}

func (db *DB) SessionByID(id string) (*CommSession, error) {
	return scanSession(db.QueryRow(sessionSelect+` WHERE s.id=?`, id))
}

func (db *DB) Sessions(where string, args ...any) ([]*CommSession, error) {
	q := sessionSelect
	if where != "" {
		q += " WHERE " + where
	}
	q += " ORDER BY s.updated_at DESC LIMIT 500"
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*CommSession{}
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
