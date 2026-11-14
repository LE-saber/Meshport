package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The embedded console is served at "/" with client-side routing fallback,
// strict security headers, and never shadows API paths.
func TestConsoleStatic(t *testing.T) {
	e := newEnv(t)
	st, body, hdr := e.raw("GET", "/", "", nil, nil)
	if st != 200 || !strings.Contains(string(body), `<script src="/app.js">`) {
		t.Fatalf("index: %d %s", st, body)
	}
	if csp := hdr.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || hdr.Get("X-Frame-Options") != "DENY" {
		t.Fatalf("security headers: %v", hdr)
	}
	if hdr.Get("Cache-Control") != "no-cache" {
		t.Fatalf("index should not be cached: %v", hdr)
	}
	for _, p := range []string{"/app.js", "/app.css", "/icon.svg"} {
		if st, b, _ := e.raw("GET", p, "", nil, nil); st != 200 || len(b) < 100 {
			t.Fatalf("%s: %d (%d bytes)", p, st, len(b))
		}
	}
	// SPA fallback (enrollment links are /enroll#<token>)
	if st, b, _ := e.raw("GET", "/enroll", "", nil, nil); st != 200 || !strings.Contains(string(b), "app.js") {
		t.Fatalf("/enroll: %d", st)
	}
	// unknown API paths stay JSON 404s
	st, b, _ := e.raw("GET", "/api/v1/nope", "", nil, nil)
	var er map[string]map[string]string
	if st != 404 || json.Unmarshal(b, &er) != nil || er["error"]["code"] != "not_found" {
		t.Fatalf("api 404: %d %s", st, b)
	}
	// no directory traversal out of the console
	if st, b, _ := e.raw("GET", "/../../go.mod", "", nil, nil); st != 200 || strings.Contains(string(b), "module meshport") {
		t.Fatalf("traversal: %d %s", st, b)
	}

	// web_dir overrides the embedded build
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("custom console"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.srv.cfg.WebDir = dir
	if _, b, _ := e.raw("GET", "/devices", "", nil, nil); string(b) != "custom console" {
		t.Fatalf("web_dir: %s", b)
	}
	// web_dir "off" disables the console
	e.srv.cfg.WebDir = "off"
	if _, b, _ := e.raw("GET", "/", "", nil, nil); !strings.Contains(string(b), `"name":"meshportd"`) {
		t.Fatalf("off: %s", b)
	}
}
