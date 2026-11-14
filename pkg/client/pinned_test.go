package client

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
	"meshport/pkg/enrollment"
)

func TestPinnedHTTPSAndWebSocket(t *testing.T) {
	var reached atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		if r.URL.Path == "/api/v1/ws" {
			ws, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer ws.CloseNow()
			_ = ws.Write(r.Context(), websocket.MessageText, []byte(`{"t":"hello","d":{"device_id":"dev_test"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer s.Close()
	ctx := context.Background()
	c := NewWithPin(s.URL, enrollment.Pin(s.Certificate()))
	var out map[string]any
	if err := c.Do(ctx, "GET", "/healthz", nil, &out); err != nil || out["ok"] != true {
		t.Fatalf("pinned HTTP: %v %v", out, err)
	}
	cn, err := c.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cn.Close()
	before := reached.Load()
	wrong := NewWithPin(s.URL, "sha256/"+base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err := wrong.Do(ctx, "POST", "/register", map[string]string{"secret": "must-not-arrive"}, nil); err == nil {
		t.Fatal("wrong key accepted")
	}
	if _, err := wrong.Connect(ctx); err == nil {
		t.Fatal("wrong websocket key accepted")
	}
	if reached.Load() != before {
		t.Fatal("credentials reached a server with the wrong key")
	}
	if err := New(s.URL).Do(ctx, "GET", "/healthz", nil, nil); err == nil {
		t.Fatal("untrusted certificate accepted without invitation")
	}
}

func TestPinnedClientRefusesPlaintextAndRedirect(t *testing.T) {
	var leaked atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer plain.Close()
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL, http.StatusTemporaryRedirect)
	}))
	defer s.Close()
	pin := enrollment.Pin(s.Certificate())
	for _, base := range []string{plain.URL, s.URL} {
		if err := NewWithPin(base, pin).Do(context.Background(), "GET", "/", nil, nil); err == nil {
			t.Fatal("plaintext or redirect accepted")
		}
	}
	if leaked.Load() != 0 {
		t.Fatal("redirect sent traffic to a plaintext server")
	}
}
