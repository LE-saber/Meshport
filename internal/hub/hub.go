// Package hub is the device WebSocket hub: presence, store-and-forward
// message relay, P2P signalling, session telemetry and remote commands.
package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"meshport/internal/events"
	"meshport/internal/policy"
	"meshport/internal/store"
	"meshport/pkg/proto"
)

const (
	MaxMessageBody = 256 * 1024 // base64 body; larger payloads use the file API
	heartbeat      = 25 * time.Second
	writeTimeout   = 10 * time.Second
)

type Hub struct {
	db  *store.DB
	bus *events.Bus
	log *slog.Logger

	mu    sync.RWMutex
	conns map[string]*Conn

	cmdMu   sync.Mutex
	pending map[string]chan proto.CmdResult

	// RelayBytes counts message and file bytes carried by the server relay.
	RelayBytes atomic.Int64
	// SignalCount counts forwarded signalling frames.
	SignalCount atomic.Int64
}

type Conn struct {
	hub    *Hub
	ws     *websocket.Conn
	send   chan []byte
	cancel context.CancelFunc
	IP     string
	Since  time.Time

	mu     sync.RWMutex
	device *store.Device
}

func New(db *store.DB, bus *events.Bus, log *slog.Logger) *Hub {
	return &Hub{db: db, bus: bus, log: log.With("src", "hub"), conns: map[string]*Conn{}, pending: map[string]chan proto.CmdResult{}}
}

func (c *Conn) Device() *store.Device {
	c.mu.RLock()
	defer c.mu.RUnlock()
	d := *c.device
	return &d
}

// ---------- public API used by REST handlers ----------

func (h *Hub) Online(id string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.conns[id]
	return ok
}

func (h *Hub) OnlineIDs() map[string]bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make(map[string]bool, len(h.conns))
	for id := range h.conns {
		out[id] = true
	}
	return out
}

func (h *Hub) ConnCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.conns)
}

// ConnInfo is returned for a device's live connection (detail drawer).
type ConnInfo struct {
	IP    string    `json:"ip"`
	Since time.Time `json:"since"`
}

func (h *Hub) Conn(id string) *ConnInfo {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if c, ok := h.conns[id]; ok {
		return &ConnInfo{IP: c.IP, Since: c.Since}
	}
	return nil
}

// Send queues a frame for a device; false when it is offline.
func (h *Hub) Send(deviceID, typ string, payload any) bool {
	h.mu.RLock()
	c := h.conns[deviceID]
	h.mu.RUnlock()
	if c == nil {
		return false
	}
	return c.write(typ, "", payload)
}

// RefreshDevice reloads the cached device record (after level/group/status changes)
// and disconnects the device when it is no longer allowed on the mesh.
func (h *Hub) RefreshDevice(id string) {
	h.mu.RLock()
	c := h.conns[id]
	h.mu.RUnlock()
	if c == nil {
		return
	}
	d, err := h.db.DeviceByID(id)
	if err != nil || !policy.Active(d) {
		reason := "device removed"
		if d != nil {
			reason = "device " + d.Status
		}
		h.Kick(id, reason)
		return
	}
	c.mu.Lock()
	old := c.device.Level
	c.device = d
	c.mu.Unlock()
	if old != d.Level {
		c.write(proto.TLevelChanged, "", proto.LevelChanged{Level: d.Level, Reason: "updated"})
	}
}

// Kick sends a kick frame and closes the connection.
func (h *Hub) Kick(id, reason string) {
	h.mu.RLock()
	c := h.conns[id]
	h.mu.RUnlock()
	if c == nil {
		return
	}
	c.write(proto.TKick, "", proto.Kick{Reason: reason})
	go func() {
		time.Sleep(200 * time.Millisecond)
		c.ws.Close(websocket.StatusPolicyViolation, reason)
		c.cancel()
	}()
}

// CloseAll disconnects every device (used by "restart hub"); clients reconnect.
func (h *Hub) CloseAll(reason string) int {
	h.mu.RLock()
	list := make([]*Conn, 0, len(h.conns))
	for _, c := range h.conns {
		list = append(list, c)
	}
	h.mu.RUnlock()
	for _, c := range list {
		c.ws.Close(websocket.StatusServiceRestart, reason)
		c.cancel()
	}
	return len(list)
}

var ErrOffline = errors.New("device is offline")

// Command sends a remote-management command and waits for the result.
func (h *Hub) Command(ctx context.Context, deviceID, name string, args map[string]string) (proto.CmdResult, error) {
	id := store.NewID("cmd")
	ch := make(chan proto.CmdResult, 1)
	h.cmdMu.Lock()
	h.pending[id] = ch
	h.cmdMu.Unlock()
	defer func() {
		h.cmdMu.Lock()
		delete(h.pending, id)
		h.cmdMu.Unlock()
	}()
	if !h.Send(deviceID, proto.TCmd, proto.Cmd{ID: id, Name: name, Args: args}) {
		return proto.CmdResult{}, ErrOffline
	}
	select {
	case r := <-ch:
		return r, nil
	case <-ctx.Done():
		return proto.CmdResult{ID: id}, ctx.Err()
	}
}

