package agent

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"meshport/pkg/client"
)

//go:embed web
var webFS embed.FS

// The local API serves the UI (desktop webview/browser, Android WebView).
// It listens on loopback only, checks the Host header (DNS rebinding) and
// requires the per-installation UI token (cookie set by /auth?t=… or the
// X-Meshport-Token header). Writes also need X-Requested-With (CSRF).

func (a *Agent) localHandler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(webFS, "web")
	files := http.FileServer(http.FS(static))
	mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'; connect-src 'self'")
		files.ServeHTTP(w, r)
	}))
	mux.HandleFunc("GET /auth", func(w http.ResponseWriter, r *http.Request) {
		if !a.useLoginCode(r.URL.Query().Get("c")) && subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("t")), []byte(a.uiToken)) != 1 {
			http.Error(w, "invalid token", http.StatusForbidden)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "mp_ui", Value: a.uiToken, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 10 * 365 * 86400})
		http.Redirect(w, r, "/", http.StatusFound)
	})
	mux.HandleFunc("GET /local/ping", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"app": "meshport", "version": a.opt.Version})
	})

	h := func(pattern string, fn func(w http.ResponseWriter, r *http.Request) (any, error)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if !a.authorized(r) {
				writeJSON(w, 401, map[string]string{"error": "unauthorized"})
				return
			}
			if r.Method != "GET" && r.Header.Get("X-Requested-With") == "" {
				writeJSON(w, 403, map[string]string{"error": "missing X-Requested-With"})
				return
			}
			out, err := fn(w, r)
			if err != nil {
				status := 400
				var ae *client.APIError
				if errors.As(err, &ae) && ae.Status >= 400 {
					status = ae.Status
					err = friendlyErr(err)
				}
				if errors.Is(err, errOffline) {
					status, err = 503, errors.New("尚未連線到伺服器")
				}
				writeJSON(w, status, map[string]string{"error": err.Error()})
				return
			}
			if out == nil {
				return // handler wrote the response
			}
			writeJSON(w, 200, out)
		})
	}

	h("POST /local/show", func(w http.ResponseWriter, r *http.Request) (any, error) {
		if a.opt.Hooks.OpenUI == nil {
			return nil, errors.New("no window")
		}
		go a.opt.Hooks.OpenUI()
		return ok(), nil
	})
	h("GET /local/state", func(w http.ResponseWriter, r *http.Request) (any, error) { return a.stateView(), nil })
	h("POST /local/setup", func(w http.ResponseWriter, r *http.Request) (any, error) {
		var req SetupRequest
		if err := readJSON(r, &req); err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		return a.Setup(ctx, req)
	})
	h("POST /local/reset", func(w http.ResponseWriter, r *http.Request) (any, error) {
		a.Reset()
		return a.stateView(), nil
	})
	h("POST /local/reconnect", func(w http.ResponseWriter, r *http.Request) (any, error) {
		a.disconnect()
		a.nudge()
		return ok(), nil
	})
	h("GET /local/peers", func(w http.ResponseWriter, r *http.Request) (any, error) {
		return map[string]any{"items": a.peerList(), "convs": a.msgs.summaries()}, nil
	})
	h("POST /local/peers/refresh", func(w http.ResponseWriter, r *http.Request) (any, error) {
		if err := a.refreshPeers(r.Context()); err != nil {
			return nil, err
		}
		return map[string]any{"items": a.peerList(), "convs": a.msgs.summaries()}, nil
	})
	h("POST /local/peers/{id}/connect", func(w http.ResponseWriter, r *http.Request) (any, error) {
		id := r.PathValue("id")
		mode := a.st.get().modeFor(id)
		if mode == "relay" {
			return map[string]any{"link": nil, "skipped": "relay mode"}, nil
		}
		if p := a.peer(id); p == nil || !p.Online {
			return nil, errors.New("對方不在線上")
		}
		_, err := a.links.ensure(r.Context(), id, mode, 15*time.Second)
		if err != nil {
			return nil, err
		}
		return map[string]any{"link": a.links.view(id)}, nil
	})
	h("POST /local/peers/{id}/disconnect", func(w http.ResponseWriter, r *http.Request) (any, error) {
		a.links.close(r.PathValue("id"), "使用者中斷", true)
		return ok(), nil
	})
	h("GET /local/messages", func(w http.ResponseWriter, r *http.Request) (any, error) {
		q := r.URL.Query()
		before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
		limit, _ := strconv.Atoi(q.Get("limit"))
		if limit <= 0 || limit > 500 {
			limit = 100
		}
		list := a.msgs.list(q.Get("peer"), before, limit)
		for i := range list {
			list[i] = a.msgView(list[i])
		}
		return map[string]any{"items": list}, nil
	})
	h("POST /local/messages", func(w http.ResponseWriter, r *http.Request) (any, error) {
		var req struct{ Peer, Text, Mode string }
		if err := readJSON(r, &req); err != nil {
			return nil, err
		}
		return a.SendText(req.Peer, req.Text, req.Mode)
	})
	h("DELETE /local/messages", func(w http.ResponseWriter, r *http.Request) (any, error) {
		a.msgs.clear(r.URL.Query().Get("peer"))
		return ok(), nil
	})
	h("POST /local/messages/{id}/retry", func(w http.ResponseWriter, r *http.Request) (any, error) {
		return ok(), a.Retry(r.PathValue("id"))
	})
	h("POST /local/read", func(w http.ResponseWriter, r *http.Request) (any, error) {
		var req struct{ Peer string }
		if err := readJSON(r, &req); err != nil {
			return nil, err
		}
		a.MarkRead(req.Peer)
		return ok(), nil
	})
	h("POST /local/files", a.hUploadFiles)
	h("POST /local/files/path", func(w http.ResponseWriter, r *http.Request) (any, error) {
		var req struct{ Peer, Path, Mode string }
		if err := readJSON(r, &req); err != nil {
			return nil, err
		}
		return a.SendFile(req.Peer, req.Path, "", req.Mode, false)
	})
	h("GET /local/files/{id}/download", func(w http.ResponseWriter, r *http.Request) (any, error) {
		m, found := a.msgs.get(r.PathValue("id"))
		if !found || m.Local == "" {
			return nil, errors.New("檔案不存在")
		}
		f, err := os.Open(m.Local)
		if err != nil {
			return nil, errors.New("檔案已被移動或刪除")
		}
		defer f.Close()
		st, _ := f.Stat()
		name := filepath.Base(m.Local)
		if r.URL.Query().Get("inline") == "1" {
			w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": name}))
		} else {
			w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
		}
		http.ServeContent(w, r, name, st.ModTime(), f)
		return nil, nil
	})
	h("POST /local/files/{id}/open", func(w http.ResponseWriter, r *http.Request) (any, error) {
		if a.opt.Hooks.OpenPath == nil {
			return nil, errors.New("此平台不支援")
		}
		m, found := a.msgs.get(r.PathValue("id"))
		if !found || m.Local == "" {
			return nil, errors.New("檔案不存在")
		}
		p := m.Local
		if r.URL.Query().Get("folder") == "1" {
			p = filepath.Dir(p)
		}
		return ok(), a.opt.Hooks.OpenPath(p)
	})
	h("GET /local/transfers", func(w http.ResponseWriter, r *http.Request) (any, error) {
		return map[string]any{"items": a.xfers.list()}, nil
	})
	h("POST /local/transfers/{id}/cancel", func(w http.ResponseWriter, r *http.Request) (any, error) {
		return ok(), a.CancelTransfer(r.PathValue("id"))
	})
	h("GET /local/settings", func(w http.ResponseWriter, r *http.Request) (any, error) { return a.st.get().Settings, nil })
	h("PATCH /local/settings", a.hPatchSettings)
	h("POST /local/rotate-key", func(w http.ResponseWriter, r *http.Request) (any, error) {
		return a.stateView(), a.RotateKey(r.Context())
	})
	h("GET /local/frpc", func(w http.ResponseWriter, r *http.Request) (any, error) { return a.frpc.status(), nil })
	h("POST /local/frpc/reload", func(w http.ResponseWriter, r *http.Request) (any, error) {
		if err := a.frpc.reload(r.Context()); err != nil {
			return nil, err
		}
		return a.frpc.status(), nil
	})
	h("GET /local/logs", func(w http.ResponseWriter, r *http.Request) (any, error) {
		return map[string]any{"lines": a.logs.tail(300)}, nil
	})
	h("GET /local/events", a.hEvents)
	for _, m := range []string{"GET", "POST", "PATCH", "DELETE"} {
		h(m+" /local/api/{path...}", a.hProxy)
	}

	return a.hostGuard(mux)
}

