package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"meshport/internal/auth"
	"meshport/internal/config"
	"meshport/internal/events"
	"meshport/internal/policy"
	"meshport/internal/store"
	"meshport/pkg/client"
)

func init() { auth.SetFastHashing() }

type env struct {
	t     *testing.T
	srv   *Server
	ts    *httptest.Server
	admin *client.Client // console admin (token in client.Token)
	dir   string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.DataDir = dir
	cfg.InternalListen = ""
	cfg.FRP.AuthToken = "frp-test-token"
	cfg.TURN.Secret = "turn-secret"
	cfg.TURN.URLs = []string{"stun:turn.test:3478", "turn:turn.test:3478?transport=udp"}
	cfg.FRP.PollInterval = time.Hour
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "meshport.db"))
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	level := slog.LevelWarn
	if os.Getenv("MESHPORT_TEST_LOG") != "" {
		level = slog.LevelDebug
	}
	sink := events.NewLogSink(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}), bus)
	srv, err := New(Options{Config: cfg, DB: db, Bus: bus, Logs: sink, Logger: slog.New(sink)})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	cfg.PublicURL = ts.URL
	ctx, cancel := context.WithCancel(context.Background())
	srv.Start(ctx)
	t.Cleanup(func() {
		ts.Close()
		cancel()
		srv.Stop()
		db.Close()
	})
	e := &env{t: t, srv: srv, ts: ts, dir: dir}
	h, _ := auth.HashPassword("admin-password-1")
	if err := db.CreateUser(&store.User{Username: "admin", PasswordHash: h, Role: policy.RoleAdmin}); err != nil {
		t.Fatal(err)
	}
	e.admin = e.login("admin", "admin-password-1", "")
	return e
}

func (e *env) ctx() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	e.t.Cleanup(cancel)
	return ctx
}

// login returns a client carrying a console session token.
func (e *env) login(user, pw, totp string) *client.Client {
	e.t.Helper()
	c := client.New(e.ts.URL)
	var out struct {
		Token string `json:"token"`
	}
	if err := c.Do(e.ctx(), "POST", "/api/v1/auth/login", map[string]string{"username": user, "password": pw, "totp": totp}, &out); err != nil {
		e.t.Fatalf("login %s: %v", user, err)
	}
	c.Token = out.Token
	return c
}

func (e *env) createUser(name, role string) *client.Client {
	e.t.Helper()
	pw := "pw-" + name + "-123456"
	e.must(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/users", map[string]string{"username": name, "password": pw, "role": role}, nil))
	return e.login(name, pw, "")
}

// enrollToken creates an enrollment token via the console.
func (e *env) enrollToken(level, group string, auto bool) string {
	e.t.Helper()
	var out struct {
		Token string `json:"token"`
	}
	e.must(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/enroll-tokens", map[string]any{"level": level, "group": group, "auto_approve": auto, "uses": 1}, &out))
	return out.Token
}

// device registers + logs in a device with the given level.
func (e *env) device(name, level string) *client.Client {
	e.t.Helper()
	c := client.New(e.ts.URL)
	if _, err := c.Register(e.ctx(), client.RegisterRequest{Name: name, OS: "Linux", Kind: "laptop", EnrollToken: e.enrollToken(level, "", true)}); err != nil {
		e.t.Fatalf("register %s: %v", name, err)
	}
	if err := c.Login(e.ctx()); err != nil {
		e.t.Fatalf("login %s: %v", name, err)
	}
	return c
}

func (e *env) connect(c *client.Client) *client.Conn {
	e.t.Helper()
	cn, err := c.Connect(e.ctx())
	if err != nil {
		e.t.Fatalf("connect: %v", err)
	}
	e.t.Cleanup(cn.Close)
	return cn
}

func (e *env) must(err error) {
	e.t.Helper()
	if err != nil {
		e.t.Fatal(err)
	}
}

// expectErr asserts an API error with the given status.
func (e *env) expectErr(err error, status int, code string) {
	e.t.Helper()
	ae, ok := err.(*client.APIError)
	if !ok {
		e.t.Fatalf("expected API error %d %s, got %v", status, code, err)
	}
	if ae.Status != status || (code != "" && ae.Code != code) {
		e.t.Fatalf("expected %d %s, got %d %s: %s", status, code, ae.Status, ae.Code, ae.Message)
	}
}

// raw performs a request and returns status + body.
func (e *env) raw(method, path, token string, body []byte, hdr map[string]string) (int, []byte, http.Header) {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.ts.URL+path, bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, resp.Header
}

// eventually polls cond until it is true or times out.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func dump(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

var _ = fmt.Sprintf
var _ = strings.TrimSpace
