package api

import (
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"meshport/internal/store"
	"meshport/pkg/proto"
)

func TestAuditSearch(t *testing.T) {
	e := newEnv(t)
	for _, row := range [][4]string{
		{"operator_100%", "user_one", "device", "target-one"},
		{"operatorX100other", "user_two", "mapping", "target-two"},
	} {
		_, err := e.srv.db.Exec(`INSERT INTO audit_log (actor_type, actor_id, actor, action, target_type, target_id, detail, ip, created_at) VALUES ('user',?,?, 'device.updated',?,?, '{}','127.0.0.1',?)`, row[1], row[0], row[2], row[3], time.Now().Unix())
		e.must(err)
	}
	for _, tc := range []struct {
		query string
		count int
	}{
		{"q=" + url.QueryEscape("operator_100%"), 1},
		{"q=user_one", 1}, {"q=target-two", 1}, {"q=mapping", 1},
		{"q=no-such-actor-or-target", 0},
		{"q=operator&target=target-two&action=device.updated", 1},
		{"q=operator&target=target-two&action=grant.created", 0},
	} {
		t.Run(tc.query, func(t *testing.T) {
			var out struct {
				Items []map[string]any `json:"items"`
			}
			e.must(e.admin.Do(e.ctx(), "GET", "/api/v1/admin/audit?"+tc.query, nil, &out))
			if len(out.Items) != tc.count {
				t.Fatalf("got %d records, want %d", len(out.Items), tc.count)
			}
		})
	}
}

func TestConsoleSessionFilters(t *testing.T) {
	e := newEnv(t)
	a, b := e.device("A", "member"), e.device("B", "member")
	ca := e.connect(a)
	for _, r := range []proto.SessionReport{
		{Session: "direct", Peer: b.DeviceID, Mode: "auto", Path: "p2p", State: "up", Via: "LAN direct"},
		{Session: "turn", Peer: b.DeviceID, Mode: "auto", Path: "relay", State: "up", Via: "TURN relay"},
		{Session: "server", Peer: b.DeviceID, Mode: "relay", Path: "relay", State: "up", Via: "server relay"},
	} {
		e.must(ca.Request(e.ctx(), proto.TSessionReport, r, nil))
	}
	for _, tc := range []struct {
		query string
		count int
	}{
		{"state=up", 3}, {"path=p2p&state=up", 1}, {"path=relay&state=up", 2},
	} {
		var out struct {
			Items []store.CommSession `json:"items"`
		}
		e.must(e.admin.Do(e.ctx(), "GET", "/api/v1/admin/sessions?"+tc.query, nil, &out))
		if len(out.Items) != tc.count {
			t.Fatalf("%s: got %d, want %d", tc.query, len(out.Items), tc.count)
		}
	}
}

func TestDirectGrantAppliesDeviceCapabilities(t *testing.T) {
	e := newEnv(t)
	guest := e.device("Target", "guest")
	cn := e.connect(guest)
	request := map[string]string{"target": guest.DeviceID, "level": "member", "duration": "perm"}
	viewer := e.createUser("observer", "viewer")
	e.expectErr(viewer.Do(e.ctx(), "POST", "/api/v1/admin/grants", request, nil), 403, "forbidden")
	var grant store.Grant
	e.must(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/grants", request, &grant))
	if grant.Status != "approved" {
		t.Fatalf("grant not approved: %+v", grant)
	}
	f, err := cn.Wait(e.ctx(), proto.TLevelChanged)
	e.must(err)
	var change proto.LevelChanged
	e.must(json.Unmarshal(f.D, &change))
	if change.Level != "member" {
		t.Fatalf("level update: %+v", change)
	}
	var me struct {
		Device       store.Device `json:"device"`
		Capabilities []string     `json:"capabilities"`
	}
	e.must(guest.Do(e.ctx(), "GET", "/api/v1/me/device", nil, &me))
	if me.Device.Level != "member" {
		t.Fatalf("device still has %q", me.Device.Level)
	}
	found := false
	for _, cap := range me.Capabilities {
		if cap == "frpc" {
			found = true
		}
	}
	if !found {
		t.Fatalf("publishing capability not applied: %+v", me)
	}
	e.must(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/grants/"+grant.ID+"/revoke", nil, nil))
	e.must(guest.Do(e.ctx(), "GET", "/api/v1/me/device", nil, &me))
	if me.Device.Level != "guest" {
		t.Fatalf("revoke did not restore guest: %+v", me)
	}
}