func ok() map[string]bool { return map[string]bool{"ok": true} }

func (a *Agent) authorized(r *http.Request) bool {
	tok := r.Header.Get("X-Meshport-Token")
	if tok == "" {
		if c, err := r.Cookie("mp_ui"); err == nil {
			tok = c.Value
		}
	}
	return subtle.ConstantTimeCompare([]byte(tok), []byte(a.uiToken)) == 1
}

// hostGuard rejects requests whose Host is not a loopback name, which
// defeats DNS-rebinding pages that try to reach the local API.
func (a *Agent) hostGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if host != "127.0.0.1" && host != "localhost" && host != "::1" && host != "[::1]" {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("無效的 JSON：%w", err)
	}
	return nil
}

func (a *Agent) hUploadFiles(w http.ResponseWriter, r *http.Request) (any, error) {
	peer, mode := r.URL.Query().Get("peer"), r.URL.Query().Get("mode")
	if a.peer(peer) == nil {
		return nil, errors.New("找不到這台裝置")
	}
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, err
	}
	var sent []Message
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if part.FileName() == "" {
			part.Close()
			continue
		}
		dir := filepath.Join(a.opt.DataDir, "outbox", randHex(6))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		name := safeName(part.FileName())
		p := filepath.Join(dir, name)
		f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return nil, err
		}
		_, err = io.Copy(f, part)
		f.Close()
		part.Close()
		if err != nil {
			os.RemoveAll(dir)
			return nil, err
		}
		m, err := a.SendFile(peer, p, name, mode, true)
		if err != nil {
			os.RemoveAll(dir)
			return nil, err
		}
		sent = append(sent, m)
	}
	if len(sent) == 0 {
		return nil, errors.New("沒有收到檔案")
	}
	return map[string]any{"items": sent}, nil
}

