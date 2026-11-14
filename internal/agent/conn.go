package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"meshport/pkg/client"
	"meshport/pkg/proto"
)

// Peer is a device this device may see, as returned by GET /peers plus
// local link state.
type Peer struct {
	ID          string    `json:"id"`
	DID         string    `json:"did"`
	Name        string    `json:"name"`
	OS          string    `json:"os"`
	Kind        string    `json:"kind"`
	Level       string    `json:"level"`
	User        string    `json:"user"`
	Group       string    `json:"group"`
	Online      bool      `json:"online"`
	LastSeenAt  any       `json:"last_seen_at"`
	BoxPubKey   string    `json:"box_pubkey"`
	Fingerprint string    `json:"fingerprint"`
	LanIP       string    `json:"lan_ip"`
	NATType     string    `json:"nat_type"`
	Link        *LinkView `json:"link,omitempty"`
}

type connErr struct {
	kind string // pending | disabled | kicked | net
	msg  string
}

func (e *connErr) Error() string { return e.msg }

// ---------- setup / registration ----------

type SetupRequest struct {
	Server      string `json:"server"`
	Name        string `json:"name"`
	EnrollToken string `json:"enroll_token"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	TOTP        string `json:"totp"`
}

func normalizeServer(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("請輸入伺服器位址")
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("伺服器位址格式不正確")
	}
	return strings.TrimSuffix(u.Scheme+"://"+u.Host+u.Path, "/"), nil
}

// Setup registers this device with a server.
func (a *Agent) Setup(ctx context.Context, req SetupRequest) (StateView, error) {
	server, err := normalizeServer(req.Server)
	if err != nil {
		return StateView{}, err
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return StateView{}, errors.New("請輸入裝置名稱")
	}
	if req.EnrollToken == "" && req.Username == "" {
		return StateView{}, errors.New("請輸入註冊碼，或使用帳號密碼")
	}
	s := a.st.get()
	cl := client.New(server)
	cl.Key = s.identity()
	if s.DeviceID != "" { // re-registering: start from a fresh identity
		a.resetIdentity()
		s = a.st.get()
		cl.Key = s.identity()
	}
	var health map[string]any
	if err := cl.Do(ctx, "GET", "/healthz", nil, &health); err != nil {
		return StateView{}, fmt.Errorf("無法連線到伺服器：%w", err)
	}
	dev, err := cl.Register(ctx, client.RegisterRequest{
		Name: name, OS: a.opt.OS, Kind: a.opt.Kind, BoxPubKey: s.boxPub(),
		EnrollToken: strings.TrimSpace(req.EnrollToken), Username: req.Username, Password: req.Password, TOTP: req.TOTP,
		LanIP: lanIP(), ClientVersion: a.opt.Version,
	})
	if err != nil {
		return StateView{}, friendlyErr(err)
	}
	if err := a.st.update(func(st *State) {
		st.Server, st.DeviceID, st.DID, st.Name, st.Level, st.Status = server, dev.ID, dev.DID, dev.Name, dev.Level, dev.Status
	}); err != nil {
		return StateView{}, err
	}
	a.mu.Lock()
	a.level = dev.Level
	a.token = ""
	a.mu.Unlock()
	a.log.Info("registered", "device", dev.ID, "status", dev.Status, "server", server)
	if dev.Status == "pending" {
		a.setStatus("pending", "")
	} else {
		a.setStatus("connecting", "")
	}
	a.nudge()
	return a.stateView(), nil
}

// Reset forgets the registration, the identity keys and the local history.
func (a *Agent) Reset() {
	a.links.closeAll("device reset")
	a.frpc.stopAll()
	a.disconnect()
	a.resetIdentity()
	for peer := range a.msgs.summaries() {
		a.msgs.clear(peer)
	}
	a.mu.Lock()
	a.peers = map[string]*Peer{}
	a.token, a.level = "", ""
	a.mu.Unlock()
	a.setStatus("setup", "")
	a.nudge()
}

func (a *Agent) resetIdentity() {
	_ = a.st.update(func(s *State) {
		s.DeviceID, s.DID, s.Level, s.Status, s.Identity, s.BoxKey = "", "", "", "", "", ""
	})
	// loadState regenerates missing keys
	if st, err := loadState(a.st.path); err == nil {
		a.st.mu.Lock()
		a.st.s = st.get()
		a.st.mu.Unlock()
	}
}

func friendlyErr(err error) error {
	var ae *client.APIError
	if !errors.As(err, &ae) {
		return err
	}
	msg := map[string]string{
		"invalid_credentials": "帳號或密碼錯誤",
		"mfa_required":        "此帳號需要 MFA 驗證碼",
		"invalid_mfa":         "MFA 驗證碼錯誤",
		"too_many_attempts":   "嘗試次數過多，請稍後再試",
		"forbidden":           "伺服器拒絕：" + ae.Message,
		"invalid_token":       "註冊碼無效或已過期",
		"device_pending":      "裝置正在等待管理員核准",
		"device_disabled":     "裝置已被停用",
		"device_banned":       "裝置已被封鎖",
		"relay_disabled":      "管理員已關閉伺服器中繼",
		"mode_violation":      "僅 P2P 模式的內容不可經伺服器傳送",
		"peer_offline":        "對方不在線上",
	}[ae.Code]
	if msg == "" {
		msg = ae.Message
		if msg == "" {
			msg = ae.Error()
		}
	}
	return errors.New(msg)
}

// ---------- connection loop ----------

func (a *Agent) waitWake(ctx context.Context, d time.Duration) {
	var t <-chan time.Time
	if d > 0 {
		tm := time.NewTimer(d)
		defer tm.Stop()
		t = tm.C
	}
	select {
	case <-ctx.Done():
	case <-a.wake:
	case <-t:
	}
}

func (a *Agent) connLoop(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		s := a.st.get()
		if s.DeviceID == "" {
			a.setStatus("setup", "")
			a.waitWake(ctx, 0)
			continue
		}
		connected, err := a.session(ctx)
		if ctx.Err() != nil {
			return
		}
		if connected {
			backoff = time.Second
		}
		wait := backoff
		var ce *connErr
		if errors.As(err, &ce) {
			switch ce.kind {
			case "pending":
				a.setStatus("pending", "")
				wait = 15 * time.Second
			case "disabled":
				a.setStatus("disabled", ce.msg)
				wait = 5 * time.Minute
			case "kicked":
				a.setStatus("offline", ce.msg)
				wait = 30 * time.Second
			default:
				a.setStatus("offline", ce.msg)
			}
		} else if err != nil {
			a.setStatus("offline", err.Error())
		}
		if err != nil {
			a.log.Warn("disconnected", "err", err, "retry_in", wait)
		}
		if wait == backoff && backoff < time.Minute {
			backoff *= 2
		}
		a.waitWake(ctx, wait)
	}
}

func (a *Agent) login(ctx context.Context) error {
	cl := a.newClient()
	cl.Token = ""
	lctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := cl.Login(lctx); err != nil {
		var ae *client.APIError
		if errors.As(err, &ae) {
			switch ae.Code {
			case "device_pending":
				return &connErr{"pending", "等待管理員核准"}
			case "device_disabled", "device_banned":
				return &connErr{"disabled", friendlyErr(err).Error()}
			case "not_found", "invalid_signature", "invalid_challenge":
				if ae.Status == 404 || ae.Code == "invalid_signature" {
					return &connErr{"disabled", "伺服器不認得此裝置（可能已被刪除），請重新註冊"}
				}
			}
		}
		return &connErr{"net", "登入失敗：" + err.Error()}
	}
	a.mu.Lock()
	a.token = cl.Token
	a.mu.Unlock()
	if a.st.get().Status != "active" {
		_ = a.st.update(func(s *State) { s.Status = "active" })
	}
	return nil
}

func (a *Agent) disconnect() {
	a.mu.Lock()
	c := a.conn
	a.mu.Unlock()
	if c != nil {
		c.Close()
	}
}

// session logs in, connects the WebSocket and serves frames until the
// connection drops. connected reports whether the socket was established.
func (a *Agent) session(ctx context.Context) (connected bool, err error) {
	a.setStatus("connecting", "")
	if err := a.login(ctx); err != nil {
		return false, err
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	conn, err := a.newClient().Connect(cctx)
	cancel()
	if err != nil {
		return false, &connErr{"net", "WebSocket 連線失敗：" + err.Error()}
	}
	kick := ""
	a.mu.Lock()
	a.conn = conn
	a.level = conn.Hello.Level
	a.mu.Unlock()
	_ = a.st.update(func(s *State) { s.Level = conn.Hello.Level; s.Status = "active" })
	a.setStatus("online", "")
	a.log.Info("connected", "device", conn.Hello.DeviceID, "level", conn.Hello.Level, "queued", conn.Hello.Queued)
	go a.afterConnect(ctx)

	hb := time.Duration(conn.Hello.Heartbeat) * time.Second
	if hb <= 0 || hb > time.Minute {
		hb = 25 * time.Second
	}
	ticker := time.NewTicker(hb)
	defer ticker.Stop()
	defer func() {
		a.mu.Lock()
		if a.conn == conn {
			a.conn = nil
		}
		a.mu.Unlock()
		conn.Close()
		a.events.publish("state", a.stateView())
	}()
	for {
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case <-ticker.C:
			_ = conn.Send(proto.TPing, nil)
		case f, ok := <-conn.Frames():
			if !ok {
				if kick != "" {
					return true, &connErr{"kicked", "伺服器中斷連線：" + kick}
				}
				return true, &connErr{"net", "與伺服器的連線中斷"}
			}
			if f.T == proto.TKick {
				var k proto.Kick
				_ = json.Unmarshal(f.D, &k)
				kick = k.Reason
				if strings.Contains(k.Reason, "disabled") || strings.Contains(k.Reason, "banned") || strings.Contains(k.Reason, "deleted") {
					return true, &connErr{"disabled", "裝置已被管理員停用或刪除：" + k.Reason}
				}
				continue
			}
			a.handleFrame(ctx, f)
		}
	}
}

func (a *Agent) afterConnect(ctx context.Context) {
	s := a.st.get()
	lan := lanIP()
	body := map[string]any{"box_pubkey": s.boxPub(), "client_version": a.opt.Version, "os": a.opt.OS, "lan_ip": lan}
	if err := a.api(ctx, "PATCH", "/me/device", body, nil); err != nil {
		a.log.Warn("update device info", "err", err)
	}
	_ = a.sendFrame(proto.TPresenceSet, proto.PresenceSet{LanIP: lan})
	if err := a.refreshPeers(ctx); err != nil {
		a.log.Warn("refresh peers", "err", err)
	}
	a.retryPending()
	if err := a.frpc.reload(ctx); err != nil {
		a.log.Warn("frpc reload", "err", err)
	}
}

func (a *Agent) handleFrame(ctx context.Context, f proto.Frame) {
	switch f.T {
	case proto.TMsgNew:
		var m proto.Message
		if json.Unmarshal(f.D, &m) == nil {
			a.onRelayMessage(ctx, m)
		}
	case proto.TMsgReceipt:
		var r proto.Receipt
		if json.Unmarshal(f.D, &r) == nil {
			for _, sid := range r.IDs {
				if mid := a.msgs.idByServer(sid); mid != "" {
					a.applyReceipt(mid, r.Kind)
				}
			}
		}
	case proto.TPresence:
		var p proto.Presence
		if json.Unmarshal(f.D, &p) == nil {
			a.onPresence(ctx, p)
		}
	case proto.TSignal:
		var s proto.Signal
		if json.Unmarshal(f.D, &s) == nil {
			a.links.onSignal(s)
		}
	case proto.TFileOffer:
		var o proto.FileOffer
		_ = json.Unmarshal(f.D, &o)
		a.log.Info("relay file ready", "file", o.FileID, "from", o.From, "size", o.Size)
	case proto.TCmd:
		var c proto.Cmd
		if json.Unmarshal(f.D, &c) == nil {
			go a.handleCmd(ctx, c)
		}
	case proto.TFRPConfig:
		go func() {
			if err := a.frpc.reload(ctx); err != nil {
				a.log.Warn("frpc reload", "err", err)
			}
		}()
	case proto.TLevelChanged:
		var l proto.LevelChanged
		if json.Unmarshal(f.D, &l) == nil {
			a.mu.Lock()
			a.level = l.Level
			a.mu.Unlock()
			_ = a.st.update(func(s *State) { s.Level = l.Level })
			a.events.publish("level", l)
			a.events.publish("state", a.stateView())
			a.notify("Meshport", "裝置等級已變更為 "+l.Level)
			go func() { _ = a.refreshPeers(ctx); _ = a.frpc.reload(ctx) }()
		}
	case proto.TGrantUpdate:
		var g proto.GrantUpdate
		if json.Unmarshal(f.D, &g) == nil {
			a.events.publish("grant", g)
		}
	case proto.TSessionClose:
		var sc proto.SessionClose
		if json.Unmarshal(f.D, &sc) == nil {
			a.links.closeSID(sc.Session, "管理員中斷了工作階段："+sc.Reason)
		}
	case proto.TError:
		var e proto.Error
		_ = json.Unmarshal(f.D, &e)
		a.log.Warn("server error frame", "code", e.Code, "message", e.Message)
	}
}

func (a *Agent) onPresence(ctx context.Context, p proto.Presence) {
	a.mu.Lock()
	peer := a.peers[p.DeviceID]
	if peer != nil {
		peer.Online = p.Online
		peer.LastSeenAt = time.Unix(p.LastSeen, 0).UTC().Format(time.RFC3339)
	}
	a.mu.Unlock()
	if peer == nil {
		go func() { _ = a.refreshPeers(ctx) }()
		return
	}
	a.events.publish("presence", p)
	if p.Online {
		a.retryPendingFor(p.DeviceID)
	} else {
		a.links.close(p.DeviceID, "對方已離線", false)
	}
}

func (a *Agent) refreshPeers(ctx context.Context) error {
	var out struct {
		Items []*Peer `json:"items"`
	}
	if err := a.api(ctx, "GET", "/peers", nil, &out); err != nil {
		return err
	}
	me := a.myID()
	m := map[string]*Peer{}
	for _, p := range out.Items {
		if p.ID != me {
			m[p.ID] = p
		}
	}
	a.mu.Lock()
	a.peers = m
	a.mu.Unlock()
	a.events.publish("peers", a.peerList())
	return nil
}

func (a *Agent) peer(id string) *Peer {
	a.mu.Lock()
	defer a.mu.Unlock()
	if p := a.peers[id]; p != nil {
		c := *p
		return &c
	}
	return nil
}

// peerFresh returns a peer, refreshing the directory once if unknown or
// missing its encryption key.
func (a *Agent) peerFresh(ctx context.Context, id string) *Peer {
	p := a.peer(id)
	if p == nil || p.BoxPubKey == "" {
		_ = a.refreshPeers(ctx)
		p = a.peer(id)
	}
	return p
}

func (a *Agent) peerName(id string) string {
	if p := a.peer(id); p != nil {
		return p.Name
	}
	return id
}

func (a *Agent) peerList() []Peer {
	a.mu.Lock()
	out := make([]Peer, 0, len(a.peers))
	for _, p := range a.peers {
		out = append(out, *p)
	}
	a.mu.Unlock()
	for i := range out {
		out[i].Link = a.links.view(out[i].ID)
	}
	return out
}

// ---------- remote management ----------

func (a *Agent) handleCmd(ctx context.Context, c proto.Cmd) {
	res := proto.CmdResult{ID: c.ID, OK: true}
	a.log.Info("remote command", "name", c.Name)
	switch c.Name {
	case "ping":
		res.Output = "pong " + a.opt.Version
	case "reload-frpc":
		if err := a.frpc.reload(ctx); err != nil {
			res.OK, res.Output = false, err.Error()
		} else {
			res.Output = a.frpc.summary()
		}
	case "rotate-key":
		if err := a.RotateKey(ctx); err != nil {
			res.OK, res.Output = false, err.Error()
		} else {
			res.Output = "identity key rotated: " + a.st.get().fingerprint()
		}
	case "refresh-peers":
		if err := a.refreshPeers(ctx); err != nil {
			res.OK, res.Output = false, err.Error()
		} else {
			res.Output = fmt.Sprintf("%d peers", len(a.peerList()))
		}
	case "collect-logs":
		n := 200
		if v := c.Args["lines"]; v != "" {
			fmt.Sscanf(v, "%d", &n)
		}
		if n <= 0 || n > 500 {
			n = 200
		}
		res.Output = strings.Join(a.logs.tail(n), "\n")
		if len(res.Output) > 60000 {
			res.Output = res.Output[len(res.Output)-60000:]
		}
	case "disconnect":
		res.Output = "disconnecting"
		_ = a.sendFrame(proto.TCmdResult, res)
		time.Sleep(200 * time.Millisecond)
		a.disconnect()
		return
	case "restart-client":
		res.Output = "restarting client services"
		_ = a.sendFrame(proto.TCmdResult, res)
		a.links.closeAll("client restart")
		a.frpc.stopAll()
		time.Sleep(200 * time.Millisecond)
		a.disconnect()
		return
	default:
		res.OK, res.Output = false, "unsupported command: "+c.Name
	}
	_ = a.sendFrame(proto.TCmdResult, res)
}

// RotateKey replaces the identity key (both keys sign the rotation).
func (a *Agent) RotateKey(ctx context.Context) error {
	cl := a.newClient()
	if cl.Token == "" {
		return errOffline
	}
	if err := cl.RotateKey(ctx); err != nil {
		return friendlyErr(err)
	}
	seed := cl.Key.Seed()
	if err := a.st.update(func(s *State) { s.Identity = base64.StdEncoding.EncodeToString(seed) }); err != nil {
		return err
	}
	a.log.Info("identity key rotated")
	a.events.publish("state", a.stateView())
	return nil
}

// lanIP returns the address of the interface used for outbound traffic.
func lanIP() string {
	c, err := net.Dial("udp", "198.51.100.1:9") // no packets are sent
	if err != nil {
		return ""
	}
	defer c.Close()
	if ua, ok := c.LocalAddr().(*net.UDPAddr); ok {
		return ua.IP.String()
	}
	return ""
}
