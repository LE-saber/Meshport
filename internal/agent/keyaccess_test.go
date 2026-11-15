package agent

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func keyRequest(a *Agent, method, path string, body any, authenticated bool) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(method, "http://127.0.0.1"+path, bytes.NewReader(b))
	r.Header.Set("X-Requested-With", "meshport-test")
	if authenticated {
		r.Header.Set("X-Meshport-Token", a.UIToken())
	}
	w := httptest.NewRecorder()
	a.localHandler().ServeHTTP(w, r)
	return w
}

func TestKeyDisclosureRequiresOwnPassword(t *testing.T) {
	a := newTestAgent(t)
	password := "test-private-view-password"
	view := func(p string) *httptest.ResponseRecorder {
		return keyRequest(a, "POST", "/local/keys/view", map[string]string{"password": p}, true)
	}
	if w := view(password); w.Code != 400 {
		t.Fatal("view before password setup", w.Code)
	}
	set := func(p, c string, auth bool) *httptest.ResponseRecorder {
		return keyRequest(a, "POST", "/local/keys/password", map[string]string{"password": p, "confirmation": c}, auth)
	}
	if w := set(password, password, false); w.Code != 401 {
		t.Fatal("unauthenticated setup", w.Code)
	}
	if w := set("short", "short", true); w.Code != 400 {
		t.Fatal("weak password accepted")
	}
	if w := set(password, "mismatch", true); w.Code != 400 {
		t.Fatal("mismatched confirmation accepted")
	}
	if w := set(password, password, true); w.Code != 200 {
		t.Fatal("setup failed", w.Code)
	}
	hash := a.st.get().KeyViewPasswordHash
	if hash == "" || strings.Contains(hash, password) {
		t.Fatal("plaintext password stored")
	}
	if w := set("different-password", "different-password", true); w.Code != 400 || a.st.get().KeyViewPasswordHash != hash {
		t.Fatal("existing password overwritten")
	}
	if w := view("wrong-password"); w.Code != 400 || strings.Contains(w.Body.String(), a.st.get().fingerprint()) {
		t.Fatal("wrong password revealed keys")
	}
	w := view(password)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("verified view failed", w.Code)
	}
	var details map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &details); err != nil {
		t.Fatal(err)
	}
	if details["identity_fingerprint"] != a.st.get().fingerprint() || details["e2e_public_key"] != a.st.get().boxPub() {
		t.Fatal("incorrect device keys")
	}
	if strings.Contains(w.Body.String(), a.st.get().Identity) || strings.Contains(w.Body.String(), a.st.get().BoxKey) {
		t.Fatal("private material exported")
	}
	if w := view(""); w.Code != 400 {
		t.Fatal("previous success left API unlocked")
	}
	reloaded, err := loadState(a.st.path)
	if err != nil || reloaded.get().KeyViewPasswordHash != hash {
		t.Fatal("password not persisted", err)
	}
}

func TestDefaultViewsExcludeKeyDetails(t *testing.T) {
	a := newTestAgent(t)
	_ = a.st.update(func(s *State) { s.ServerPin = "sha256/server-secret-fingerprint" })
	a.mu.Lock()
	a.peers["peer"] = &Peer{ID: "peer", Fingerprint: "SHA256:peer-fingerprint", BoxPubKey: "peer-public-key"}
	a.mu.Unlock()
	for _, path := range []string{"/local/state", "/local/peers"} {
		w := keyRequest(a, "GET", path, nil, true)
		if w.Code != 200 {
			t.Fatal(path, w.Code)
		}
		for _, secret := range []string{a.st.get().fingerprint(), a.st.get().boxPub(), a.st.get().ServerPin, "peer-fingerprint", "peer-public-key", "key_view_password_hash"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("default view leaked key detail", path)
			}
		}
	}
	if a.peer("peer").BoxPubKey != "peer-public-key" {
		t.Fatal("redaction changed protocol keys")
	}
}

func TestKeyPasswordAttemptLimit(t *testing.T) {
	a := newTestAgent(t)
	password := "valid-password"
	keyRequest(a, "POST", "/local/keys/password", map[string]string{"password": password, "confirmation": password}, true)
	for i := 0; i < 5; i++ {
		keyRequest(a, "POST", "/local/keys/view", map[string]string{"password": "wrong"}, true)
	}
	if w := keyRequest(a, "POST", "/local/keys/view", map[string]string{"password": password}, true); w.Code != 429 {
		t.Fatal("attempt limit bypassed", w.Code)
	}
	a.keyAccess.Lock()
	a.keyAccess.blockedUntil = time.Now().Add(-time.Second)
	a.keyAccess.Unlock()
	if w := keyRequest(a, "POST", "/local/keys/view", map[string]string{"password": password}, true); w.Code != 200 {
		t.Fatal("valid password failed after cooldown", w.Code)
	}
}

func TestLocalDeviceProxyDoesNotBypassKeyPassword(t *testing.T) {
	a := newTestAgent(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"device": map[string]string{"id": "device", "pubkey": "public-identity", "box_pubkey": "public-e2e", "fingerprint": "SHA256:private-view-only"}})
	}))
	defer server.Close()
	_ = a.st.update(func(s *State) { s.Server = server.URL })
	w := keyRequest(a, "GET", "/local/api/me/device", nil, true)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	if strings.Contains(w.Body.String(), "public-identity") || strings.Contains(w.Body.String(), "public-e2e") || strings.Contains(w.Body.String(), "SHA256:") {
		t.Fatal("proxy bypassed password gate")
	}
	if !strings.Contains(w.Body.String(), "device") {
		t.Fatal("device information lost")
	}
}