func (a *Agent) hPatchSettings(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Mode        *string           `json:"mode"`
		PeerModes   map[string]string `json:"peer_modes"`
		DownloadDir *string           `json:"download_dir"`
		FRPC        *bool             `json:"frpc"`
		Autostart   *bool             `json:"autostart"`
		Notify      *bool             `json:"notify"`
		Theme       *string           `json:"theme"`
		Name        *string           `json:"name"`
	}
	if err := readJSON(r, &req); err != nil {
		return nil, err
	}
	if req.Mode != nil && !validMode(*req.Mode) {
		return nil, errors.New("mode 必須是 auto、p2p 或 relay")
	}
	if req.DownloadDir != nil {
		d := strings.TrimSpace(*req.DownloadDir)
		if !filepath.IsAbs(d) {
			return nil, errors.New("下載資料夾必須是絕對路徑")
		}
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("無法建立下載資料夾：%w", err)
		}
		req.DownloadDir = &d
	}
	if req.Autostart != nil {
		if a.opt.Hooks.SetAutostart == nil {
			return nil, errors.New("此平台不支援開機自動啟動")
		}
		if err := a.opt.Hooks.SetAutostart(*req.Autostart); err != nil {
			return nil, err
		}
	}
	if req.Name != nil {
		n := strings.TrimSpace(*req.Name)
		if n == "" || len(n) > 64 {
			return nil, errors.New("裝置名稱需為 1–64 字元")
		}
		if err := a.api(r.Context(), "PATCH", "/me/device", map[string]string{"name": n}, nil); err != nil {
			return nil, err
		}
	}
	frpcChanged := false
	err := a.st.update(func(s *State) {
		if req.Mode != nil {
			s.Settings.Mode = *req.Mode
		}
		for peer, m := range req.PeerModes {
			if validMode(m) {
				s.Settings.PeerModes[peer] = m
			} else {
				delete(s.Settings.PeerModes, peer)
			}
		}
		if req.DownloadDir != nil {
			s.Settings.DownloadDir = *req.DownloadDir
		}
		if req.FRPC != nil && *req.FRPC != s.Settings.FRPC {
			s.Settings.FRPC = *req.FRPC
			frpcChanged = true
		}
		if req.Autostart != nil {
			s.Settings.Autostart = *req.Autostart
		}
		if req.Notify != nil {
			s.Settings.Notify = *req.Notify
		}
		if req.Theme != nil {
			s.Settings.Theme = *req.Theme
		}
		if req.Name != nil {
			s.Name = strings.TrimSpace(*req.Name)
		}
	})
	if err != nil {
		return nil, err
	}
	if frpcChanged {
		go func() { _ = a.frpc.reload(a.ctx) }()
	}
	a.events.publish("state", a.stateView())
	return a.st.get().Settings, nil
}