// ---------- connection lifecycle ----------

// Serve runs a device connection until it closes.
func (h *Hub) Serve(ctx context.Context, ws *websocket.Conn, d *store.Device, ip string) {
	ctx, cancel := context.WithCancel(ctx)
	c := &Conn{hub: h, ws: ws, send: make(chan []byte, 256), cancel: cancel, device: d, IP: ip, Since: time.Now().UTC()}
	ws.SetReadLimit(MaxMessageBody + 64*1024)

	h.mu.Lock()
	if old := h.conns[d.ID]; old != nil {
		old.write(proto.TKick, "", proto.Kick{Reason: "replaced by a newer connection"})
		go func(o *Conn) {
			time.Sleep(100 * time.Millisecond)
			o.ws.Close(websocket.StatusPolicyViolation, "replaced")
			o.cancel()
		}(old)
	}
	h.conns[d.ID] = c
	h.mu.Unlock()

	now := store.Now()
	_, _ = h.db.Exec(`UPDATE devices SET last_seen_at=?, last_ip=? WHERE id=?`, now, ip, d.ID)
	h.log.Info("device connected", "device", d.Name, "id", d.ID, "ip", ip)
	h.bus.Publish("device.online", map[string]any{"id": d.ID, "name": d.Name, "ip": ip})
	h.broadcastPresence(d, true, now.Unix())

	go c.writer(ctx)

	queued := h.db.Count(`SELECT COUNT(*) FROM messages WHERE to_id=? AND status='queued'`, d.ID)
	c.write(proto.THello, "", proto.Hello{
		DeviceID: d.ID, DID: d.DID, Level: d.Level, ServerTime: time.Now().Unix(), Queued: queued,
		Heartbeat: int(heartbeat / time.Second), Caps: []string{"relay", "signal", "files", "frp", "turn", "grants"},
	})
	h.flushQueue(c)

	c.reader(ctx)

	cancel()
	h.mu.Lock()
	if h.conns[d.ID] == c {
		delete(h.conns, d.ID)
	}
	stillOnline := h.conns[d.ID] != nil
	h.mu.Unlock()
	if !stillOnline {
		now := store.Now()
		_, _ = h.db.Exec(`UPDATE devices SET last_seen_at=? WHERE id=?`, now, d.ID)
		_, _ = h.db.Exec(`UPDATE comm_sessions SET state='closed', ended_at=?, updated_at=?, rate_bps=0 WHERE (a_id=? OR b_id=?) AND state IN ('up','nego','idle')`, now, now, d.ID, d.ID)
		h.log.Info("device disconnected", "device", d.Name, "id", d.ID)
		h.bus.Publish("device.offline", map[string]any{"id": d.ID, "name": d.Name})
		h.broadcastPresence(d, false, now.Unix())
	}
}

func (c *Conn) write(typ, id string, payload any) bool {
	b, err := proto.Encode(typ, id, payload)
	if err != nil {
		return false
	}
	select {
	case c.send <- b:
		return true
	default:
		c.hub.log.Warn("send buffer full, dropping connection", "device", c.device.ID)
		c.cancel()
		return false
	}
}

func (c *Conn) writer(ctx context.Context) {
	t := time.NewTicker(heartbeat)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case b := <-c.send:
			wctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := c.ws.Write(wctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				c.cancel()
				return
			}
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := c.ws.Ping(pctx)
			cancel()
			if err != nil {
				c.cancel()
				return
			}
		}
	}
}

func (c *Conn) reader(ctx context.Context) {
	for {
		typ, data, err := c.ws.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		var f proto.Frame
		if err := json.Unmarshal(data, &f); err != nil {
			c.write(proto.TError, "", proto.Error{Code: "bad_frame", Message: "invalid JSON frame"})
			continue
		}
		if err := c.hub.handle(c, &f); err != nil {
			var pe *protoErr
			if errors.As(err, &pe) {
				c.write(proto.TError, f.ID, proto.Error{Code: pe.code, Message: pe.msg})
			} else {
				c.hub.log.Error("frame handling failed", "type", f.T, "err", err)
				c.write(proto.TError, f.ID, proto.Error{Code: "internal", Message: "internal error"})
			}
		}
	}
}

type protoErr struct{ code, msg string }

func (e *protoErr) Error() string { return e.code + ": " + e.msg }
func perr(code, format string, a ...any) error {
	return &protoErr{code: code, msg: fmt.Sprintf(format, a...)}
}

func (h *Hub) broadcastPresence(d *store.Device, online bool, ts int64) {
	scope := h.db.Settings().DiscoveryScope
	p := proto.Presence{DeviceID: d.ID, Online: online, LastSeen: ts}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for id, c := range h.conns {
		if id == d.ID {
			continue
		}
		if policy.CanSee(c.Device(), d, scope) {
			c.write(proto.TPresence, "", p)
		}
	}
}
