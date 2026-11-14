package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"meshport/pkg/client"
	"meshport/pkg/proto"
)

// Delivery modes (proto.ModeP2P / ModeRelay / ModeAuto):
//
//	p2p   – only a direct WebRTC connection without TURN. Messages wait on
//	        this device ("pending") until a direct connection succeeds.
//	relay – always through the server (WebSocket relay, /files uploads).
//	auto  – use an open or quickly established P2P link (TURN allowed),
//	        otherwise fall back to the server relay.

var inflight sync.Map // message id -> struct{}: avoids concurrent deliveries

// SendText queues a text message and starts delivery.
func (a *Agent) SendText(peer, text, mode string) (Message, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Message{}, errors.New("訊息是空的")
	}
	if len(text) > 64<<10 {
		return Message{}, errors.New("訊息過長（上限 64 KB），請改用檔案傳送")
	}
	if a.peer(peer) == nil {
		return Message{}, errors.New("找不到這台裝置，或無權傳訊給它")
	}
	if !validMode(mode) {
		mode = a.st.get().modeFor(peer)
	}
	m := &Message{ID: newID("m"), Peer: peer, Out: true, Kind: "text", Text: text, TS: nowMS(), Mode: mode, Status: "sending"}
	a.msgs.add(m)
	a.events.publish("message", a.msgView(*m))
	go a.deliver(m.ID)
	return a.msgView(*m), nil
}

func (a *Agent) setMsg(id string, fn func(*Message)) {
	if m, ok := a.msgs.update(id, fn); ok {
		a.events.publish("message", a.msgView(m))
	}
}

// msgView hides key material from the UI.
func (a *Agent) msgView(m Message) Message {
	if m.File != nil {
		f := *m.File
		f.Key, f.Nonce = "", ""
		m.File = &f
	}
	return m
}

// deliver sends one outgoing text message according to its mode.
func (a *Agent) deliver(id string) {
	if _, busy := inflight.LoadOrStore(id, struct{}{}); busy {
		return
	}
	defer inflight.Delete(id)
	m, ok := a.msgs.get(id)
	if !ok || !m.Out {
		return
	}
	if m.Kind == "file" {
		a.runFileSend(id)
		return
	}
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()
	a.setMsg(id, func(m *Message) { m.Status, m.Error = "sending", "" })
	p := a.peerFresh(ctx, m.Peer)
	if p == nil {
		a.setMsg(id, func(m *Message) { m.Status, m.Error = "failed", "對方已不在可見範圍內" })
		return
	}
	env := &Envelope{V: 1, K: "text", MID: m.ID, TS: m.TS, Text: m.Text}
	body, err := seal(env, p.BoxPubKey, a.st.get().boxPriv())
	if err != nil {
		a.setMsg(id, func(m *Message) { m.Status, m.Error = "failed", err.Error() })
		return
	}
	switch m.Mode {
	case proto.ModeRelay:
		a.sendViaRelay(ctx, id, p.ID, m.Mode, "text", body)
	case proto.ModeP2P:
		if !p.Online {
			a.setMsg(id, func(m *Message) { m.Status, m.Error = "pending", "等待對方上線以建立直連" })
			return
		}
		l, err := a.links.ensure(ctx, p.ID, proto.ModeP2P, 15*time.Second)
		if err == nil {
			err = l.sendEnvelope(body)
		}
		if err != nil {
			a.setMsg(id, func(m *Message) { m.Status, m.Error = "pending", "直連尚未成功："+err.Error() })
			return
		}
		a.setMsg(id, func(m *Message) { m.Status, m.Path, m.Error = "sent", l.pathName(), "" })
	default: // auto
		if p.Online {
			if l, err := a.links.ensure(ctx, p.ID, proto.ModeAuto, 8*time.Second); err == nil {
				if err := l.sendEnvelope(body); err == nil {
					a.setMsg(id, func(m *Message) { m.Status, m.Path, m.Error = "sent", l.pathName(), "" })
					return
				}
			}
		}
		a.sendViaRelay(ctx, id, p.ID, proto.ModeAuto, "text", body)
	}
}

