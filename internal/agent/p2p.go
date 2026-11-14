package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"

	"meshport/pkg/proto"
)

// P2P links: one WebRTC peer connection per peer, negotiated through the
// server's signal relay. A reliable, ordered data channel "mesh" carries
// sealed envelopes; each P2P file transfer opens its own channel "f:<tid>".

type iceServers struct {
	servers []webrtc.ICEServer
	expires time.Time
}

func (a *Agent) iceConfig(ctx context.Context, mode string) (webrtc.Configuration, error) {
	a.mu.Lock()
	cache := a.iceCache
	a.mu.Unlock()
	if cache == nil || time.Until(cache.expires) < 10*time.Minute {
		var out struct {
			Servers []struct {
				URLs       []string `json:"urls"`
				Username   string   `json:"username"`
				Credential string   `json:"credential"`
			} `json:"ice_servers"`
			ExpiresAt string `json:"expires_at"`
		}
		if err := a.api(ctx, "GET", "/rtc/ice-servers", nil, &out); err != nil {
			return webrtc.Configuration{}, err
		}
		c := &iceServers{expires: time.Now().Add(time.Hour)}
		if t, err := time.Parse(time.RFC3339, out.ExpiresAt); err == nil {
			c.expires = t
		}
		for _, s := range out.Servers {
			c.servers = append(c.servers, webrtc.ICEServer{URLs: s.URLs, Username: s.Username, Credential: s.Credential})
		}
		a.mu.Lock()
		a.iceCache = c
		a.mu.Unlock()
		cache = c
	}
	cfg := webrtc.Configuration{}
	for _, s := range cache.servers {
		if mode == proto.ModeP2P { // direct only: drop TURN
			var urls []string
			for _, u := range s.URLs {
				if strings.HasPrefix(u, "stun:") || strings.HasPrefix(u, "stuns:") {
					urls = append(urls, u)
				}
			}
			if len(urls) > 0 {
				cfg.ICEServers = append(cfg.ICEServers, webrtc.ICEServer{URLs: urls})
			}
			continue
		}
		cfg.ICEServers = append(cfg.ICEServers, s)
	}
	return cfg, nil
}

