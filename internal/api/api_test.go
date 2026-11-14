package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"meshport/internal/auth"
	"meshport/internal/store"
	"meshport/pkg/client"
	"meshport/pkg/proto"
)

func TestConsoleAuthAndBruteForce(t *testing.T) {
	e := newEnv(t)
	c := client.New(e.ts.URL)
	err := c.Do(e.ctx(), "POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "wrong-password"}, nil)
	e.expectErr(err, 401, "invalid_credentials")

	var me map[string]any
	e.must(e.admin.Do(e.ctx(), "GET", "/api/v1/auth/me", nil, &me))
	if me["role"] != "admin" {
		t.Fatalf("me: %v", me)
	}
	// unauthenticated console call
	e.expectErr(c.Do(e.ctx(), "GET", "/api/v1/admin/overview", nil, nil), 401, "unauthorized")

	// 5 failures from one IP -> temporary block, flagged abnormal, alert raised
	for i := 0; i < 4; i++ {
		_ = c.Do(e.ctx(), "POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "nope-nope"}, nil)
	}
	err = c.Do(e.ctx(), "POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "admin-password-1"}, nil)
	e.expectErr(err, 429, "too_many_attempts")
	var logins struct {
		Items   []map[string]any `json:"items"`
		Summary map[string]int   `json:"summary"`
	}
	e.must(e.admin.Do(e.ctx(), "GET", "/api/v1/admin/security/logins?abnormal=1", nil, &logins))
	if len(logins.Items) == 0 {
		t.Fatal("expected abnormal login records")
	}
	var alerts struct {
		Items []alertRow `json:"items"`
	}
	e.must(e.admin.Do(e.ctx(), "GET", "/api/v1/admin/alerts?category=security", nil, &alerts))
	if len(alerts.Items) == 0 {
		t.Fatal("expected a security alert")
	}
	e.must(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/security/unblock", map[string]string{"ip": "127.0.0.1"}, nil))
	e.login("admin", "admin-password-1", "")
}

func TestMFAFlow(t *testing.T) {
	e := newEnv(t)
	var setup struct {
		Secret string `json:"secret"`
		URI    string `json:"otpauth_uri"`
	}
	e.must(e.admin.Do(e.ctx(), "POST", "/api/v1/auth/mfa/setup", nil, &setup))
	if !strings.HasPrefix(setup.URI, "otpauth://totp/") {
		t.Fatal(setup.URI)
	}
	e.expectErr(e.admin.Do(e.ctx(), "POST", "/api/v1/auth/mfa/enable", map[string]string{"code": "000000"}, nil), 400, "invalid_mfa")
	e.must(e.admin.Do(e.ctx(), "POST", "/api/v1/auth/mfa/enable", map[string]string{"code": auth.TOTPCode(setup.Secret, time.Now())}, nil))
	c := client.New(e.ts.URL)
	e.expectErr(c.Do(e.ctx(), "POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "admin-password-1"}, nil), 401, "mfa_required")
	e.login("admin", "admin-password-1", auth.TOTPCode(setup.Secret, time.Now()))

	// require MFA: an operator without MFA is blocked from the console until enrolled
	e.must(e.admin.Do(e.ctx(), "PUT", "/api/v1/admin/security/mfa", map[string]bool{"required": true}, nil))
	op := e.createUser("olivia", "operator")
	e.expectErr(op.Do(e.ctx(), "GET", "/api/v1/admin/devices", nil, nil), 403, "mfa_required")
	e.must(op.Do(e.ctx(), "POST", "/api/v1/auth/mfa/setup", nil, nil)) // auth endpoints stay available
}

func TestRolesAndAPITokens(t *testing.T) {
	e := newEnv(t)
	viewer := e.createUser("vic", "viewer")
	e.must(viewer.Do(e.ctx(), "GET", "/api/v1/admin/devices", nil, nil))
	e.expectErr(viewer.Do(e.ctx(), "POST", "/api/v1/admin/groups", map[string]string{"name": "x"}, nil), 403, "forbidden")

	var tok struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	e.must(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/security/tokens", map[string]any{"name": "ci", "scopes": []string{"read"}}, &tok))
	ro := client.New(e.ts.URL)
	ro.Token = tok.Token
	e.must(ro.Do(e.ctx(), "GET", "/api/v1/admin/overview", nil, nil))
	e.expectErr(ro.Do(e.ctx(), "POST", "/api/v1/admin/groups", map[string]string{"name": "x"}, nil), 403, "forbidden")
	// a viewer cannot mint a write token
	e.expectErr(viewer.Do(e.ctx(), "POST", "/api/v1/admin/security/tokens", map[string]any{"name": "w", "scopes": []string{"write"}}, nil), 403, "forbidden")
	e.must(e.admin.Do(e.ctx(), "DELETE", "/api/v1/admin/security/tokens/"+tok.ID, nil, nil))
	e.expectErr(ro.Do(e.ctx(), "GET", "/api/v1/admin/overview", nil, nil), 401, "unauthorized")

	// service keys
	var key struct{ Token string }
	e.must(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/system/api-keys", map[string]any{"name": "grafana", "scopes": []string{"read"}}, &key))
	st, body, _ := e.raw("GET", "/metrics", key.Token, nil, nil)
	if st != 200 || !strings.Contains(string(body), "meshport_open_alerts") {
		t.Fatalf("prometheus: %d %s", st, body)
	}

	// cookie-authenticated mutations need X-Requested-With (CSRF guard)
	req, _ := http.NewRequest("POST", e.ts.URL+"/api/v1/admin/groups", strings.NewReader(`{"name":"g1"}`))
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: e.admin.Token})
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("csrf: got %d", resp.StatusCode)
	}
	req, _ = http.NewRequest("POST", e.ts.URL+"/api/v1/admin/groups", strings.NewReader(`{"name":"g1"}`))
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: e.admin.Token})
	req.Header.Set("X-Requested-With", "fetch")
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("cookie with header: got %d", resp.StatusCode)
	}
}

func TestRegistrationApprovalAndStatus(t *testing.T) {
	e := newEnv(t)
	alice := e.createUser("alice", "user")
	_ = alice
	// self-registration with user credentials -> pending (manual approval by default)
	c := client.New(e.ts.URL)
	d, err := c.Register(e.ctx(), client.RegisterRequest{Name: "Pixel 9", OS: "Android 15", Kind: "phone", Username: "alice", Password: "pw-alice-123456"})
	e.must(err)
	if d.Status != "pending" {
		t.Fatalf("status %s", d.Status)
	}
	e.expectErr(c.Login(e.ctx()), 403, "device_pending")
	var ov map[string]any
	e.must(e.admin.Do(e.ctx(), "GET", "/api/v1/admin/overview", nil, &ov))
	if ov["devices"].(map[string]any)["pending"].(float64) != 1 {
		t.Fatalf("overview pending: %v", ov["devices"])
	}
	e.must(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/devices/"+d.ID+"/approve", nil, nil))
	e.must(c.Login(e.ctx()))
	cn := e.connect(c)
	if cn.Hello.DeviceID != d.ID || cn.Hello.Level != "guest" {
		t.Fatalf("hello %+v", cn.Hello)
	}
	// replayed registration with the same key is refused
	_, err = c.Register(e.ctx(), client.RegisterRequest{Name: "dup", Username: "alice", Password: "pw-alice-123456"})
	e.expectErr(err, 409, "conflict")

	// disable -> kicked + cannot log in; enable -> works again
	e.must(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/devices/"+d.ID+"/disable", nil, nil))
	f, err := cn.Wait(e.ctx(), proto.TKick)
	e.must(err)
	_ = f
	e.expectErr(c.Login(e.ctx()), 403, "device_disabled")
	e.must(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/devices/"+d.ID+"/enable", nil, nil))
	e.must(c.Login(e.ctx()))

	// ban shows in the ban list; unban
	e.must(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/devices/"+d.ID+"/ban", map[string]string{"reason": "lost phone"}, nil))
	var bans struct{ Items []store.Device }
	e.must(e.admin.Do(e.ctx(), "GET", "/api/v1/admin/security/bans", nil, &bans))
	if len(bans.Items) != 1 || bans.Items[0].BanReason != "lost phone" {
		t.Fatalf("bans %+v", bans.Items)
	}
	e.must(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/devices/"+d.ID+"/unban", nil, nil))

	// closed registration
	e.must(e.admin.Do(e.ctx(), "PATCH", "/api/v1/admin/system/settings", map[string]any{"open_registration": false}, nil))
	c2 := client.New(e.ts.URL)
	_, err = c2.Register(e.ctx(), client.RegisterRequest{Name: "x", Username: "alice", Password: "pw-alice-123456"})
	e.expectErr(err, 403, "forbidden")

	// device list filters and counts
	var list struct {
		Items  []store.Device `json:"items"`
		Counts map[string]int `json:"counts"`
	}
	e.must(e.admin.Do(e.ctx(), "GET", "/api/v1/admin/devices?q=pixel", nil, &list))
	if len(list.Items) != 1 || list.Counts["all"] != 1 {
		t.Fatalf("list %+v", list)
	}
	// delete
	e.must(e.admin.Do(e.ctx(), "DELETE", "/api/v1/admin/devices/"+d.ID, nil, nil))
	e.expectErr(e.admin.Do(e.ctx(), "GET", "/api/v1/admin/devices/"+d.ID, nil, nil), 404, "not_found")
}

func TestRelayMessagingAndReceipts(t *testing.T) {
	e := newEnv(t)
	mac := e.device("MacBook", "member")
	nas := e.device("NAS", "member")
	cm := e.connect(mac)

	// discovery
	var peers struct{ Items []peerView }
	e.must(mac.Do(e.ctx(), "GET", "/api/v1/peers", nil, &peers))
	if len(peers.Items) != 1 || peers.Items[0].ID != nas.DeviceID || peers.Items[0].Online {
		t.Fatalf("peers %+v", peers.Items)
	}

	// offline recipient -> queued, flushed on connect
	ack, err := cm.SendMessage(e.ctx(), nas.DeviceID, proto.ModeRelay, []byte("ciphertext-1"))
	e.must(err)
	if ack.Status != "queued" {
		t.Fatalf("ack %+v", ack)
	}
	cn := e.connect(nas)
	if cn.Hello.Queued != 1 {
		t.Fatalf("hello queued %d", cn.Hello.Queued)
	}
	f, err := cn.Wait(e.ctx(), proto.TMsgNew)
	e.must(err)
	var m proto.Message
	_ = json.Unmarshal(f.D, &m)
	if body, _ := base64.StdEncoding.DecodeString(m.Body); string(body) != "ciphertext-1" || m.From != mac.DeviceID {
		t.Fatalf("message %+v", m)
	}
	// presence: mac learns nas came online
	pf, err := cm.Wait(e.ctx(), proto.TPresence)
	e.must(err)
	var pr proto.Presence
	_ = json.Unmarshal(pf.D, &pr)
	if pr.DeviceID != nas.DeviceID || !pr.Online {
		t.Fatalf("presence %+v", pr)
	}

	// receipts flow back to the sender
	e.must(cn.Request(e.ctx(), proto.TMsgRead, proto.Receipt{IDs: []string{m.ID}}, nil))
	rf, err := cm.Wait(e.ctx(), proto.TMsgReceipt)
	e.must(err)
	var rc proto.Receipt
	_ = json.Unmarshal(rf.D, &rc)
	if rc.Kind != "read" || rc.IDs[0] != m.ID {
		t.Fatalf("receipt %+v", rc)
	}

	// online recipient -> relayed immediately (auto mode falls back to relay)
	ack, err = cm.SendMessage(e.ctx(), nas.DeviceID, proto.ModeAuto, []byte("hi"))
	e.must(err)
	if ack.Status != "relayed" {
		t.Fatalf("ack %+v", ack)
	}
	if _, err := cn.Wait(e.ctx(), proto.TMsgNew); err != nil {
		t.Fatal(err)
	}

	// p2p-only content must never be relayed
	_, err = cm.SendMessage(e.ctx(), nas.DeviceID, proto.ModeP2P, []byte("x"))
	if ae, ok := err.(*client.APIError); !ok || ae.Code != "mode_violation" {
		t.Fatalf("expected mode_violation, got %v", err)
	}

	// relay disabled by admin
	e.must(e.admin.Do(e.ctx(), "PATCH", "/api/v1/admin/system/settings", map[string]any{"relay_enabled": false}, nil))
	_, err = cm.SendMessage(e.ctx(), nas.DeviceID, proto.ModeRelay, []byte("x"))
	if ae, ok := err.(*client.APIError); !ok || ae.Code != "relay_disabled" {
		t.Fatalf("expected relay_disabled, got %v", err)
	}
	e.must(e.admin.Do(e.ctx(), "PATCH", "/api/v1/admin/system/settings", map[string]any{"relay_enabled": true}, nil))

	// REST fallback send + queued fetch
	cn.Close()
	time.Sleep(100 * time.Millisecond)
	var restAck proto.MsgAck
	e.must(mac.Do(e.ctx(), "POST", "/api/v1/messages", proto.MsgSend{To: nas.DeviceID, Mode: "relay", Body: base64.StdEncoding.EncodeToString([]byte("rest"))}, &restAck))
	var q struct{ Items []proto.Message }
	e.must(nas.Do(e.ctx(), "GET", "/api/v1/messages", nil, &q))
	found := false
	for _, it := range q.Items {
		if it.ID == restAck.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("REST message not queued")
	}
	e.must(nas.Do(e.ctx(), "POST", "/api/v1/messages/receipts", map[string]any{"ids": []string{restAck.ID}, "kind": "delivered"}, nil))

	// the admin sees the relay session with counters
	var sess struct {
		Items []store.CommSession `json:"items"`
	}
	e.must(e.admin.Do(e.ctx(), "GET", "/api/v1/admin/sessions?include_closed=1", nil, &sess))
	sent := 0
	for _, it := range sess.Items {
		sent += it.MsgsSent
		if it.Path != "relay" {
			t.Fatalf("sessions %s", dump(sess.Items))
		}
	}
	if sent != 3 { // queued + auto + REST; rejected sends are not counted
		t.Fatalf("expected 3 relayed messages across sessions, got %d: %s", sent, dump(sess.Items))
	}
}

func TestSignallingSessionsAndCommands(t *testing.T) {
	e := newEnv(t)
	a := e.device("Pixel", "member")
	b := e.device("Mac", "member")
	ca, cb := e.connect(a), e.connect(b)

	// ICE servers carry TURN REST credentials
	var ice struct {
		ICEServers []struct {
			URLs       []string `json:"urls"`
			Username   string   `json:"username"`
			Credential string   `json:"credential"`
		} `json:"ice_servers"`
	}
	e.must(a.Do(e.ctx(), "GET", "/api/v1/rtc/ice-servers", nil, &ice))
	if len(ice.ICEServers) != 2 || !strings.HasSuffix(ice.ICEServers[1].Username, ":"+a.DeviceID) || ice.ICEServers[1].Credential == "" {
		t.Fatalf("ice %+v", ice)
	}

	// offer/answer forwarded with the sender filled in
	sdp := json.RawMessage(`{"sdp":"v=0..."}`)
	e.must(ca.Request(e.ctx(), proto.TSignal, proto.Signal{To: b.DeviceID, Session: "ses_test1", Kind: "offer", Mode: "auto", Data: sdp}, nil))
	f, err := cb.Wait(e.ctx(), proto.TSignal)
	e.must(err)
	var sig proto.Signal
	_ = json.Unmarshal(f.D, &sig)
	if sig.From != a.DeviceID || sig.Kind != "offer" || string(sig.Data) != string(sdp) {
		t.Fatalf("signal %+v", sig)
	}
	// telemetry: P2P established
	lat := 18
	e.must(cb.Request(e.ctx(), proto.TSessionReport, proto.SessionReport{Session: "ses_test1", Peer: a.DeviceID, Mode: "auto", Path: "p2p", Via: "IPv6 direct", State: "up",
		Latency: &lat, RateBps: 2400000, Sent: 3, Delivered: 3, File: &proto.File{Name: "IMG.HEIC", Size: 100, Done: 41}}, nil))
	ses, err := e.srv.db.SessionByID("ses_test1")
	e.must(err)
	if ses.Path != "p2p" || !ses.P2POK || !ses.P2PAttempted || ses.File.V == nil || ses.File.V.Percent != 41 {
		t.Fatalf("session %s", dump(ses))
	}
	// a third device cannot hijack the session id
	c := e.device("Other", "member")
	cc := e.connect(c)
	err = cc.Request(e.ctx(), proto.TSessionReport, proto.SessionReport{Session: "ses_test1", Peer: a.DeviceID, Mode: "auto", Path: "fail", State: "fail"}, nil)
	if ae, ok := err.(*client.APIError); !ok || ae.Code != "forbidden" {
		t.Fatalf("expected forbidden, got %v", err)
	}
	// failure report appears in the failures list
	e.must(ca.Request(e.ctx(), proto.TSessionReport, proto.SessionReport{Session: "ses_t2", Peer: b.DeviceID, Mode: "p2p", Path: "fail", State: "fail", Reason: "symmetric NAT"}, nil))
	var fails struct{ Items []map[string]any }
	e.must(e.admin.Do(e.ctx(), "GET", "/api/v1/admin/failures?kind=p2p", nil, &fails))
	if len(fails.Items) == 0 || fails.Items[0]["reason"] != "symmetric NAT" {
		t.Fatalf("failures %v", fails.Items)
	}
	// P2P stats
	var p2p struct {
		Attempted int     `json:"attempted"`
		Succeeded int     `json:"succeeded"`
		Rate      float64 `json:"rate"`
	}
	e.must(e.admin.Do(e.ctx(), "GET", "/api/v1/admin/metrics/p2p", nil, &p2p))
	if p2p.Attempted != 2 || p2p.Succeeded != 1 {
		t.Fatalf("p2p stats %+v", p2p)
	}

	// admin disconnect -> both receive session.close
	e.must(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/sessions/ses_test1/disconnect", nil, nil))
	if _, err := ca.Wait(e.ctx(), proto.TSessionClose); err != nil {
		t.Fatal(err)
	}

	// remote command round trip
	go func() {
		f, err := cb.Wait(context.Background(), proto.TCmd)
		if err != nil {
			return
		}
		var cmd proto.Cmd
		_ = json.Unmarshal(f.D, &cmd)
		_ = cb.Send(proto.TCmdResult, proto.CmdResult{ID: cmd.ID, OK: true, Output: "pong " + cmd.Name})
	}()
	var res proto.CmdResult
	e.must(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/devices/"+b.DeviceID+"/command", map[string]any{"name": "ping", "timeout_s": 5}, &res))
	if !res.OK || res.Output != "pong ping" {
		t.Fatalf("cmd %+v", res)
	}
}

func TestDiscoveryScope(t *testing.T) {
	e := newEnv(t)
	e.must(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/groups", map[string]string{"name": "home"}, nil))
	e.must(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/groups", map[string]string{"name": "office"}, nil))
	reg := func(name, group, level string) *client.Client {
		c := client.New(e.ts.URL)
		_, err := c.Register(e.ctx(), client.RegisterRequest{Name: name, EnrollToken: e.enrollToken(level, group, true)})
		e.must(err)
		e.must(c.Login(e.ctx()))
		return c
	}
	h1, h2 := reg("h1", "home", "guest"), reg("h2", "home", "guest")
	o1 := reg("o1", "office", "guest")
	adm := reg("ops", "office", "admin")
	e.must(e.admin.Do(e.ctx(), "PATCH", "/api/v1/admin/system/settings", map[string]any{"discovery_scope": "group"}, nil))
	names := func(c *client.Client) map[string]bool {
		var p struct{ Items []peerView }
		e.must(c.Do(e.ctx(), "GET", "/api/v1/peers", nil, &p))
		out := map[string]bool{}
		for _, x := range p.Items {
			out[x.Name] = true
		}
		return out
	}
	if n := names(h1); !n["h2"] || n["o1"] || !n["ops"] {
		t.Fatalf("h1 sees %v", n)
	}
	if n := names(adm); !n["h1"] || !n["h2"] || !n["o1"] {
		t.Fatalf("admin sees %v", n)
	}
	cn := e.connect(h1)
	_, err := cn.SendMessage(e.ctx(), o1.DeviceID, "relay", []byte("x"))
	if ae, ok := err.(*client.APIError); !ok || ae.Code != "forbidden" {
		t.Fatalf("cross-group message: %v", err)
	}
	_ = h2
}

func TestGrantLifecycle(t *testing.T) {
	e := newEnv(t)
	ops := e.device("tokyo-edge", "admin")
	guest := e.device("ThinkPad", "guest")
	member := e.device("Office-PC", "member")
	cg := e.connect(guest)

	// non-admin devices cannot request
	e.expectErr(member.Do(e.ctx(), "POST", "/api/v1/grants", map[string]string{"target": guest.DeviceID, "level": "member"}, nil), 403, "forbidden")
	// cannot request for itself
	e.expectErr(ops.Do(e.ctx(), "POST", "/api/v1/grants", map[string]string{"target": ops.DeviceID, "level": "admin"}, nil), 403, "forbidden")

	var g store.Grant
	e.must(ops.Do(e.ctx(), "POST", "/api/v1/grants", map[string]string{"target": guest.DeviceID, "level": "member", "reason": "rsDev", "duration": "7d"}, &g))
	if g.Status != "pending" {
		t.Fatalf("grant %+v", g)
	}
	e.expectErr(ops.Do(e.ctx(), "POST", "/api/v1/grants", map[string]string{"target": guest.DeviceID, "level": "member"}, nil), 409, "conflict")
	if _, err := cg.Wait(e.ctx(), proto.TGrantUpdate); err != nil {
		t.Fatal(err)
	}
	var list struct {
		Items  []store.Grant  `json:"items"`
		Counts map[string]int `json:"counts"`
	}
	e.must(e.admin.Do(e.ctx(), "GET", "/api/v1/admin/grants?status=pending", nil, &list))
	if len(list.Items) != 1 || list.Counts["pending"] != 1 {
		t.Fatalf("pending %+v", list)
	}
	e.must(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/grants/"+g.ID+"/approve", nil, &g))
	if g.Status != "approved" || g.PrevLevel != "guest" || g.ExpiresAt.IsZero() {
		t.Fatalf("approved %+v", g)
	}
	lf, err := cg.Wait(e.ctx(), proto.TLevelChanged)
	e.must(err)
	var lc proto.LevelChanged
	_ = json.Unmarshal(lf.D, &lc)
	if lc.Level != "member" {
		t.Fatalf("level changed %+v", lc)
	}
	d, _ := e.srv.db.DeviceByID(guest.DeviceID)
	if d.Level != "member" {
		t.Fatalf("device level %s", d.Level)
	}
	// revoke restores the previous level
	e.must(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/grants/"+g.ID+"/revoke", nil, nil))
	d, _ = e.srv.db.DeviceByID(guest.DeviceID)
	if d.Level != "guest" {
		t.Fatalf("after revoke %s", d.Level)
	}

	// direct temporary grant by an operator; expiry restores the level
	var g2 store.Grant
	e.must(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/grants", map[string]string{"target": guest.DeviceID, "level": "member", "duration": "24h"}, &g2))
	_, _ = e.srv.db.Exec(`UPDATE grants SET expires_at=? WHERE id=?`, time.Now().Add(-time.Minute).Unix(), g2.ID)
	e.srv.expireGrants()
	g3, _ := e.srv.db.GrantByID(g2.ID)
	d, _ = e.srv.db.DeviceByID(guest.DeviceID)
	if g3.Status != "expired" || d.Level != "guest" {
		t.Fatalf("expiry: grant %s level %s", g3.Status, d.Level)
	}

	// reject
	var g4 store.Grant
	e.must(ops.Do(e.ctx(), "POST", "/api/v1/grants", map[string]string{"target": member.DeviceID, "level": "admin", "reason": "deploy"}, &g4))
	e.must(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/grants/"+g4.ID+"/reject", map[string]string{"note": "no"}, nil))
	var mine struct{ Items []store.Grant }
	e.must(ops.Do(e.ctx(), "GET", "/api/v1/me/grants", nil, &mine))
	if len(mine.Items) != 2 {
		t.Fatalf("my grants %d", len(mine.Items))
	}
	var levels struct{ Items []map[string]any }
	e.must(e.admin.Do(e.ctx(), "GET", "/api/v1/admin/levels", nil, &levels))
	if len(levels.Items) != 3 {
		t.Fatal("levels")
	}
}

func TestFRPMappingsAndPlugin(t *testing.T) {
	e := newEnv(t)
	var nodeResp struct {
		Node     nodeView `json:"node"`
		FrpsTOML string   `json:"frps_toml"`
	}
	e.must(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/frp/nodes", map[string]any{
		"name": "tpe", "region": "Taipei", "public_addr": "203.0.113.10", "bind_port": 7000, "vhost_http_port": 80, "vhost_https_port": 443,
		"subdomain_host": "apps.example.com", "allow_ports": "6000-7000,9987", "dashboard_addr": "127.0.0.1:1"}, &nodeResp))
	node, err := e.srv.db.NodeByID(nodeResp.Node.ID) // plugin token is not exposed over the API
	e.must(err)
	if !strings.Contains(nodeResp.FrpsTOML, "[[httpPlugins]]") || !strings.Contains(nodeResp.FrpsTOML, "allowPorts") {
		t.Fatalf("frps toml:\n%s", nodeResp.FrpsTOML)
	}
	guest := e.device("guest-box", "guest")
	mac := e.device("mac", "member")

	e.expectErr(guest.Do(e.ctx(), "POST", "/api/v1/me/mappings", map[string]any{"name": "x", "node": "tpe", "proto": "tcp", "local_port": 22, "remote_port": 6022}, nil), 403, "forbidden")
	var ssh store.Mapping
	e.must(mac.Do(e.ctx(), "POST", "/api/v1/me/mappings", map[string]any{"name": "ssh-mac", "node": "tpe", "proto": "tcp", "local_port": 22, "remote_port": 6022}, &ssh))
	if ssh.PublicAddr != "203.0.113.10:6022" || ssh.Status != "pending" {
		t.Fatalf("mapping %+v", ssh)
	}
	// validation
	e.expectErr(mac.Do(e.ctx(), "POST", "/api/v1/me/mappings", map[string]any{"name": "bad", "node": "tpe", "proto": "tcp", "local_port": 22, "remote_port": 8000}, nil), 400, "bad_request")
	e.expectErr(mac.Do(e.ctx(), "POST", "/api/v1/me/mappings", map[string]any{"name": "dup", "node": "tpe", "proto": "tcp", "local_port": 23, "remote_port": 6022}, nil), 400, "bad_request")
	var blog store.Mapping
	e.must(mac.Do(e.ctx(), "POST", "/api/v1/me/mappings", map[string]any{"name": "web-blog", "node": "tpe", "proto": "http", "local_port": 8080, "domains": []string{"Blog.Example.com"}}, &blog))
	if blog.PublicAddr != "http://blog.example.com" {
		t.Fatalf("blog %+v", blog)
	}
	var sub store.Mapping
	e.must(mac.Do(e.ctx(), "POST", "/api/v1/me/mappings", map[string]any{"name": "media", "node": "tpe", "proto": "https", "local_port": 8096, "subdomain": "media"}, &sub))
	if sub.PublicAddr != "https://media.apps.example.com" {
		t.Fatalf("sub %+v", sub)
	}
	var adb store.Mapping
	e.must(mac.Do(e.ctx(), "POST", "/api/v1/me/mappings", map[string]any{"name": "adb", "node": "tpe", "proto": "stcp", "local_port": 5555}, &adb))
	if len(adb.SecretKey) < 8 {
		t.Fatal("stcp secret not generated")
	}

	// generated frpc config
	var cfgs struct {
		Items []struct {
			Node string `json:"node"`
			TOML string `json:"toml"`
		} `json:"items"`
	}
	e.must(mac.Do(e.ctx(), "GET", "/api/v1/me/frpc", nil, &cfgs))
	if len(cfgs.Items) != 1 {
		t.Fatalf("configs %+v", cfgs)
	}
	toml := cfgs.Items[0].TOML
	for _, want := range []string{`serverAddr = "203.0.113.10"`, `user = "` + mac.DeviceID + `"`, `auth.token = "frp-test-token"`, `metadatas.frp_secret`, `name = "ssh-mac"`, `remotePort = 6022`, `customDomains = ["blog.example.com"]`, `subdomain = "media"`, `secretKey = "`} {
		if !strings.Contains(toml, want) {
			t.Fatalf("frpc toml missing %q:\n%s", want, toml)
		}
	}

	// ---- frps plugin protocol
	dev, _ := e.srv.db.DeviceByID(mac.DeviceID)
	plugin := func(op string, content any) map[string]any {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"version": "0.1.0", "op": op, "content": content})
		st, b, _ := e.raw("POST", "/internal/frps-plugin/"+node.ID+"/"+node.PluginToken+"?version=0.1.0&op="+op, "", body, map[string]string{"Content-Type": "application/json"})
		if st != 200 {
			t.Fatalf("plugin %s: %d %s", op, st, b)
		}
		var r map[string]any
		_ = json.Unmarshal(b, &r)
		return r
	}
	metas := map[string]string{"device_id": dev.ID, "frp_secret": dev.FRPSecret}
	user := map[string]any{"user": dev.ID, "metas": metas, "run_id": "run1"}
	if r := plugin("Login", map[string]any{"user": dev.ID, "metas": metas, "client_address": "198.51.100.7:5000", "version": "0.72.0"}); r["reject"] == true {
		t.Fatalf("login rejected %v", r)
	}
	if r := plugin("Login", map[string]any{"user": dev.ID, "metas": map[string]string{"device_id": dev.ID, "frp_secret": "bad"}, "client_address": "1.2.3.4:1"}); r["reject"] != true {
		t.Fatal("bad secret accepted")
	}
	g, _ := e.srv.db.DeviceByID(guest.DeviceID)
	if r := plugin("Login", map[string]any{"user": g.ID, "metas": map[string]string{"device_id": g.ID, "frp_secret": g.FRPSecret}}); r["reject"] != true {
		t.Fatal("guest frpc login accepted")
	}
	if r := plugin("NewProxy", map[string]any{"user": user, "proxy_name": dev.ID + ".ssh-mac", "proxy_type": "tcp", "remote_port": 6022}); r["reject"] == true {
		t.Fatalf("proxy rejected %v", r)
	}
	m, _ := e.srv.db.MappingByID(ssh.ID)
	if m.Status != "online" {
		t.Fatalf("status %s", m.Status)
	}
	if r := plugin("NewProxy", map[string]any{"user": user, "proxy_name": dev.ID + ".ssh-mac", "proxy_type": "tcp", "remote_port": 6500}); r["reject"] != true {
		t.Fatal("port mismatch accepted")
	}
	if r := plugin("NewProxy", map[string]any{"user": user, "proxy_name": dev.ID + ".rogue", "proxy_type": "tcp", "remote_port": 6100}); r["reject"] != true {
		t.Fatal("unregistered proxy accepted")
	}
	if r := plugin("NewProxy", map[string]any{"user": user, "proxy_name": dev.ID + ".web-blog", "proxy_type": "http", "custom_domains": []string{"evil.example.com"}}); r["reject"] != true {
		t.Fatal("domain mismatch accepted")
	}
	if r := plugin("Ping", map[string]any{"user": user}); r["reject"] == true {
		t.Fatal("ping rejected")
	}
	var clients struct{ Items []map[string]any }
	e.must(e.admin.Do(e.ctx(), "GET", "/api/v1/admin/frp/clients", nil, &clients))
	if len(clients.Items) != 1 || clients.Items[0]["device_id"] != dev.ID {
		t.Fatalf("frpc clients %v", clients.Items)
	}

	// stop from the console: device notified, next ping rejected once, NewProxy refused
	cn := e.connect(mac)
	e.must(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/frp/mappings/"+ssh.ID+"/stop", nil, nil))
	if _, err := cn.Wait(e.ctx(), proto.TFRPConfig); err != nil {
		t.Fatal(err)
	}
	if r := plugin("Ping", map[string]any{"user": user}); r["reject"] != true {
		t.Fatal("ping after stop should force a reconnect")
	}
	if r := plugin("Ping", map[string]any{"user": user}); r["reject"] == true {
		t.Fatal("kick should be one-shot")
	}
	if r := plugin("NewProxy", map[string]any{"user": user, "proxy_name": dev.ID + ".ssh-mac", "proxy_type": "tcp", "remote_port": 6022}); r["reject"] != true {
		t.Fatal("stopped mapping accepted")
	}
	e.must(mac.Do(e.ctx(), "GET", "/api/v1/me/frpc", nil, &cfgs))
	if strings.Contains(cfgs.Items[0].TOML, `name = "ssh-mac"`) {
		t.Fatal("stopped mapping still in frpc config")
	}

	// level drop to guest revokes frp access at the next ping
	e.must(e.admin.Do(e.ctx(), "PATCH", "/api/v1/admin/devices/"+mac.DeviceID, map[string]string{"level": "guest"}, nil))
	_ = plugin("Ping", map[string]any{"user": user}) // consumes the kick
	if r := plugin("Ping", map[string]any{"user": user}); r["reject"] != true {
		t.Fatal("guest ping accepted")
	}
	e.must(e.admin.Do(e.ctx(), "PATCH", "/api/v1/admin/devices/"+mac.DeviceID, map[string]string{"level": "member"}, nil))

	// admin list / filter / stop-all / delete
	var all struct{ Items []store.Mapping }
	e.must(e.admin.Do(e.ctx(), "GET", "/api/v1/admin/frp/mappings?state=on&q=blog", nil, &all))
	if len(all.Items) != 1 || all.Items[0].Name != "web-blog" {
		t.Fatalf("filter %+v", all.Items)
	}
	var stopped struct{ Stopped int }
	e.must(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/frp/stop-all", map[string]string{"node": "tpe"}, &stopped))
	if stopped.Stopped != 3 {
		t.Fatalf("stopped %d", stopped.Stopped)
	}
	// admin-level device may use FRP management endpoints (client "Servers" tab)
	ops := e.device("ops", "admin")
	e.must(ops.Do(e.ctx(), "POST", "/api/v1/admin/frp/mappings/"+blog.ID+"/start", nil, nil))
	e.expectErr(ops.Do(e.ctx(), "DELETE", "/api/v1/admin/frp/nodes/"+node.ID, nil, nil), 403, "forbidden")
	e.expectErr(mac.Do(e.ctx(), "POST", "/api/v1/admin/frp/mappings/"+blog.ID+"/stop", nil, nil), 403, "forbidden")
	e.must(mac.Do(e.ctx(), "DELETE", "/api/v1/me/mappings/"+adb.ID, nil, nil))
	// bad plugin token
	st, _, _ := e.raw("POST", "/internal/frps-plugin/"+node.ID+"/wrong", "", []byte(`{}`), nil)
	if st != 403 {
		t.Fatalf("plugin auth: %d", st)
	}
}

func TestACL(t *testing.T) {
	e := newEnv(t)
	dev := e.device("phone", "guest")
	// deny list blocks everything from the range; your own address is protected
	e.expectErr(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/security/acl", map[string]string{"action": "deny", "cidr": "127.0.0.0/8"}, nil), 409, "conflict")
	var r struct{ ID string }
	e.must(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/security/acl", map[string]string{"action": "deny", "cidr": "203.0.113.0/24", "note": "scanner"}, &r))
	if !e.srv.acl.Denied("203.0.113.9") || e.srv.acl.Denied("198.51.100.1") {
		t.Fatal("deny semantics")
	}
	// allow list limits console access but not devices
	e.must(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/security/acl", map[string]string{"action": "allow", "cidr": "127.0.0.1"}, nil))
	if e.srv.acl.AllowedConsole("10.1.1.1") || !e.srv.acl.AllowedConsole("127.0.0.1") {
		t.Fatal("allow semantics")
	}
	e.must(dev.Do(e.ctx(), "GET", "/api/v1/peers", nil, nil))
	e.must(e.admin.Do(e.ctx(), "DELETE", "/api/v1/admin/security/acl/"+r.ID, nil, nil))
	var list struct{ Items []map[string]any }
	e.must(e.admin.Do(e.ctx(), "GET", "/api/v1/admin/security/acl", nil, &list))
	if len(list.Items) != 1 {
		t.Fatalf("acl %v", list.Items)
	}
}

func TestRelayFiles(t *testing.T) {
	e := newEnv(t)
	a, b := e.device("a", "guest"), e.device("b", "guest")
	cb := e.connect(b)
	data := []byte(strings.Repeat("meshport-file-", 5000))
	sum := sha256.Sum256(data)
	var f struct {
		ID string `json:"id"`
	}
	e.must(a.Do(e.ctx(), "POST", "/api/v1/files", map[string]any{"to": b.DeviceID, "name": "../../report.pdf", "size": len(data), "sha256": hex.EncodeToString(sum[:])}, &f))
	half := len(data) / 2
	st, body, h := e.raw("PATCH", "/api/v1/files/"+f.ID, a.Token, data[:half], map[string]string{"Upload-Offset": "0"})
	if st != 200 || h.Get("Upload-Offset") != strconv.Itoa(half) {
		t.Fatalf("chunk1 %d %s", st, body)
	}
	// wrong offset -> 409 with the right offset
	st, _, h = e.raw("PATCH", "/api/v1/files/"+f.ID, a.Token, data[half:], map[string]string{"Upload-Offset": "0"})
	if st != 409 || h.Get("Upload-Offset") != strconv.Itoa(half) {
		t.Fatalf("offset check %d", st)
	}
	st, body, _ = e.raw("PATCH", "/api/v1/files/"+f.ID, a.Token, data[half:], map[string]string{"Upload-Offset": strconv.Itoa(half)})
	if st != 200 || !strings.Contains(string(body), `"complete":true`) {
		t.Fatalf("chunk2 %d %s", st, body)
	}
	of, err := cb.Wait(e.ctx(), proto.TFileOffer)
	e.must(err)
	var offer proto.FileOffer
	_ = json.Unmarshal(of.D, &offer)
	if offer.Name != "report.pdf" || offer.Size != int64(len(data)) {
		t.Fatalf("offer %+v", offer)
	}
	st, got, _ := e.raw("GET", "/api/v1/files/"+f.ID+"/content", b.Token, nil, nil)
	if st != 200 || string(got) != string(data) {
		t.Fatalf("download %d len %d", st, len(got))
	}
	// third parties cannot read it
	c := e.device("c", "guest")
	st, _, _ = e.raw("GET", "/api/v1/files/"+f.ID+"/content", c.Token, nil, nil)
	if st != 404 {
		t.Fatalf("third party download %d", st)
	}
}

func TestWebhooksBackupsSystem(t *testing.T) {
	e := newEnv(t)
	var mu sync.Mutex
	var got []map[string]any
	var sigOK = true
	var secret string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sig := r.Header.Get("X-Meshport-Signature")
		parts := strings.Split(sig, ",")
		ts := strings.TrimPrefix(parts[0], "t=")
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(ts + "."))
		mac.Write(body)
		mu.Lock()
		if len(parts) != 2 || strings.TrimPrefix(parts[1], "v1=") != hex.EncodeToString(mac.Sum(nil)) {
			sigOK = false
		}
		var ev map[string]any
		_ = json.Unmarshal(body, &ev)
		got = append(got, ev)
		mu.Unlock()
	}))
	defer hook.Close()
	var wh struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	}
	e.must(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/system/webhooks", map[string]any{"url": hook.URL, "events": []string{"device.*", "webhook.test"}}, &wh))
	secret = wh.Secret
	var test map[string]any
	e.must(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/system/webhooks/"+wh.ID+"/test", nil, &test))
	if test["ok"] != true {
		t.Fatalf("test %v", test)
	}
	e.device("hooked", "guest") // device.registered / device.online events
	eventually(t, "webhook delivery", func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, ev := range got {
			if ev["type"] == "device.registered" {
				return true
			}
		}
		return false
	})
	mu.Lock()
	if !sigOK {
		t.Fatal("webhook signature did not verify")
	}
	mu.Unlock()

	// backups
	var bk struct{ ID string }
	e.must(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/system/backups", nil, &bk))
	var bl struct {
		Items []map[string]any `json:"items"`
	}
	e.must(e.admin.Do(e.ctx(), "GET", "/api/v1/admin/system/backups", nil, &bl))
	// a scheduled backup may also exist when the test runs during the backup hour
	var manual map[string]any
	for _, it := range bl.Items {
		if it["id"] == bk.ID {
			manual = it
		}
	}
	if manual == nil || manual["status"] != "ok" {
		t.Fatalf("backups %v", bl.Items)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "backups", manual["file"].(string))); err != nil {
		t.Fatal(err)
	}
	st, body, _ := e.raw("GET", "/api/v1/admin/system/backups/"+bk.ID+"/download", e.admin.Token, nil, nil)
	if st != 200 || !strings.HasPrefix(string(body), "SQLite format 3") {
		t.Fatalf("download %d", st)
	}
	// system status, versions, db, settings, docs
	var status struct{ Services []serviceStatus }
	e.must(e.admin.Do(e.ctx(), "GET", "/api/v1/admin/system/status", nil, &status))
	if len(status.Services) < 3 {
		t.Fatal("services")
	}
	var dbs map[string]any
	e.must(e.admin.Do(e.ctx(), "GET", "/api/v1/admin/system/db?check=1", nil, &dbs))
	if dbs["integrity"] != "ok" {
		t.Fatalf("db %v", dbs)
	}
	e.expectErr(e.admin.Do(e.ctx(), "PATCH", "/api/v1/admin/system/settings", map[string]any{"accent": "purple"}, nil), 400, "bad_request")
	e.expectErr(e.admin.Do(e.ctx(), "PATCH", "/api/v1/admin/system/settings", map[string]any{"nope": 1}, nil), 400, "bad_request")
	var settings store.Settings
	e.must(e.admin.Do(e.ctx(), "PATCH", "/api/v1/admin/system/settings", map[string]any{"accent": "terracotta", "log_retention_days": 30}, &settings))
	if settings.Accent != "terracotta" || settings.LogRetentionDays != 30 || !settings.RelayEnabled {
		t.Fatalf("settings %+v", settings)
	}
	var up map[string]any
	e.must(e.admin.Do(e.ctx(), "GET", "/api/v1/admin/system/update", nil, &up))
	if up["available"] != false {
		t.Fatalf("update %v", up)
	}
	st, body, _ = e.raw("GET", "/api/v1/openapi.json", "", nil, nil)
	if st != 200 || !strings.Contains(string(body), `"/api/v1/admin/frp/mappings"`) {
		t.Fatalf("openapi %d", st)
	}
	st, _, _ = e.raw("GET", "/api/docs", "", nil, nil)
	if st != 200 {
		t.Fatal("docs")
	}
	// audit trail captured the changes
	var audit struct{ Items []map[string]any }
	e.must(e.admin.Do(e.ctx(), "GET", "/api/v1/admin/audit?action=settings", nil, &audit))
	if len(audit.Items) == 0 {
		t.Fatal("audit")
	}
	// hub restart disconnects devices
	var rs map[string]any
	e.must(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/system/services/hub/restart", nil, &rs))
}

func TestKeyRotationAndMetrics(t *testing.T) {
	e := newEnv(t)
	d := e.device("laptop", "member")
	cn := e.connect(d)
	e.must(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/devices/"+d.DeviceID+"/rotate-key", nil, nil))
	f, err := cn.Wait(e.ctx(), proto.TCmd)
	e.must(err)
	var cmd proto.Cmd
	_ = json.Unmarshal(f.D, &cmd)
	if cmd.Name != "rotate-key" {
		t.Fatal(cmd.Name)
	}
	old, _ := e.srv.db.DeviceByID(d.DeviceID)
	e.must(d.RotateKey(e.ctx()))
	nw, _ := e.srv.db.DeviceByID(d.DeviceID)
	if nw.Fingerprint == old.Fingerprint || nw.KeyRotateRequired {
		t.Fatal("key not rotated")
	}
	e.must(d.Login(e.ctx())) // new key works
	var keys struct{ Items []map[string]any }
	e.must(e.admin.Do(e.ctx(), "GET", "/api/v1/admin/security/keys", nil, &keys))
	if keys.Items[0]["state"] != "ok" {
		t.Fatalf("keys %v", keys.Items)
	}
	// metrics are sampled every 2 s
	time.Sleep(2500 * time.Millisecond)
	var m struct {
		Latest map[string]float64 `json:"latest"`
		Series map[string][]any   `json:"series"`
	}
	e.must(e.admin.Do(e.ctx(), "GET", "/api/v1/admin/metrics?range=live&keys=cpu,mem,devices_online", nil, &m))
	if m.Latest["devices_online"] != 1 || len(m.Series["mem"]) == 0 {
		t.Fatalf("metrics %+v", m)
	}
	var ov map[string]any
	e.must(e.admin.Do(e.ctx(), "GET", "/api/v1/admin/overview", nil, &ov))
	var logs struct{ Items []map[string]any }
	time.Sleep(1100 * time.Millisecond) // log sink flushes every second
	e.must(e.admin.Do(e.ctx(), "GET", "/api/v1/admin/logs?q=device", nil, &logs))
	if len(logs.Items) == 0 {
		t.Fatal("expected persisted service logs")
	}
}
