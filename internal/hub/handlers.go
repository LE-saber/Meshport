package hub

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"meshport/internal/policy"
	"meshport/internal/store"
	"meshport/pkg/proto"
)

func (h *Hub) handle(c *Conn, f *proto.Frame) error {
	switch f.T {
	case proto.TPing:
		c.write(proto.TPong, f.ID, map[string]int64{"ts": time.Now().UnixMilli()})
		return nil
	case proto.TMsgSend:
		var m proto.MsgSend
		if err := json.Unmarshal(f.D, &m); err != nil {
			return perr("bad_request", "invalid msg.send payload")
		}
		ack, err := h.relayMessage(c.Device(), &m)
		if err != nil {
			return err
		}
		c.write(proto.TAck, f.ID, ack)
		return nil
	case proto.TMsgDelivered, proto.TMsgRead:
		var r proto.Receipt
		if err := json.Unmarshal(f.D, &r); err != nil || len(r.IDs) == 0 || len(r.IDs) > 500 {
			return perr("bad_request", "receipt needs 1..500 ids")
		}
		kind := "delivered"
		if f.T == proto.TMsgRead {
			kind = "read"
		}
		h.receipt(c.Device(), r.IDs, kind)
		if f.ID != "" {
			c.write(proto.TAck, f.ID, nil)
		}
		return nil
	case proto.TSignal:
		var s proto.Signal
		if err := json.Unmarshal(f.D, &s); err != nil || s.To == "" || s.Session == "" || s.Kind == "" {
			return perr("bad_request", "signal needs to, sid and kind")
		}
		if err := h.signal(c.Device(), &s); err != nil {
			return err
		}
		if f.ID != "" {
			c.write(proto.TAck, f.ID, nil)
		}
		return nil
	case proto.TSessionReport:
		var r proto.SessionReport
		if err := json.Unmarshal(f.D, &r); err != nil || r.Session == "" || r.Peer == "" {
			return perr("bad_request", "session.report needs sid and peer")
		}
		if err := h.SessionReport(c.Device(), &r); err != nil {
			return err
		}
		if f.ID != "" {
			c.write(proto.TAck, f.ID, nil)
		}
		return nil
	case proto.TCmdResult:
		var r proto.CmdResult
		if err := json.Unmarshal(f.D, &r); err != nil {
			return perr("bad_request", "invalid cmd.result")
		}
		h.cmdMu.Lock()
		ch := h.pending[r.ID]
		h.cmdMu.Unlock()
		if ch != nil {
			select {
			case ch <- r:
			default:
			}
		}
		h.bus.Publish("device.cmd_result", map[string]any{"device_id": c.device.ID, "cmd_id": r.ID, "ok": r.OK, "output": r.Output})
		return nil
	case proto.TPresenceSet:
		var p proto.PresenceSet
		if err := json.Unmarshal(f.D, &p); err != nil {
			return perr("bad_request", "invalid presence.set")
		}
		_, err := h.db.Exec(`UPDATE devices SET lan_ip=COALESCE(NULLIF(?,''),lan_ip), nat_type=COALESCE(NULLIF(?,''),nat_type) WHERE id=?`, trim(p.LanIP, 64), trim(p.NATType, 32), c.device.ID)
		if f.ID != "" {
			c.write(proto.TAck, f.ID, nil)
		}
		return err
	default:
		return perr("unknown_type", "unknown frame type %q", f.T)
	}
}