// sendViaRelay pushes a sealed envelope through the server relay. If the
// server is unreachable the message stays pending and is retried.
func (a *Agent) sendViaRelay(ctx context.Context, id, to, mode, ct, body string) {
	req := proto.MsgSend{To: to, Mode: mode, ClientID: id, ContentType: ct, Body: body}
	var ack proto.MsgAck
	err := a.request(ctx, proto.TMsgSend, req, &ack)
	if errors.Is(err, errOffline) {
		err = a.api(ctx, "POST", "/messages", req, &ack)
	}
	if err != nil {
		var ae *client.APIError
		if errors.As(err, &ae) && ae.Code != "" && ae.Code != "http" {
			if id != "" {
				a.setMsg(id, func(m *Message) { m.Status, m.Error = "failed", friendlyErr(err).Error() })
			}
			return
		}
		if id != "" {
			a.setMsg(id, func(m *Message) { m.Status, m.Error = "pending", "伺服器暫時無法連線，稍後自動重送" })
		}
		return
	}
	if id == "" {
		return
	}
	status := "sent"
	if ack.Status == "queued" {
		status = "queued"
	}
	a.setMsg(id, func(m *Message) {
		m.ServerID, m.Path, m.Error = ack.ID, "relay", ""
		if m.Status != "delivered" && m.Status != "read" {
			m.Status = status
		}
	})
}

func (a *Agent) retryPending() {
	for _, m := range a.msgs.pending() {
		a.retryOne(m)
	}
}

func (a *Agent) retryPendingFor(peer string) {
	for _, m := range a.msgs.pending() {
		if m.Peer == peer {
			a.retryOne(m)
		}
	}
}

func (a *Agent) retryOne(m Message) {
	if a.currentConn() == nil {
		return
	}
	if m.Mode == proto.ModeP2P {
		if p := a.peer(m.Peer); p == nil || !p.Online {
			return
		}
	}
	go a.deliver(m.ID)
}

// Retry re-sends a failed or pending message (from the UI).
func (a *Agent) Retry(id string) error {
	m, ok := a.msgs.get(id)
	if !ok || !m.Out {
		return errors.New("找不到訊息")
	}
	if m.Status != "failed" && m.Status != "pending" {
		return errors.New("訊息已送出")
	}
	go a.deliver(id)
	return nil
}

// ---------- receiving ----------

func (a *Agent) onRelayMessage(ctx context.Context, m proto.Message) {
	// always acknowledge so the server stops re-sending it
	_ = a.sendFrame(proto.TMsgDelivered, proto.Receipt{IDs: []string{m.ID}})
	a.onEnvelope(ctx, m.From, m.Body, "relay", m.ID, m.Mode, nil)
}