type LinkView struct {
	SID     string `json:"sid"`
	State   string `json:"state"` // nego | up | fail | closed
	Mode    string `json:"mode"`
	Path    string `json:"path"` // p2p | turn
	Via     string `json:"via"`
	Latency *int   `json:"lat_ms,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

type link struct {
	lm        *linkManager
	peer      string
	sid       string
	mode      string
	initiator bool
	created   time.Time

	mu          sync.Mutex
	pc          *webrtc.PeerConnection
	ctrl        *webrtc.DataChannel
	remoteSet   bool
	remoteCands []webrtc.ICECandidateInit
	localCands  []webrtc.ICECandidateInit
	localSent   bool // offer/answer sent; candidates can flow
	state       string
	path        string
	via         string
	reason      string
	replaced    bool
	latency     *int
	lastActive  time.Time
	bytes       int64
	lastBytes   int64
	rate        int64
	sent        int
	delivered   int
	waiters     map[string]chan string
	recv        map[string]*incomingP2P

	ready     chan struct{}
	done      chan struct{}
	readyOnce sync.Once
	closeOnce sync.Once
}

type linkManager struct {
	a     *Agent
	api   *webrtc.API
	mu    sync.Mutex
	links map[string]*link
}

func newLinkManager(a *Agent) *linkManager {
	se := webrtc.SettingEngine{}
	se.SetICETimeouts(6*time.Second, 15*time.Second, 2*time.Second)
	return &linkManager{a: a, api: webrtc.NewAPI(webrtc.WithSettingEngine(se)), links: map[string]*link{}}
}

func (lm *linkManager) newLink(peer, sid, mode string, initiator bool) *link {
	return &link{lm: lm, peer: peer, sid: sid, mode: mode, initiator: initiator, created: time.Now(), state: "nego",
		lastActive: time.Now(), waiters: map[string]chan string{}, recv: map[string]*incomingP2P{},
		ready: make(chan struct{}), done: make(chan struct{})}
}

// up returns an open link to the peer, if any.
func (lm *linkManager) up(peer string) *link {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	if l := lm.links[peer]; l != nil && l.isUp() {
		return l
	}
	return nil
}

func (lm *linkManager) view(peer string) *LinkView {
	lm.mu.Lock()
	l := lm.links[peer]
	lm.mu.Unlock()
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return &LinkView{SID: l.sid, State: l.state, Mode: l.mode, Path: l.path, Via: l.via, Latency: l.latency, Reason: l.reason}
}

// ensure returns an open link, dialing if needed, waiting at most timeout.
// A negotiation that outlives the timeout keeps running in the background.
func (lm *linkManager) ensure(ctx context.Context, peer, mode string, timeout time.Duration) (*link, error) {
	if lm.a.currentConn() == nil {
		return nil, errOffline
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		lm.mu.Lock()
		l := lm.links[peer]
		if l != nil && mode == proto.ModeP2P && l.isUp() && l.pathName() == "turn" {
			// p2p-only content must not cross a TURN relay: renegotiate without TURN
			delete(lm.links, peer)
			go l.close("switching to direct-only", true, false)
			l = nil
		}
		if l == nil {
			l = lm.newLink(peer, newID("ses"), mode, true)
			lm.links[peer] = l
			go l.dial()
		}
		lm.mu.Unlock()
		select {
		case <-l.ready:
			return l, nil
		case <-l.done:
			l.mu.Lock()
			replaced, reason := l.replaced, l.reason
			l.mu.Unlock()
			if replaced {
				continue
			}
			return nil, errors.New(reason)
		case <-ctx.Done():
			return nil, errors.New("連線協商逾時")
		}
	}
}

func (lm *linkManager) close(peer, reason string, sendBye bool) {
	lm.mu.Lock()
	l := lm.links[peer]
	lm.mu.Unlock()
	if l != nil {
		l.close(reason, false, sendBye)
	}
}

func (lm *linkManager) closeSID(sid, reason string) {
	lm.mu.Lock()
	var target *link
	for _, l := range lm.links {
		if l.sid == sid {
			target = l
		}
	}
	lm.mu.Unlock()
	if target != nil {
		target.close(reason, false, true)
	}
}

func (lm *linkManager) closeAll(reason string) {
	lm.mu.Lock()
	all := make([]*link, 0, len(lm.links))
	for _, l := range lm.links {
		all = append(all, l)
	}
	lm.mu.Unlock()
	for _, l := range all {
		l.close(reason, false, true)
	}
}

func (lm *linkManager) find(peer, sid string) *link {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	if l := lm.links[peer]; l != nil && l.sid == sid {
		return l
	}
	return nil
}

// onSignal handles offer / answer / candidate / bye from a peer.
func (lm *linkManager) onSignal(s proto.Signal) {
	a := lm.a
	switch s.Kind {
	case "offer":
		myMode := a.st.get().modeFor(s.From)
		if myMode == proto.ModeRelay {
			_ = a.sendFrame(proto.TSignal, proto.Signal{To: s.From, Session: s.Session, Kind: "bye", Data: jsonString("peer uses relay-only mode")})
			return
		}
		mode := proto.ModeAuto
		if s.Mode == proto.ModeP2P || myMode == proto.ModeP2P {
			mode = proto.ModeP2P
		}
		lm.mu.Lock()
		cur := lm.links[s.From]
		if cur != nil && cur.sid == s.Session {
			lm.mu.Unlock()
			return
		}
		if cur != nil {
			cur.mu.Lock()
			negotiating := cur.initiator && cur.state == "nego"
			cur.mu.Unlock()
			if negotiating && a.myID() > s.From {
				lm.mu.Unlock() // glare: the larger device id keeps its own offer
				return
			}
			cur.mu.Lock()
			cur.replaced = true
			cur.mu.Unlock()
			delete(lm.links, s.From)
			go cur.close("replaced by a new session", false, false)
		}
		l := lm.newLink(s.From, s.Session, mode, false)
		lm.links[s.From] = l
		lm.mu.Unlock()
		go l.accept(s.Data)
	case "answer":
		if l := lm.find(s.From, s.Session); l != nil {
			go l.onAnswer(s.Data)
		}
	case "candidate":
		if l := lm.find(s.From, s.Session); l != nil {
			var c webrtc.ICECandidateInit
			if json.Unmarshal(s.Data, &c) == nil {
				l.addRemoteCandidate(c)
			}
		}
	case "bye":
		if l := lm.find(s.From, s.Session); l != nil {
			var why string
			_ = json.Unmarshal(s.Data, &why)
			if why == "" {
				why = "對方結束了連線"
			}
			l.close(why, false, false)
		}
	}
}

func jsonString(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

// tick runs every 5 s: pings, rate, session reports, timeouts, idle close.
func (lm *linkManager) tick() {
	lm.mu.Lock()
	all := make([]*link, 0, len(lm.links))
	for _, l := range lm.links {
		all = append(all, l)
	}
	lm.mu.Unlock()
	for _, l := range all {
		l.mu.Lock()
		state, created, idle := l.state, l.created, time.Since(l.lastActive)
		l.rate = (l.bytes - l.lastBytes) / 5
		l.lastBytes = l.bytes
		busy := len(l.recv) > 0 || len(l.waiters) > 0
		l.mu.Unlock()
		switch {
		case state == "nego" && time.Since(created) > 25*time.Second:
			l.close("ICE 協商逾時（雙方 NAT 可能無法直接穿透）", true, true)
		case state == "up" && idle > 10*time.Minute && !busy && lm.a.xfers.active(l.peer) == nil:
			l.close("閒置", false, true)
		case state == "up":
			l.sendCtrl(map[string]any{"t": "ping", "ts": nowMS()})
			l.report("")
		}
	}
}

// ---------- link lifecycle ----------

func (l *link) isUp() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state == "up" && l.ctrl != nil
}

func (l *link) pathName() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.path == "" {
		return "p2p"
	}
	return l.path
}

func (l *link) newPC(ctx context.Context) (*webrtc.PeerConnection, error) {
	cfg, err := l.lm.a.iceConfig(ctx, l.mode)
	if err != nil {
		return nil, fmt.Errorf("取得 ICE 伺服器失敗：%w", err)
	}
	pc, err := l.lm.api.NewPeerConnection(cfg)
	if err != nil {
		return nil, err
	}
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		init := c.ToJSON()
		l.mu.Lock()
		if !l.localSent {
			l.localCands = append(l.localCands, init)
			l.mu.Unlock()
			return
		}
		l.mu.Unlock()
		l.signal("candidate", init)
	})
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		switch s {
		case webrtc.PeerConnectionStateConnected:
			l.updatePath()
			l.report("")
		case webrtc.PeerConnectionStateDisconnected:
			l.report("")
		case webrtc.PeerConnectionStateFailed:
			l.close("ICE 連線失敗", true, true)
		case webrtc.PeerConnectionStateClosed:
			l.close("連線已關閉", false, false)
		}
	})
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		if dc.Label() == "mesh" {
			l.wireCtrl(dc)
		} else if strings.HasPrefix(dc.Label(), "f:") {
			l.lm.a.onFileChannel(l, dc)
		}
	})
	l.mu.Lock()
	l.pc = pc
	l.mu.Unlock()
	return pc, nil
}

func (l *link) dial() {
	ctx, cancel := context.WithTimeout(l.lm.a.ctx, 20*time.Second)
	defer cancel()
	pc, err := l.newPC(ctx)
	if err != nil {
		l.close(err.Error(), true, false)
		return
	}
	dc, err := pc.CreateDataChannel("mesh", nil)
	if err != nil {
		l.close(err.Error(), true, false)
		return
	}
	l.wireCtrl(dc)
	offer, err := pc.CreateOffer(nil)
	if err == nil {
		err = pc.SetLocalDescription(offer)
	}
	if err != nil {
		l.close("建立 offer 失敗："+err.Error(), true, false)
		return
	}
	data, _ := json.Marshal(offer)
	err = l.lm.a.request(ctx, proto.TSignal, proto.Signal{To: l.peer, Session: l.sid, Kind: "offer", Mode: l.mode, Data: data}, nil)
	if err != nil {
		l.close("信令失敗："+friendlyErr(err).Error(), true, false)
		return
	}
	l.flushLocalCandidates()
	l.report("")
}

func (l *link) accept(data json.RawMessage) {
	ctx, cancel := context.WithTimeout(l.lm.a.ctx, 20*time.Second)
	defer cancel()
	pc, err := l.newPC(ctx)
	if err != nil {
		l.close(err.Error(), true, true)
		return
	}
	var offer webrtc.SessionDescription
	if err := json.Unmarshal(data, &offer); err != nil {
		l.close("無效的 offer", true, true)
		return
	}
	if err := pc.SetRemoteDescription(offer); err != nil {
		l.close("套用 offer 失敗："+err.Error(), true, true)
		return
	}
	l.flushRemoteCandidates()
	answer, err := pc.CreateAnswer(nil)
	if err == nil {
		err = pc.SetLocalDescription(answer)
	}
	if err != nil {
		l.close("建立 answer 失敗："+err.Error(), true, true)
		return
	}
	l.signal("answer", answer)
	l.flushLocalCandidates()
}

func (l *link) onAnswer(data json.RawMessage) {
	var ans webrtc.SessionDescription
	if err := json.Unmarshal(data, &ans); err != nil {
		return
	}
	l.mu.Lock()
	pc := l.pc
	l.mu.Unlock()
	if pc == nil {
		return
	}
	if err := pc.SetRemoteDescription(ans); err != nil {
		l.close("套用 answer 失敗："+err.Error(), true, true)
		return
	}
	l.flushRemoteCandidates()
}

func (l *link) addRemoteCandidate(c webrtc.ICECandidateInit) {
	l.mu.Lock()
	if l.pc == nil || !l.remoteSet {
		l.remoteCands = append(l.remoteCands, c)
		l.mu.Unlock()
		return
	}
	pc := l.pc
	l.mu.Unlock()
	_ = pc.AddICECandidate(c)
}

func (l *link) flushRemoteCandidates() {
	l.mu.Lock()
	l.remoteSet = true
	cands, pc := l.remoteCands, l.pc
	l.remoteCands = nil
	l.mu.Unlock()
	for _, c := range cands {
		_ = pc.AddICECandidate(c)
	}
}

func (l *link) flushLocalCandidates() {
	l.mu.Lock()
	l.localSent = true
	cands := l.localCands
	l.localCands = nil
	l.mu.Unlock()
	for _, c := range cands {
		l.signal("candidate", c)
	}
}

func (l *link) signal(kind string, v any) {
	data, _ := json.Marshal(v)
	_ = l.lm.a.sendFrame(proto.TSignal, proto.Signal{To: l.peer, Session: l.sid, Kind: kind, Mode: l.mode, Data: data})
}

func (l *link) wireCtrl(dc *webrtc.DataChannel) {
	dc.OnOpen(func() {
		l.mu.Lock()
		if l.state == "closed" || l.state == "fail" {
			l.mu.Unlock()
			return
		}
		l.ctrl = dc
		l.state = "up"
		l.lastActive = time.Now()
		l.mu.Unlock()
		l.updatePath()
		l.readyOnce.Do(func() { close(l.ready) })
		l.lm.a.log.Info("p2p link up", "peer", l.peer, "path", l.pathName(), "via", l.viaText())
		l.report("")
		l.lm.a.events.publish("link", map[string]any{"peer": l.peer, "link": l.lm.view(l.peer)})
		l.lm.a.retryPendingFor(l.peer)
	})
	dc.OnClose(func() { l.close("資料通道已關閉", false, false) })
	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		if !msg.IsString {
			return
		}
		l.mu.Lock()
		l.lastActive = time.Now()
		l.bytes += int64(len(msg.Data))
		l.mu.Unlock()
		var f struct {
			T  string `json:"t"`
			B  string `json:"b"`
			TS int64  `json:"ts"`
		}
		if json.Unmarshal(msg.Data, &f) != nil {
			return
		}
		switch f.T {
		case "m":
			l.lm.a.onEnvelope(l.lm.a.ctx, l.peer, f.B, l.pathName(), "", l.mode, l)
		case "ping":
			l.sendCtrl(map[string]any{"t": "pong", "ts": f.TS})
		case "pong":
			ms := int(nowMS() - f.TS)
			l.mu.Lock()
			l.latency = &ms
			l.mu.Unlock()
		}
	})
}

func (l *link) viaText() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.via
}

func (l *link) updatePath() {
	l.mu.Lock()
	pc := l.pc
	l.mu.Unlock()
	if pc == nil || pc.SCTP() == nil || pc.SCTP().Transport() == nil {
		return
	}
	pair, err := pc.SCTP().Transport().ICETransport().GetSelectedCandidatePair()
	if err != nil || pair == nil || pair.Local == nil || pair.Remote == nil {
		return
	}
	path, via := "p2p", ""
	switch {
	case pair.Local.Typ == webrtc.ICECandidateTypeRelay || pair.Remote.Typ == webrtc.ICECandidateTypeRelay:
		path = "turn"
		addr := pair.Local.Address
		if pair.Remote.Typ == webrtc.ICECandidateTypeRelay {
			addr = pair.Remote.Address
		}
		via = "TURN relay · " + addr
	case pair.Local.Typ == webrtc.ICECandidateTypeHost && pair.Remote.Typ == webrtc.ICECandidateTypeHost:
		via = fmt.Sprintf("LAN direct · %s:%d", pair.Remote.Address, pair.Remote.Port)
	default:
		via = fmt.Sprintf("UDP hole punch · %s:%d", pair.Remote.Address, pair.Remote.Port)
	}
	if pair.Local.Protocol == webrtc.ICEProtocolTCP {
		via = strings.Replace(via, "UDP", "TCP", 1)
	}
	l.mu.Lock()
	l.path, l.via = path, via
	l.mu.Unlock()
}

func (l *link) sendCtrl(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	l.mu.Lock()
	dc := l.ctrl
	l.mu.Unlock()
	if dc == nil {
		return errors.New("P2P 連線尚未建立")
	}
	if err := dc.SendText(string(b)); err != nil {
		return err
	}
	l.mu.Lock()
	l.bytes += int64(len(b))
	l.mu.Unlock()
	return nil
}

func (l *link) sendEnvelope(body string) error {
	if err := l.sendCtrl(map[string]string{"t": "m", "b": body}); err != nil {
		return err
	}
	l.mu.Lock()
	l.sent++
	l.lastActive = time.Now()
	l.mu.Unlock()
	return nil
}

func (l *link) addWaiter(tid string) chan string {
	ch := make(chan string, 1)
	l.mu.Lock()
	l.waiters[tid] = ch
	l.mu.Unlock()
	return ch
}

func (l *link) dropWaiter(tid string) {
	l.mu.Lock()
	delete(l.waiters, tid)
	l.mu.Unlock()
}

func (l *link) resolveWaiter(tid, v string) {
	l.mu.Lock()
	ch := l.waiters[tid]
	delete(l.waiters, tid)
	l.mu.Unlock()
	if ch != nil {
		ch <- v
	}
}

func (l *link) addBytes(n int) {
	l.mu.Lock()
	l.bytes += int64(n)
	l.lastActive = time.Now()
	l.mu.Unlock()
}

// close tears the link down once. failed marks a negotiation/ICE failure.
func (l *link) close(reason string, failed, sendBye bool) {
	l.closeOnce.Do(func() {
		l.mu.Lock()
		if failed {
			l.state = "fail"
		} else {
			l.state = "closed"
		}
		l.reason = reason
		pc := l.pc
		waiters := l.waiters
		l.waiters = map[string]chan string{}
		l.mu.Unlock()
		for _, ch := range waiters {
			select {
			case ch <- "closed":
			default:
			}
		}
		lm := l.lm
		lm.mu.Lock()
		if lm.links[l.peer] == l {
			delete(lm.links, l.peer)
		}
		lm.mu.Unlock()
		close(l.done)
		if sendBye {
			_ = lm.a.sendFrame(proto.TSignal, proto.Signal{To: l.peer, Session: l.sid, Kind: "bye", Data: jsonString(reason)})
		}
		l.report(reason)
		if pc != nil {
			go pc.Close()
		}
		lm.a.log.Info("p2p link closed", "peer", l.peer, "reason", reason, "failed", failed)
		lm.a.events.publish("link", map[string]any{"peer": l.peer, "link": nil, "reason": reason})
	})
}

// report sends the session.report for this link. Both sides report; the
// server keeps one row per session id.
func (l *link) report(reason string) {
	l.mu.Lock()
	r := proto.SessionReport{Session: l.sid, Peer: l.peer, Mode: l.mode, Via: l.via, Latency: l.latency, RateBps: l.rate,
		Sent: l.sent, Delivered: l.delivered, Reason: reason}
	switch l.state {
	case "up":
		r.State = "up"
		r.Path = "p2p"
		if l.path == "turn" {
			r.Path = "relay"
		}
	case "nego":
		r.State, r.Path = "nego", "nego"
		if r.Via == "" {
			r.Via = "ICE · negotiating"
		}
	case "fail":
		r.State, r.Path = "fail", "fail"
	default:
		r.State, r.Path = "closed", "idle"
	}
	l.mu.Unlock()
	if t := l.lm.a.xfers.active(l.peer); t != nil {
		r.File = &proto.File{Name: t.Name, Size: t.Size, Done: t.Done, RateBps: t.RateBps, Direction: t.Dir}
	}
	l.lm.a.reportSession(r)
}