// hEvents streams server-sent events to the UI.
func (a *Agent) hEvents(w http.ResponseWriter, r *http.Request) (any, error) {
	fl, okf := w.(http.Flusher)
	if !okf {
		return nil, errors.New("streaming unsupported")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	ch := a.events.subscribe()
	defer a.events.unsubscribe(ch)
	first, _ := json.Marshal(map[string]any{"type": "state", "data": a.stateView()})
	fmt.Fprintf(w, "data: %s\n\n", first)
	fl.Flush()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return nil, nil
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		case b := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		}
	}
}

var proxyAllow = regexp.MustCompile(`^(me/mappings(/[A-Za-z0-9_-]+(/(start|stop))?)?|frp/nodes|me/grants|grants|me/device|files)$`)

// hProxy forwards a small allowlist of device API calls with the device
// token, so the UI can manage mappings and level requests.
func (a *Agent) hProxy(w http.ResponseWriter, r *http.Request) (any, error) {
	p := r.PathValue("path")
	if !proxyAllow.MatchString(p) {
		return nil, &client.APIError{Status: 404, Code: "not_found", Message: "not available from the local API"}
	}
	var body any
	if r.Method == "POST" || r.Method == "PATCH" {
		var raw json.RawMessage
		if err := readJSON(r, &raw); err != nil && !errors.Is(err, io.EOF) {
			if r.ContentLength != 0 {
				return nil, err
			}
		}
		if len(raw) > 0 {
			body = raw
		}
	}
	var out string
	q := ""
	if r.URL.RawQuery != "" {
		q = "?" + r.URL.RawQuery
	}
	if err := a.api(r.Context(), r.Method, "/"+p+q, body, &out); err != nil {
		return nil, err
	}
	if strings.TrimSpace(out) == "" {
		return ok(), nil
	}
	return json.RawMessage(out), nil
}

// LoginURL returns a URL with a one-time code (valid 2 minutes) instead of
// the long-lived token, so the token never appears in process arguments.
func (a *Agent) LoginURL() string {
	code := randHex(16)
	loginCodes.Store(code, time.Now().Add(2*time.Minute))
	return "http://" + a.Addr() + "/auth?c=" + code
}

var loginCodes sync.Map // code -> expiry

func (a *Agent) useLoginCode(c string) bool {
	if c == "" {
		return false
	}
	v, found := loginCodes.LoadAndDelete(c)
	return found && time.Now().Before(v.(time.Time))
}
