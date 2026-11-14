//go:build linux && !android

// The integration tests start the (Linux-only) server in-process.

package agent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"meshport/internal/api"
	"meshport/internal/auth"
	"meshport/internal/config"
	"meshport/internal/events"
	"meshport/internal/policy"
	"meshport/internal/store"
	"meshport/pkg/client"
	"meshport/pkg/enrollment"
)

// testServer starts a real meshportd API server in-process.
func testServer(t *testing.T, pinned ...bool) (url string, admin *client.Client) {
	t.Helper()
	auth.SetFastHashing()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.DataDir = dir
	cfg.InternalListen = ""
	cfg.FRP.AuthToken = "frp-test-token"
	cfg.TURN.URLs = nil // host candidates only: both agents run on this machine
	cfg.FRP.PollInterval = time.Hour
	cfg.FRP.Binary = os.Getenv("MESHPORT_FRPS")
	var ts *httptest.Server
	pin := ""
	if len(pinned) > 0 && pinned[0] {
		ts = httptest.NewTLSServer(nil)
		pin = enrollment.Pin(ts.Certificate())
		cfg.EnrollmentURL = ts.URL
		cfg.EnrollmentCert = filepath.Join(dir, "server.crt")
		if err := os.WriteFile(cfg.EnrollmentCert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw}), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "meshport.db"))
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	sink := events.NewLogSink(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}), bus)
	srv, err := api.New(api.Options{Config: cfg, DB: db, Bus: bus, Logs: sink, Logger: slog.New(sink)})
	if err != nil {
		t.Fatal(err)
	}
	if ts == nil {
		ts = httptest.NewServer(srv.Handler())
	} else {
		ts.Config.Handler = srv.Handler()
	}
	cfg.PublicURL = ts.URL
	ctx, cancel := context.WithCancel(context.Background())
	srv.Start(ctx)
	t.Cleanup(func() { ts.Close(); cancel(); srv.Stop(); db.Close() })
	h, _ := auth.HashPassword("admin-password-1")
	if err := db.CreateUser(&store.User{Username: "admin", PasswordHash: h, Role: policy.RoleAdmin}); err != nil {
		t.Fatal(err)
	}
	admin = client.NewWithPin(ts.URL, pin)
	var out struct {
		Token string `json:"token"`
	}
	if err := admin.Do(context.Background(), "POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "admin-password-1"}, &out); err != nil {
		t.Fatal(err)
	}
	admin.Token = out.Token
	return ts.URL, admin
}

func enroll(t *testing.T, admin *client.Client, level string) string {
	t.Helper()
	var out struct {
		Token string `json:"token"`
		Code  string `json:"join_code"`
	}
	if err := admin.Do(context.Background(), "POST", "/api/v1/admin/enroll-tokens", map[string]any{"level": level, "auto_approve": true, "uses": 1}, &out); err != nil {
		t.Fatal(err)
	}
	if out.Code != "" {
		return out.Code
	}
	return out.Token
}

func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func startAgent(t *testing.T, server, name, token string) *Agent {
	t.Helper()
	a, err := New(Options{DataDir: t.TempDir(), Version: "test", OS: "Linux", Kind: "laptop", Platform: "linux", LogStderr: os.Getenv("MESHPORT_TEST_LOG") != ""})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Stop)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := a.Setup(ctx, SetupRequest{Server: server, Name: name, EnrollToken: token}); err != nil {
		t.Fatalf("setup %s: %v", name, err)
	}
	waitFor(t, name+" online", 10*time.Second, func() bool { return a.Status() == "online" })
	return a
}

func lastMsg(a *Agent, peer string) *Message {
	l := a.msgs.list(peer, 0, 1)
	if len(l) == 0 {
		return nil
	}
	return &l[0]
}

func TestAgentsEndToEnd(t *testing.T) {
	testAgentsEndToEnd(t, false)
}

