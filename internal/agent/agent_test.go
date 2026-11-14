package agent

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/cookiejar"
	"path/filepath"
	"strings"
	"testing"
)

func newTestAgent(t *testing.T) *Agent {
	t.Helper()
	a, err := New(Options{DataDir: t.TempDir(), Version: "test", Platform: "linux"})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestSealOpen(t *testing.T) {
	a, b := newTestAgent(t), newTestAgent(t)
	sa, sb := a.st.get(), b.st.get()
	env := &Envelope{V: 1, K: "text", MID: "m_1", TS: 42, Text: "你好 hello"}
	body, err := seal(env, sb.boxPub(), sa.boxPriv())
	if err != nil {
		t.Fatal(err)
	}
	got, err := open(body, sa.boxPub(), sb.boxPriv())
	if err != nil || got.Text != env.Text || got.MID != "m_1" {
		t.Fatalf("open: %v %+v", err, got)
	}
	// wrong sender key
	c := newTestAgent(t)
	if _, err := open(body, c.st.get().boxPub(), sb.boxPriv()); err == nil {
		t.Fatal("opened with the wrong sender key")
	}
	// tampered ciphertext
	raw, _ := base64.StdEncoding.DecodeString(body)
	raw[len(raw)-1] ^= 1
	if _, err := open(base64.StdEncoding.EncodeToString(raw), sa.boxPub(), sb.boxPriv()); err == nil {
		t.Fatal("tampered message opened")
	}
	if _, err := seal(env, "", sa.boxPriv()); err == nil {
		t.Fatal("sealed without a peer key")
	}
}

func TestFileCipherRoundTrip(t *testing.T) {
	for _, size := range []int64{0, 1, fileChunk - 1, fileChunk, fileChunk + 1, 3*fileChunk + 5} {
		key, nonce := newFileKey()
		ref := &FileRef{Size: size, Key: key, Nonce: nonce}
		fc, err := newFileCipher(ref)
		if err != nil {
			t.Fatal(err)
		}
		plain := make([]byte, size)
		_, _ = rand.Read(plain)
		var ct bytes.Buffer
		if err := fc.encryptStream(bytes.NewReader(plain), func(_ int64, c []byte) error { ct.Write(c); return nil }); err != nil {
			t.Fatal(err)
		}
		if int64(ct.Len()) != fc.cipherSize() || fc.cipherSize() != encCipherSize(size) {
			t.Fatalf("size %d: cipher %d, want %d", size, ct.Len(), fc.cipherSize())
		}
		// decrypt with odd write splits
		var out bytes.Buffer
		dw := fc.decryptTo(&out)
		data := ct.Bytes()
		for i := 0; i < len(data); i += 7777 {
			end := i + 7777
			if end > len(data) {
				end = len(data)
			}
			if _, err := dw.Write(data[i:end]); err != nil {
				t.Fatalf("size %d: %v", size, err)
			}
		}
		if !dw.complete() || !bytes.Equal(out.Bytes(), plain) {
			t.Fatalf("size %d: round trip mismatch (complete=%v)", size, dw.complete())
		}
		if size > fileChunk {
			// truncation: drop the final chunk
			dw2 := fc.decryptTo(io.Discard)
			_, _ = dw2.Write(data[:fileChunk+16])
			if dw2.complete() {
				t.Fatal("truncated file reported complete")
			}
		}
		if size > 2*fileChunk {
			// reordering: swap chunks 0 and 1 fails authentication
			sw := append(append([]byte{}, data[fileChunk+16:2*(fileChunk+16)]...), data[:fileChunk+16]...)
			if _, err := fc.decryptTo(io.Discard).Write(sw); err == nil {
				t.Fatal("reordered chunks accepted")
			}
		}
	}
}

func TestMessageStorePersistence(t *testing.T) {
	dir := t.TempDir()
	ms, err := openMessageStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := &Message{ID: "m_a", Peer: "dev_x", Out: true, Kind: "text", Text: "hi", TS: 1, Status: "sending"}
	if !ms.add(m) || ms.add(&Message{ID: "m_a", Peer: "dev_x"}) {
		t.Fatal("dedupe failed")
	}
	ms.update("m_a", func(m *Message) { m.Status = "sent"; m.ServerID = "msg_1" })
	ms.add(&Message{ID: "m_b", Peer: "dev_x", Kind: "text", Text: "yo", TS: 2, Status: "received"})
	ms2, err := openMessageStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	list := ms2.list("dev_x", 0, 10)
	if len(list) != 2 || list[0].Status != "sent" || ms2.idByServer("msg_1") != "m_a" {
		t.Fatalf("reload: %+v", list)
	}
	if ms2.summaries()["dev_x"].Unread != 1 {
		t.Fatal("unread count")
	}
	if seen := ms2.markSeen("dev_x"); len(seen) != 1 || ms2.unreadTotal() != 0 {
		t.Fatal("markSeen")
	}
}

func TestSafeNames(t *testing.T) {
	for in, want := range map[string]string{"../../etc/passwd": "passwd", `C:\x\a<b>.txt`: "a_b_.txt", "  .. ": "file", "ok.png": "ok.png"} {
		if got := safeName(in); got != want {
			t.Errorf("safeName(%q) = %q, want %q", in, got, want)
		}
	}
	dir := t.TempDir()
	p1 := uniquePath(dir, "a.txt")
	if filepath.Base(p1) != "a.txt" {
		t.Fatal(p1)
	}
}

func TestLocalAPIAuth(t *testing.T) {
	a := newTestAgent(t)
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	defer a.Stop()
	base := "http://" + a.Addr()
	get := func(c *http.Client, path string, hdr map[string]string) int {
		req, _ := http.NewRequest("GET", base+path, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	plain := &http.Client{}
	if s := get(plain, "/local/state", nil); s != 401 {
		t.Fatalf("no token: %d", s)
	}
	if s := get(plain, "/local/state", map[string]string{"X-Meshport-Token": a.UIToken()}); s != 200 {
		t.Fatalf("header token: %d", s)
	}
	// DNS rebinding: foreign Host header
	req, _ := http.NewRequest("GET", base+"/local/ping", nil)
	req.Host = "evil.example:80"
	resp, _ := plain.Do(req)
	if resp.StatusCode != 403 {
		t.Fatalf("foreign host: %d", resp.StatusCode)
	}
	resp.Body.Close()
	// one-time login code sets the cookie, and cannot be reused
	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar}
	u := a.LoginURL()
	if s := get(browser, strings.TrimPrefix(u, base), nil); s != 200 {
		t.Fatalf("login code: %d", s)
	}
	if s := get(browser, "/local/state", nil); s != 200 {
		t.Fatalf("cookie: %d", s)
	}
	if s := get(plain, strings.TrimPrefix(u, base), nil); s != 403 {
		t.Fatalf("reused code: %d", s)
	}
	// writes need X-Requested-With
	req, _ = http.NewRequest("POST", base+"/local/reconnect", nil)
	resp, _ = browser.Do(req)
	if resp.StatusCode != 403 {
		t.Fatalf("csrf: %d", resp.StatusCode)
	}
	resp.Body.Close()
	// proxy allowlist
	req, _ = http.NewRequest("GET", base+"/local/api/admin/devices", nil)
	resp, _ = browser.Do(req)
	if resp.StatusCode != 404 {
		t.Fatalf("proxy allowlist: %d", resp.StatusCode)
	}
	resp.Body.Close()
}
