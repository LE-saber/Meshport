package api

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/coder/websocket"

	"meshport/internal/events"
	"meshport/internal/store"
)

var metricKeys = []string{"cpu", "mem", "disk", "load1", "net_rx_bps", "net_tx_bps", "devices_online", "sessions_p2p", "sessions_relay", "sessions_nego",
	"p2p_bps", "relay_bps", "frp_bps", "frp_active", "p2p_success", "ws_conns"}

func (s *Server) hMetrics(c *Ctx) error {
	rng := c.Query("range")
	if rng == "" {
		rng = "1h"
	}
	switch rng {
	case "live", "5m", "1h", "24h", "7d":
	default:
		return badRequest("range must be live, 5m, 1h, 24h or 7d")
	}
	keys := metricKeys
	if k := c.Query("keys"); k != "" {
		keys = strings.Split(k, ",")
	}
	points := c.QueryInt("points", 120, 1000)
	series := map[string]any{}
	for _, k := range keys {
		series[k] = s.mon.Series(strings.TrimSpace(k), rng, points)
	}
	return c.OK(map[string]any{"range": rng, "latest": s.mon.Latest(), "series": series})
}

// hP2PStats summarises P2P success per hour from session telemetry.
func (s *Server) hP2PStats(c *Ctx) error {
	hours := c.QueryInt("hours", 24, 24*7)
	since := time.Now().Add(-time.Duration(hours) * time.Hour).Unix()
	rows, err := s.db.Query(`SELECT (started_at/3600)*3600 AS h, SUM(p2p_attempted), SUM(p2p_attempted AND p2p_ok), SUM(path='relay') FROM comm_sessions WHERE started_at>=? GROUP BY h ORDER BY h`, since)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []map[string]any{}
	var ta, tok int
	for rows.Next() {
		var h int64
		var att, ok, relay int
		if err := rows.Scan(&h, &att, &ok, &relay); err != nil {
			return err
		}
		ta += att
		tok += ok
		rate := 0.0
		if att > 0 {
			rate = float64(ok) / float64(att) * 100
		}
		out = append(out, map[string]any{"hour": time.Unix(h, 0).UTC().Format(time.RFC3339), "attempted": att, "succeeded": ok, "relay": relay, "rate": rate})
	}
	overall := 0.0
	if ta > 0 {
		overall = float64(tok) / float64(ta) * 100
	}
	return c.OK(map[string]any{"items": out, "attempted": ta, "succeeded": tok, "rate": overall})
}

func (s *Server) hFailures(c *Ctx) error {
	where := []string{"1=1"}
	args := []any{}
	if k := c.Query("kind"); k != "" {
		where = append(where, "f.kind=?")
		args = append(args, k)
	}
	args = append(args, c.QueryInt("limit", 100, 1000))
	rows, err := s.db.Query(`SELECT f.id, f.kind, f.a_id, COALESCE(a.name,''), f.b_id, COALESCE(b.name,''), f.reason, f.detail, f.created_at
		FROM failures f LEFT JOIN devices a ON a.id=f.a_id LEFT JOIN devices b ON b.id=f.b_id WHERE `+strings.Join(where, " AND ")+` ORDER BY f.id DESC LIMIT ?`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var kind, aid, an, bid, bn, reason, detail string
		var at store.Time
		if err := rows.Scan(&id, &kind, &aid, &an, &bid, &bn, &reason, &detail, &at); err != nil {
			return err
		}
		out = append(out, map[string]any{"id": id, "kind": kind, "a_id": aid, "a": an, "b_id": bid, "b": bn, "reason": reason, "detail": detail, "at": at})
	}
	byKind := map[string]int{}
	krows, err := s.db.Query(`SELECT kind, COUNT(*) FROM failures WHERE created_at>=? GROUP BY kind`, time.Now().Add(-24*time.Hour).Unix())
	if err == nil {
		for krows.Next() {
			var k string
			var n int
			if krows.Scan(&k, &n) == nil {
				byKind[k] = n
			}
		}
		krows.Close()
	}
	return c.OK(map[string]any{"items": out, "last_24h": byKind})
}

