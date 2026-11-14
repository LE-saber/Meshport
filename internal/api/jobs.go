package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"meshport/internal/events"
	"meshport/internal/store"
)

// runJobs performs periodic maintenance.
func (s *Server) runJobs(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	lastBackupDay := ""
	lastDaily := ""
	for {
		s.expireGrants()
		s.staleSessions()
		now := time.Now().UTC()
		day := now.Format("2006-01-02")
		st := s.db.Settings()
		if st.BackupHour >= 0 && now.Hour() == st.BackupHour && lastBackupDay != day {
			lastBackupDay = day
			if s.db.Count(`SELECT COUNT(*) FROM backups WHERE kind='scheduled' AND status='ok' AND created_at>=?`, now.Add(-20*time.Hour).Unix()) == 0 {
				if _, err := s.Backup("scheduled"); err != nil {
					s.log.Error("scheduled backup failed", "err", err, "src", "backup")
				}
			}
		}
		if lastDaily != day {
			lastDaily = day
			s.retention()
			s.keyAgeAlerts()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// expireGrants ends temporary grants whose validity ran out.
func (s *Server) expireGrants() {
	list, err := s.db.Grants(`g.status='approved' AND g.expires_at>0 AND g.expires_at<?`, time.Now().Unix())
	if err != nil {
		return
	}
	for _, g := range list {
		if err := s.endGrant(g, "expired", "system"); err == nil {
			s.Audit(nil, "", "grant.expired", "grant", g.ID, map[string]string{"target": g.TargetName, "restored": g.PrevLevel})
		}
	}
}

// staleSessions marks sessions without reports for 2 minutes as idle and closes them after 1 hour.
func (s *Server) staleSessions() {
	now := time.Now()
	_, _ = s.db.Exec(`UPDATE comm_sessions SET state='idle', path=CASE WHEN path='nego' THEN 'fail' ELSE path END, rate_bps=0 WHERE state IN ('up','nego') AND updated_at<?`, now.Add(-2*time.Minute).Unix())
	_, _ = s.db.Exec(`UPDATE comm_sessions SET state='closed', ended_at=? WHERE state IN ('idle','fail') AND updated_at<?`, now.Unix(), now.Add(-time.Hour).Unix())
}

// retention deletes old messages, files, logs and records.
func (s *Server) retention() {
	st := s.db.Settings()
	now := time.Now()
	msgCut := now.Add(-time.Duration(st.MessageRetentionDays) * 24 * time.Hour).Unix()
	logCut := now.Add(-time.Duration(st.LogRetentionDays) * 24 * time.Hour).Unix()
	_, _ = s.db.Exec(`DELETE FROM messages WHERE created_at<?`, msgCut)
	// delivered relay message bodies are not needed after delivery; keep metadata
	_, _ = s.db.Exec(`UPDATE messages SET body='' WHERE status IN ('delivered','read') AND body!='' AND delivered_at<?`, now.Add(-24*time.Hour).Unix())
	_, _ = s.db.Exec(`DELETE FROM logs WHERE ts<?`, logCut)
	_, _ = s.db.Exec(`DELETE FROM failures WHERE created_at<?`, logCut)
	_, _ = s.db.Exec(`DELETE FROM login_records WHERE created_at<?`, now.Add(-90*24*time.Hour).Unix())
	_, _ = s.db.Exec(`DELETE FROM comm_sessions WHERE state='closed' AND updated_at<?`, now.Add(-30*24*time.Hour).Unix())
	_, _ = s.db.Exec(`DELETE FROM alerts WHERE status='resolved' AND updated_at<?`, now.Add(-90*24*time.Hour).Unix())
	_, _ = s.db.Exec(`DELETE FROM web_sessions WHERE expires_at<?`, now.Unix())
	_, _ = s.db.Exec(`DELETE FROM device_tokens WHERE expires_at<?`, now.Unix())
	_, _ = s.db.Exec(`DELETE FROM enroll_tokens WHERE expires_at<? OR uses_left<=0`, now.Add(-7*24*time.Hour).Unix())
	rows, err := s.db.Query(`SELECT id FROM files WHERE expires_at<?`, now.Unix())
	if err == nil {
		var ids []string
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()
		for _, id := range ids {
			_ = os.Remove(filepath.Join(s.filesDir(), id))
			_, _ = s.db.Exec(`DELETE FROM files WHERE id=?`, id)
		}
	}
}

func (s *Server) keyAgeAlerts() {
	list, err := s.db.Devices(store.DeviceFilter{Status: "active"})
	if err != nil {
		return
	}
	for _, d := range list {
		age := time.Since(d.KeyCreatedAt.Time)
		if age > 365*24*time.Hour {
			s.raiseAlert("warn", "security", "Device key older than one year: "+d.Name, fmt.Sprintf("%s · %s · %d days", d.DID, d.Fingerprint, int(age.Hours()/24)), "keys", "keyrotate:"+d.ID)
		}
	}
}

// runAlerts turns platform events and metric thresholds into alerts.
func (s *Server) runAlerts(ctx context.Context) {
	ch, unsub := s.bus.Subscribe(1024)
	defer unsub()
	highCPU := 0
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-ch:
			data, _ := ev.Data.(map[string]any)
			str := func(k string) string { v, _ := data[k].(string); return v }
			switch ev.Type {
			case "frp.node.offline":
				s.raiseAlert("crit", "frp", "frps node unreachable: "+str("name"), str("error"), "frp", "node-offline:"+str("id"))
			case "frp.node.online":
				s.resolveAlerts("node-offline:" + str("id"))
			case "frp.node.crashed":
				s.raiseAlert("crit", "frp", "Managed frps exited: "+str("node"), str("error"), "frp", "node-crash:"+str("node"))
			case "frp.mapping.error":
				s.raiseAlert("warn", "frp", "Mapping rejected: "+str("name"), str("reason"), "frp", "mapping-error:"+str("id"))
			case "frp.mapping.online":
				s.resolveAlerts("mapping-error:" + str("id"))
			case "metrics.tick":
				vals, _ := ev.Data.(map[string]float64)
				if vals["cpu"] > 90 {
					highCPU++
				} else {
					highCPU = 0
					s.resolveAlerts("cpu-high")
				}
				if highCPU == 150 { // ~5 minutes at 2 s
					s.raiseAlert("warn", "system", "CPU above 90% for 5 minutes", fmt.Sprintf("current %.0f%%", vals["cpu"]), "monitor", "cpu-high")
				}
				if vals["disk"] > 90 {
					s.raiseAlert("crit", "system", "Disk almost full", fmt.Sprintf("%.0f%% used, %.1f GB free", vals["disk"], vals["disk_free_gb"]), "monitor", "disk-full")
				} else if vals["disk"] > 0 {
					s.resolveAlerts("disk-full")
				}
			}
		}
	}
}

// runWebhooks delivers matching events to every enabled webhook with retries.
func (s *Server) runWebhooks(ctx context.Context) {
	ch, unsub := s.bus.Subscribe(2048)
	defer unsub()
	type hook struct {
		id, url, secret string
		events          []string
	}
	var hooks []hook
	var loaded time.Time
	gen := int64(-1)
	load := func() {
		gen = s.hooksGen.Load()
		hooks = hooks[:0]
		rows, err := s.db.Query(`SELECT id, url, secret, events FROM webhooks WHERE enabled=1 AND fail_count<50`)
		if err != nil {
			return
		}
		for rows.Next() {
			var h hook
			var evs string
			if rows.Scan(&h.id, &h.url, &h.secret, &evs) == nil {
				_ = json.Unmarshal([]byte(evs), &h.events)
				hooks = append(hooks, h)
			}
		}
		rows.Close()
		loaded = time.Now()
	}
	sem := make(chan struct{}, 8)
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-ch:
			switch ev.Type {
			case "metrics.tick", "log", "session.update", "audit":
				continue // high-volume internal events are not sent to webhooks
			}
			if time.Since(loaded) > 10*time.Second || s.hooksGen.Load() != gen {
				load()
			}
			for _, h := range hooks {
				if !events.Match(h.events, ev.Type) {
					continue
				}
				h, ev := h, ev
				sem <- struct{}{}
				go func() {
					defer func() { <-sem }()
					for attempt, wait := 0, time.Second; attempt < 3; attempt, wait = attempt+1, wait*4 {
						if _, err := s.deliver(h.id, h.url, h.secret, ev); err == nil {
							return
						}
						select {
						case <-ctx.Done():
							return
						case <-time.After(wait):
						}
					}
				}()
			}
		}
	}
}