func trim(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// peer loads a target device and checks that from may talk to it.
func (h *Hub) peer(from *store.Device, to string) (*store.Device, error) {
	t, err := h.db.DeviceByID(to)
	if errors.Is(err, store.ErrNotFound) {
		return nil, perr("not_found", "peer device not found")
	}
	if err != nil {
		return nil, err
	}
	if !policy.CanMessage(from, t, h.db.Settings().DiscoveryScope) {
		return nil, perr("forbidden", "you are not allowed to reach this device")
	}
	return t, nil
}

// relayMessage stores and forwards a message through the server relay.
func (h *Hub) relayMessage(from *store.Device, m *proto.MsgSend) (*proto.MsgAck, error) {
	switch m.Mode {
	case proto.ModeRelay, proto.ModeAuto:
	case proto.ModeP2P:
		return nil, perr("mode_violation", "messages in p2p-only mode must not be sent through the server")
	default:
		return nil, perr("bad_request", "mode must be relay or auto")
	}
	if !h.db.Settings().RelayEnabled {
		return nil, perr("relay_disabled", "the server relay is disabled by the administrator")
	}
	if m.Body == "" || len(m.Body) > MaxMessageBody {
		return nil, perr("bad_request", "body must be 1..%d bytes of base64; use the file API for larger payloads", MaxMessageBody)
	}
	if _, err := base64.StdEncoding.DecodeString(m.Body); err != nil {
		return nil, perr("bad_request", "body must be base64")
	}
	to, err := h.peer(from, m.To)
	if err != nil {
		return nil, err
	}
	ct := m.ContentType
	if ct == "" {
		ct = "text"
	}
	msg := proto.Message{ID: store.NewID("msg"), From: from.ID, To: to.ID, Mode: m.Mode, ContentType: trim(ct, 100), Body: m.Body, CreatedAt: time.Now().Unix()}
	if _, err := h.db.Exec(`INSERT INTO messages (id, client_id, from_id, to_id, mode, content_type, body, size, status, created_at) VALUES (?,?,?,?,?,?,?,?,'queued',?)`,
		msg.ID, trim(m.ClientID, 100), msg.From, msg.To, msg.Mode, msg.ContentType, msg.Body, len(msg.Body), msg.CreatedAt); err != nil {
		return nil, err
	}
	h.RelayBytes.Add(int64(len(m.Body)))
	h.touchRelaySession(from.ID, to.ID, m.Mode, m.Session, "sent")
	status := "queued"
	if h.Send(to.ID, proto.TMsgNew, msg) {
		status = "relayed"
	}
	return &proto.MsgAck{ClientID: m.ClientID, ID: msg.ID, Status: status}, nil
}

// flushQueue delivers queued relay messages to a device that just connected.
func (h *Hub) flushQueue(c *Conn) {
	rows, err := h.db.Query(`SELECT id, from_id, to_id, mode, content_type, body, created_at FROM messages WHERE to_id=? AND status='queued' ORDER BY created_at, rowid LIMIT 1000`, c.device.ID)
	if err != nil {
		h.log.Error("queue query failed", "err", err)
		return
	}
	var list []proto.Message
	for rows.Next() {
		var m proto.Message
		if err := rows.Scan(&m.ID, &m.From, &m.To, &m.Mode, &m.ContentType, &m.Body, &m.CreatedAt); err == nil {
			list = append(list, m)
		}
	}
	rows.Close()
	for _, m := range list {
		c.write(proto.TMsgNew, "", m)
	}
	// relay files waiting for this device
	frows, err := h.db.Query(`SELECT id, from_id, name, size, sha256 FROM files WHERE to_id=? AND status='ready'`, c.device.ID)
	if err != nil {
		return
	}
	var offers []proto.FileOffer
	for frows.Next() {
		var o proto.FileOffer
		if err := frows.Scan(&o.FileID, &o.From, &o.Name, &o.Size, &o.SHA256); err == nil {
			offers = append(offers, o)
		}
	}
	frows.Close()
	for _, o := range offers {
		c.write(proto.TFileOffer, "", o)
	}
}

// receipt marks messages delivered/read by their recipient and notifies senders.
func (h *Hub) receipt(by *store.Device, ids []string, kind string) {
	now := time.Now().Unix()
	bySender := map[string][]string{}
	for _, id := range ids {
		var from, status string
		err := h.db.QueryRow(`SELECT from_id, status FROM messages WHERE id=? AND to_id=?`, id, by.ID).Scan(&from, &status)
		if err != nil {
			continue
		}
		if kind == "delivered" && status == "queued" {
			_, _ = h.db.Exec(`UPDATE messages SET status='delivered', delivered_at=? WHERE id=?`, now, id)
			h.touchRelaySession(from, by.ID, "", "", "delivered")
		} else if kind == "read" && status != "read" {
			_, _ = h.db.Exec(`UPDATE messages SET status='read', read_at=?, delivered_at=CASE WHEN delivered_at=0 THEN ? ELSE delivered_at END WHERE id=?`, now, now, id)
			if status == "queued" {
				h.touchRelaySession(from, by.ID, "", "", "delivered")
			}
			h.touchRelaySession(from, by.ID, "", "", "read")
		} else {
			continue
		}
		bySender[from] = append(bySender[from], id)
	}
	for sender, list := range bySender {
		h.Send(sender, proto.TMsgReceipt, proto.Receipt{IDs: list, Kind: kind, By: by.ID})
	}
}

// touchRelaySession keeps message counters for the server-relayed path of a pair.
func (h *Hub) touchRelaySession(from, to, mode, sid, counter string) {
	now := time.Now().Unix()
	var id string
	var err error
	if sid != "" {
		err = h.db.QueryRow(`SELECT id FROM comm_sessions WHERE id=? AND ((a_id=? AND b_id=?) OR (a_id=? AND b_id=?))`, sid, from, to, to, from).Scan(&id)
	} else {
		err = h.db.QueryRow(`SELECT id FROM comm_sessions WHERE ((a_id=? AND b_id=?) OR (a_id=? AND b_id=?)) AND state!='closed' ORDER BY updated_at DESC LIMIT 1`, from, to, to, from).Scan(&id)
	}
	if errors.Is(err, sql.ErrNoRows) {
		if counter != "sent" {
			return
		}
		if sid == "" {
			sid = store.NewID("ses")
		}
		id = sid
		if mode == "" {
			mode = proto.ModeRelay
		}
		_, err = h.db.Exec(`INSERT INTO comm_sessions (id, a_id, b_id, mode, path, via, state, started_at, updated_at) VALUES (?,?,?,?, 'relay', 'server relay · wss', 'up', ?, ?)`, id, from, to, mode, now, now)
		if err != nil {
			return
		}
		h.bus.Publish("session.update", map[string]any{"id": id})
	} else if err != nil {
		return
	}
	col := map[string]string{"sent": "msgs_sent", "delivered": "msgs_delivered", "read": "msgs_read", "failed": "msgs_failed"}[counter]
	if col == "" {
		return
	}
	_, _ = h.db.Exec(`UPDATE comm_sessions SET `+col+`=`+col+`+1, updated_at=? WHERE id=?`, now, id)
}

// signal forwards a P2P negotiation frame to the peer.
func (h *Hub) signal(from *store.Device, s *proto.Signal) error {
	if len(s.Data) > 64*1024 {
		return perr("bad_request", "signal payload too large")
	}
	to, err := h.peer(from, s.To)
	if err != nil {
		return err
	}
	out := *s
	out.From = from.ID
	out.To = ""
	if !h.Send(to.ID, proto.TSignal, out) {
		h.Failure("p2p", from.ID, to.ID, "peer offline", "signal "+s.Kind)
		return perr("peer_offline", "peer is offline")
	}
	h.SignalCount.Add(1)
	if s.Kind == "offer" {
		mode := s.Mode
		if mode != proto.ModeP2P && mode != proto.ModeRelay {
			mode = proto.ModeAuto
		}
		now := time.Now().Unix()
		res, err := h.db.Exec(`UPDATE comm_sessions SET state='nego', path='nego', p2p_attempted=1, mode=?, updated_at=? WHERE id=? AND ((a_id=? AND b_id=?) OR (a_id=? AND b_id=?))`, mode, now, s.Session, from.ID, to.ID, to.ID, from.ID)
		if err == nil {
			if n, _ := res.RowsAffected(); n == 0 && !h.sessionExists(s.Session) {
				_, _ = h.db.Exec(`INSERT INTO comm_sessions (id, a_id, b_id, mode, path, via, state, p2p_attempted, started_at, updated_at) VALUES (?,?,?,?, 'nego', 'ICE · negotiating', 'nego', 1, ?, ?)`, s.Session, from.ID, to.ID, mode, now, now)
				h.bus.Publish("session.update", map[string]any{"id": s.Session})
			}
		}
	}
	return nil
}

func (h *Hub) sessionExists(id string) bool {
	return h.db.Count(`SELECT COUNT(*) FROM comm_sessions WHERE id=?`, id) > 0
}

// Failure records a connection failure for the monitoring page.
func (h *Hub) Failure(kind, a, b, reason, detail string) {
	_, _ = h.db.Exec(`INSERT INTO failures (kind, a_id, b_id, reason, detail, created_at) VALUES (?,?,?,?,?,?)`, kind, a, b, trim(reason, 200), trim(detail, 500), time.Now().Unix())
	h.bus.Publish("failure", map[string]any{"kind": kind, "a": a, "b": b, "reason": reason})
}

var validPath = map[string]bool{"p2p": true, "relay": true, "nego": true, "fail": true, "idle": true}
var validState = map[string]bool{"up": true, "nego": true, "fail": true, "idle": true, "closed": true}

// SessionReport upserts the transport telemetry a device reports for a conversation.
func (h *Hub) SessionReport(from *store.Device, r *proto.SessionReport) error {
	if len(r.Session) > 64 || strings.ContainsAny(r.Session, " \n\t") {
		return perr("bad_request", "invalid sid")
	}
	if !validPath[r.Path] || !validState[r.State] {
		return perr("bad_request", "invalid path or state")
	}
	if r.Mode != proto.ModeP2P && r.Mode != proto.ModeRelay && r.Mode != proto.ModeAuto {
		return perr("bad_request", "invalid mode")
	}
	to, err := h.peer(from, r.Peer)
	if err != nil {
		return err
	}
	var a, b string
	err = h.db.QueryRow(`SELECT a_id, b_id FROM comm_sessions WHERE id=?`, r.Session).Scan(&a, &b)
	now := time.Now().Unix()
	var file any = "null"
	if r.File != nil {
		pct := 0
		if r.File.Size > 0 {
			pct = int(r.File.Done * 100 / r.File.Size)
		}
		fb, _ := json.Marshal(store.FileProgress{Name: trim(r.File.Name, 200), Size: r.File.Size, Done: r.File.Done, Percent: pct, RateBps: r.File.RateBps, Direction: r.File.Direction})
		file = string(fb)
	}
	attempted := r.Mode != proto.ModeRelay
	ok := r.Path == "p2p" && r.State == "up"
	ended := int64(0)
	if r.State == "closed" {
		ended = now
	}
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = h.db.Exec(`INSERT INTO comm_sessions (id, a_id, b_id, mode, path, via, state, latency_ms, rate_bps, msgs_sent, msgs_delivered, msgs_read, msgs_failed, file, p2p_attempted, p2p_ok, fail_reason, started_at, updated_at, ended_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			r.Session, from.ID, to.ID, r.Mode, r.Path, trim(r.Via, 120), r.State, r.Latency, r.RateBps, r.Sent, r.Delivered, r.Read, r.Failed, file, attempted, ok, trim(r.Reason, 200), now, now, ended)
	case err != nil:
		return err
	default:
		if !((a == from.ID && b == to.ID) || (a == to.ID && b == from.ID)) {
			return perr("forbidden", "session belongs to other devices")
		}
		_, err = h.db.Exec(`UPDATE comm_sessions SET mode=?, path=?, via=?, state=?, latency_ms=?, rate_bps=?,
			msgs_sent=MAX(msgs_sent, ?), msgs_delivered=MAX(msgs_delivered, ?), msgs_read=MAX(msgs_read, ?), msgs_failed=MAX(msgs_failed, ?),
			file=CASE WHEN ?='null' THEN file ELSE ? END, p2p_attempted=MAX(p2p_attempted, ?), p2p_ok=MAX(p2p_ok, ?), fail_reason=?, updated_at=?, ended_at=?
			WHERE id=?`,
			r.Mode, r.Path, trim(r.Via, 120), r.State, r.Latency, r.RateBps, r.Sent, r.Delivered, r.Read, r.Failed, file, file, attempted, ok, trim(r.Reason, 200), now, ended, r.Session)
	}
	if err != nil {
		return err
	}
	if r.State == "fail" {
		reason := r.Reason
		if reason == "" {
			reason = "p2p negotiation failed"
		}
		h.Failure("p2p", from.ID, to.ID, reason, r.Via)
	}
	h.bus.Publish("session.update", map[string]any{"id": r.Session, "path": r.Path, "state": r.State})
	return nil
}

// RelayREST relays a message submitted over REST instead of the socket.
func (h *Hub) RelayREST(from *store.Device, m *proto.MsgSend) (*proto.MsgAck, error) {
	return h.relayMessage(from, m)
}

// Receipt applies delivered/read receipts submitted over REST.
func (h *Hub) Receipt(by *store.Device, ids []string, kind string) { h.receipt(by, ids, kind) }

// ErrInfo extracts the code and message of a hub protocol error.
func ErrInfo(err error) (code, msg string, ok bool) {
	var pe *protoErr
	if errors.As(err, &pe) {
		return pe.code, pe.msg, true
	}
	return "", "", false
}