// onEnvelope handles a sealed envelope from either transport.
func (a *Agent) onEnvelope(ctx context.Context, from, body, path, serverID, mode string, l *link) {
	p := a.peerFresh(ctx, from)
	if p == nil {
		a.log.Warn("message from unknown device dropped", "from", from)
		return
	}
	env, err := open(body, p.BoxPubKey, a.st.get().boxPriv())
	if err != nil && serverID != "" {
		// the peer may have re-registered with a new key; refresh once
		_ = a.refreshPeers(ctx)
		if p = a.peer(from); p != nil {
			env, err = open(body, p.BoxPubKey, a.st.get().boxPriv())
		}
	}
	if err != nil {
		a.log.Warn("cannot decrypt message", "from", from, "err", err)
		return
	}
	switch env.K {
	case "text", "file":
		if env.MID == "" {
			return
		}
		ts := env.TS
		if ts <= 0 || ts > nowMS()+5*60*1000 {
			ts = nowMS()
		}
		msg := &Message{ID: env.MID, Peer: from, Kind: env.K, Text: env.Text, File: env.File, TS: ts, Mode: mode, Path: path, Status: "received", ServerID: serverID}
		if l != nil {
			msg.Path = l.pathName()
		}
		if !a.msgs.add(msg) {
			// duplicate (e.g. resent); still acknowledge
			a.ackDelivered(from, env.MID, l)
			return
		}
		a.events.publish("message", a.msgView(*msg))
		if env.K == "text" {
			a.notify(p.Name, truncate(env.Text, 120))
			a.ackDelivered(from, env.MID, l)
		} else if env.File != nil {
			a.notify(p.Name, "傳來檔案："+env.File.Name)
			a.startFileReceive(msg.ID, from, env.File, l)
		}
	case "ack":
		for _, id := range env.IDs {
			a.applyReceipt(id, "delivered")
			a.signalAck(id)
		}
	case "read":
		for _, id := range env.IDs {
			a.applyReceipt(id, "read")
		}
	case "accept", "reject":
		if l != nil {
			for _, tid := range env.IDs {
				l.resolveWaiter(tid, env.K)
			}
		}
	}
}

func (a *Agent) ackDelivered(peer, mid string, l *link) {
	if l == nil {
		return // relay messages are acknowledged with msg.delivered
	}
	a.sendControl(peer, &Envelope{V: 1, K: "ack", IDs: []string{mid}}, l)
}

// sendControl seals a control envelope and sends it over the link, or over
// the relay when no link is available and the peer's mode allows it.
func (a *Agent) sendControl(peer string, env *Envelope, l *link) {
	p := a.peer(peer)
	if p == nil {
		return
	}
	body, err := seal(env, p.BoxPubKey, a.st.get().boxPriv())
	if err != nil {
		return
	}
	if l == nil {
		l = a.links.up(peer)
	}
	if l != nil && l.sendEnvelope(body) == nil {
		return
	}
	if a.st.get().modeFor(peer) != proto.ModeP2P {
		ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
		defer cancel()
		a.sendViaRelay(ctx, "", peer, proto.ModeAuto, "receipt", body)
	}
}

var statusRank = map[string]int{"pending": 0, "sending": 1, "failed": 1, "sent": 2, "queued": 2, "delivered": 3, "read": 4}

func (a *Agent) applyReceipt(mid, kind string) {
	a.setMsg(mid, func(m *Message) {
		if m.Out && statusRank[kind] > statusRank[m.Status] {
			m.Status = kind
			m.Error = ""
		}
	})
}

// MarkRead marks a conversation as read and sends read receipts.
func (a *Agent) MarkRead(peer string) {
	seen := a.msgs.markSeen(peer)
	if len(seen) == 0 {
		return
	}
	var relayIDs, directIDs []string
	for _, m := range seen {
		if m.ServerID != "" {
			relayIDs = append(relayIDs, m.ServerID)
		} else {
			directIDs = append(directIDs, m.ID)
		}
	}
	if len(relayIDs) > 0 {
		if err := a.sendFrame(proto.TMsgRead, proto.Receipt{IDs: relayIDs}); err != nil {
			ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
			_ = a.api(ctx, "POST", "/messages/receipts", map[string]any{"ids": relayIDs, "kind": "read"}, nil)
			cancel()
		}
	}
	if len(directIDs) > 0 {
		go a.sendControl(peer, &Envelope{V: 1, K: "read", IDs: directIDs}, nil)
	}
	a.events.publish("read", map[string]any{"peer": peer})
	a.events.publish("state", a.stateView())
}

// ---------- ack waiters (file transfers wait for the receiver's ack) ----------

var ackWaiters sync.Map // mid -> chan struct{}

func waitAck(mid string) chan struct{} {
	ch := make(chan struct{})
	ackWaiters.Store(mid, ch)
	return ch
}

func (a *Agent) signalAck(mid string) {
	if v, ok := ackWaiters.LoadAndDelete(mid); ok {
		close(v.(chan struct{}))
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