func TestAgentsEndToEndPinned(t *testing.T) {
	testAgentsEndToEnd(t, true)
}

func TestPinnedAgentRestart(t *testing.T) {
	url, admin := testServer(t, true)
	a := startAgent(t, url, "persistent pin", enroll(t, admin, "member"))
	state := a.st.get()
	if state.ServerPin == "" {
		t.Fatal("server pin was not saved")
	}
	a.Stop()
	b, err := New(a.opt)
	if err != nil {
		t.Fatal(err)
	}
	if b.st.get().ServerPin != state.ServerPin || b.myID() != state.DeviceID {
		t.Fatal("saved identity or pin changed")
	}
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Stop)
	waitFor(t, "pinned identity reconnects after restart", 10*time.Second, func() bool { return b.Status() == "online" })
}

func testAgentsEndToEnd(t *testing.T, pinned bool) {
	if testing.Short() {
		t.Skip("integration test")
	}
	url, admin := testServer(t, pinned)
	a := startAgent(t, url, "alpha", enroll(t, admin, "member"))
	b := startAgent(t, url, "beta", enroll(t, admin, "member"))
	aid, bid := a.myID(), b.myID()
	waitFor(t, "peers with keys", 10*time.Second, func() bool {
		_ = a.refreshPeers(context.Background())
		_ = b.refreshPeers(context.Background())
		pa, pb := a.peer(bid), b.peer(aid)
		return pa != nil && pb != nil && pa.BoxPubKey != "" && pb.BoxPubKey != "" && pa.Online && pb.Online
	})

	t.Run("relay text with receipts", func(t *testing.T) {
		m, err := a.SendText(bid, "hello via relay", "relay")
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, "beta receives", 10*time.Second, func() bool {
			r := lastMsg(b, aid)
			return r != nil && r.Text == "hello via relay" && r.Path == "relay" && !r.Out
		})
		waitFor(t, "delivered receipt", 10*time.Second, func() bool { x, _ := a.msgs.get(m.ID); return x.Status == "delivered" })
		b.MarkRead(aid)
		waitFor(t, "read receipt", 10*time.Second, func() bool { x, _ := a.msgs.get(m.ID); return x.Status == "read" })
	})

	t.Run("p2p text", func(t *testing.T) {
		m, err := a.SendText(bid, "hello direct", "p2p")
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, "beta receives over p2p", 20*time.Second, func() bool {
			r := lastMsg(b, aid)
			return r != nil && r.Text == "hello direct" && r.Path == "p2p"
		})
		waitFor(t, "p2p ack", 10*time.Second, func() bool { x, _ := a.msgs.get(m.ID); return x.Status == "delivered" && x.Path == "p2p" })
		// reply over the same link
		if _, err := b.SendText(aid, "pong", "auto"); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "alpha receives reply", 10*time.Second, func() bool { r := lastMsg(a, bid); return r != nil && r.Text == "pong" && r.Path == "p2p" })
		b.MarkRead(aid)
		waitFor(t, "p2p read receipt", 10*time.Second, func() bool { x, _ := a.msgs.get(m.ID); return x.Status == "read" })
	})

	sendFile := func(t *testing.T, mode string, size int) {
		data := make([]byte, size)
		_, _ = rand.Read(data)
		src := filepath.Join(t.TempDir(), "payload-"+mode+".bin")
		if err := os.WriteFile(src, data, 0o600); err != nil {
			t.Fatal(err)
		}
		m, err := a.SendFile(bid, src, "", mode, false)
		if err != nil {
			t.Fatal(err)
		}
		var got string
		waitFor(t, mode+" file received", 60*time.Second, func() bool {
			r, ok := b.msgs.get(m.ID)
			got = r.Local
			return ok && r.Local != ""
		})
		recv, err := os.ReadFile(got)
		if err != nil || !bytes.Equal(recv, data) {
			t.Fatalf("%s file content mismatch (err=%v, %d vs %d bytes)", mode, err, len(recv), len(data))
		}
		waitFor(t, mode+" sender transfer done", 20*time.Second, func() bool {
			x := a.xfers.get(m.File.TID)
			return x != nil && x.snapshot().State == "done"
		})
		x, _ := b.msgs.get(m.ID)
		want := "relay"
		if mode == "p2p" {
			want = "p2p"
		}
		if x.Path != want && !(mode == "p2p" && x.Path == "turn") {
			t.Fatalf("%s file path = %q", mode, x.Path)
		}
	}
	t.Run("relay file", func(t *testing.T) { sendFile(t, "relay", 300<<10+17) })
	t.Run("p2p file", func(t *testing.T) { sendFile(t, "p2p", 2<<20+5) })
	t.Run("p2p-only waits while peer offline", func(t *testing.T) {
		b.Stop()
		waitFor(t, "beta offline", 10*time.Second, func() bool { p := a.peer(bid); return p != nil && !p.Online })
		m, err := a.SendText(bid, "later", "p2p")
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, "pending", 5*time.Second, func() bool { x, _ := a.msgs.get(m.ID); return x.Status == "pending" })
	})
}

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// TestEmbeddedFRPC publishes a local TCP service through a real frps managed
// by meshportd, using the agent's in-process frpc. Needs MESHPORT_FRPS.
func TestEmbeddedFRPC(t *testing.T) {
	if os.Getenv("MESHPORT_FRPS") == "" {
		t.Skip("set MESHPORT_FRPS to an frps v0.72 binary")
	}
	url, admin := testServer(t)
	echo, _ := net.Listen("tcp", "127.0.0.1:0")
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				line, _ := bufio.NewReader(c).ReadString('\n')
				fmt.Fprintf(c, "echo:%s", line)
			}(c)
		}
	}()
	bind, dash, remote := freePort(t), freePort(t), freePort(t)
	ctx := context.Background()
	if err := admin.Do(ctx, "POST", "/api/v1/admin/frp/nodes", map[string]any{
		"name": "local", "public_addr": "127.0.0.1", "bind_port": bind, "allow_ports": fmt.Sprint(remote),
		"dashboard_addr": fmt.Sprintf("127.0.0.1:%d", dash), "managed": true}, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "frps listening", 15*time.Second, func() bool {
		c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", bind))
		if err == nil {
			c.Close()
		}
		return err == nil
	})
	a := startAgent(t, url, "frp-box", enroll(t, admin, "member"))
	var m struct {
		ID         string `json:"id"`
		PublicAddr string `json:"public_addr"`
	}
	if err := a.api(ctx, "POST", "/me/mappings", map[string]any{"name": "echo", "node": "local", "proto": "tcp",
		"local_port": echo.Addr().(*net.TCPAddr).Port, "remote_port": remote}, &m); err != nil {
		t.Fatal(err)
	}
	roundTrip := func() string {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", remote), time.Second)
		if err != nil {
			return ""
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(2 * time.Second))
		fmt.Fprint(c, "ping\n")
		s, _ := bufio.NewReader(c).ReadString('\n')
		return s
	}
	waitFor(t, "forwarding through embedded frpc", 30*time.Second, func() bool { return roundTrip() == "echo:ping\n" })
	waitFor(t, "frpc status running", 10*time.Second, func() bool {
		st := a.frpc.status()
		return len(st.Nodes) == 1 && len(st.Nodes[0].Proxies) == 1 && st.Nodes[0].Proxies[0].Phase == "running"
	})
	// stopping the mapping on the server removes it from the hot-reloaded frpc
	if err := a.api(ctx, "POST", "/me/mappings/"+m.ID+"/stop", nil, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "mapping stopped", 30*time.Second, func() bool { return roundTrip() == "" })
	if err := a.api(ctx, "POST", "/me/mappings/"+m.ID+"/start", nil, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "mapping restarted", 30*time.Second, func() bool { return roundTrip() == "echo:ping\n" })
}