func (s *Server) hLogs(c *Ctx) error {
	where := []string{"1=1"}
	args := []any{}
	if lv := c.Query("level"); lv != "" {
		where = append(where, "level=?")
		args = append(args, lv)
	}
	if src := c.Query("source"); src != "" {
		where = append(where, "source LIKE ? ESCAPE '\\'")
		args = append(args, store.Like(src))
	}
	if q := c.Query("q"); q != "" {
		where = append(where, "msg LIKE ? ESCAPE '\\'")
		args = append(args, store.Like(q))
	}
	if before := c.QueryInt("before", 0, 1<<62); before > 0 {
		where = append(where, "id < ?")
		args = append(args, before)
	}
	args = append(args, c.QueryInt("limit", 200, 2000))
	rows, err := s.db.Query(`SELECT id, ts, level, source, msg FROM logs WHERE `+strings.Join(where, " AND ")+` ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []events.LogEntry{}
	for rows.Next() {
		var e events.LogEntry
		var ts int64
		if err := rows.Scan(&e.ID, &ts, &e.Level, &e.Source, &e.Msg); err != nil {
			return err
		}
		e.TS = time.Unix(ts, 0).UTC()
		out = append(out, e)
	}
	return c.Items(out)
}

func (s *Server) hAudit(c *Ctx) error {
	where := []string{"1=1"}
	args := []any{}
	if a := c.Query("action"); a != "" {
		where = append(where, "action LIKE ? ESCAPE '\\'")
		args = append(args, store.Like(a))
	}
	if t := c.Query("target"); t != "" {
		where = append(where, "target_id=?")
		args = append(args, t)
	}
	args = append(args, c.QueryInt("limit", 200, 2000), c.QueryInt("offset", 0, 1_000_000))
	rows, err := s.db.Query(`SELECT id, actor_type, actor_id, actor, action, target_type, target_id, detail, ip, created_at FROM audit_log WHERE `+strings.Join(where, " AND ")+` ORDER BY id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var at, aid, actor, action, tt, tid, detail, ip string
		var ts store.Time
		if err := rows.Scan(&id, &at, &aid, &actor, &action, &tt, &tid, &detail, &ip, &ts); err != nil {
			return err
		}
		out = append(out, map[string]any{"id": id, "actor_type": at, "actor_id": aid, "actor": actor, "action": action, "target_type": tt, "target_id": tid,
			"detail": json.RawMessage(detail), "ip": ip, "at": ts})
	}
	return c.Items(out)
}

// hStream is the console's live feed: metrics every 2 s, logs, alerts,
// device presence, session and FRP changes. Optional ?events=a.*,b filters.
func (s *Server) hStream(c *Ctx) error {
	opts := &websocket.AcceptOptions{}
	if u := s.cfg.PublicURL; u != "" {
		host := strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
		opts.OriginPatterns = []string{strings.TrimSuffix(host, "/")}
	}
	ws, err := websocket.Accept(c.W, c.R, opts)
	if err != nil {
		return nil
	}
	defer ws.CloseNow()
	filters := []string{"*"}
	if f := c.Query("events"); f != "" {
		filters = strings.Split(f, ",")
	}
	ch, unsub := s.bus.Subscribe(512)
	defer unsub()
	ctx, cancel := context.WithCancel(c.R.Context())
	defer cancel()
	go func() { // drain client frames; detect close
		for {
			if _, _, err := ws.Read(ctx); err != nil {
				cancel()
				return
			}
		}
	}()
	hello, _ := json.Marshal(events.Event{Type: "hello", Time: time.Now().UTC(), Data: map[string]any{"user": c.P.Name(), "role": c.P.Role}})
	_ = ws.Write(ctx, websocket.MessageText, hello)
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ping.C:
			pctx, pc := context.WithTimeout(ctx, 10*time.Second)
			err := ws.Ping(pctx)
			pc()
			if err != nil {
				return nil
			}
		case ev := <-ch:
			if !events.Match(filters, ev.Type) {
				continue
			}
			b, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			wctx, wc := context.WithTimeout(ctx, 10*time.Second)
			err = ws.Write(wctx, websocket.MessageText, b)
			wc()
			if err != nil {
				return nil
			}
		}
	}
}
