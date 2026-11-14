// Package agent is the Meshport client core shared by the desktop app
// (Windows/Linux) and the Android app (through gomobile).
//
// It owns the device identity, the server connection (REST + WebSocket),
// end-to-end encrypted messaging over WebRTC data channels or the server
// relay, file transfers, the embedded frpc, and a local HTTP API with a
// web UI that both shells display.
package agent

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"meshport/pkg/client"
	"meshport/pkg/proto"
)

// Hooks are optional platform integrations supplied by the shell.
type Hooks struct {
	// Notify shows a system notification (new message, finished transfer).
	Notify func(title, body string)
	// SetAutostart enables or disables start-on-login. Nil = unsupported.
	SetAutostart func(on bool) error
	// OpenPath opens a received file or its folder. Nil = unsupported.
	OpenPath func(path string) error
	// OpenUI brings the UI window to the front (tray, second instance).
	OpenUI func()
}

type Options struct {
	DataDir     string // identity, settings, message history, logs
	Listen      string // local API address, default 127.0.0.1:0
	Version     string
	OS          string // reported to the server, e.g. "Windows 11", "Android 15"
	Kind        string // desktop | laptop | phone | server
	DownloadDir string // default for received files
	Platform    string // windows | linux | android
	LogStderr   bool
	Hooks       Hooks
}

// Agent is one running client instance.
type Agent struct {
	opt  Options
	log  *slog.Logger
	logs *logRing

	st    *stateStore
	msgs  *messageStore
	xfers *transferStore

	events *eventHub
	links  *linkManager
	frpc   *frpcManager

	mu       sync.Mutex
	token    string
	conn     *client.Conn
	connErr  string
	status   string // setup | pending | connecting | online | offline | disabled
	level    string
	peers    map[string]*Peer
	iceCache *iceServers
	wake     chan struct{} // nudges the connection loop

	uiToken  string
	listener net.Listener
	httpSrv  *http.Server
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

// New loads (or creates) the client state in opt.DataDir.
func New(opt Options) (*Agent, error) {
	if opt.DataDir == "" {
		return nil, errors.New("agent: DataDir is required")
	}
	if opt.Listen == "" {
		opt.Listen = "127.0.0.1:0"
	}
	if opt.Kind == "" {
		opt.Kind = "desktop"
	}
	if opt.Version == "" {
		opt.Version = "dev"
	}
	for _, d := range []string{opt.DataDir, filepath.Join(opt.DataDir, "messages"), filepath.Join(opt.DataDir, "tmp"), filepath.Join(opt.DataDir, "frpc")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	logs, err := newLogRing(filepath.Join(opt.DataDir, "meshport.log"), opt.LogStderr)
	if err != nil {
		return nil, err
	}
	a := &Agent{
		opt:    opt,
		logs:   logs,
		log:    slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo})),
		events: newEventHub(),
		peers:  map[string]*Peer{},
		wake:   make(chan struct{}, 1),
	}
	if a.st, err = loadState(filepath.Join(opt.DataDir, "state.json")); err != nil {
		return nil, err
	}
	if a.st.get().Settings.DownloadDir == "" {
		dl := opt.DownloadDir
		if dl == "" {
			dl = filepath.Join(opt.DataDir, "downloads")
		}
		_ = a.st.update(func(s *State) { s.Settings.DownloadDir = dl })
	}
	if a.msgs, err = openMessageStore(filepath.Join(opt.DataDir, "messages")); err != nil {
		return nil, err
	}
	a.xfers = newTransferStore()
	a.links = newLinkManager(a)
	a.frpc = newFRPCManager(a)
	if a.uiToken, err = loadOrCreateToken(filepath.Join(opt.DataDir, "ui-token")); err != nil {
		return nil, err
	}
	s := a.st.get()
	a.level = s.Level
	switch {
	case s.DeviceID == "":
		a.status = "setup"
	case s.Status == "pending":
		a.status = "pending"
	default:
		a.status = "offline"
	}
	return a, nil
}

// Start opens the local API and begins connecting to the server.
func (a *Agent) Start() error {
	a.ctx, a.cancel = context.WithCancel(context.Background())
	ln, err := net.Listen("tcp", a.opt.Listen)
	if err != nil {
		return err
	}
	a.listener = ln
	a.httpSrv = &http.Server{Handler: a.localHandler(), ReadHeaderTimeout: 10 * time.Second}
	a.wg.Add(3)
	go func() { defer a.wg.Done(); _ = a.httpSrv.Serve(ln) }()
	go func() { defer a.wg.Done(); a.connLoop(a.ctx) }()
	go func() { defer a.wg.Done(); a.housekeeping(a.ctx) }()
	a.log.Info("agent started", "version", a.opt.Version, "ui", a.URL())
	return nil
}

// Stop shuts everything down.
func (a *Agent) Stop() {
	if a.cancel == nil {
		return
	}
	a.cancel()
	a.links.closeAll("client stopping")
	a.frpc.stopAll()
	a.mu.Lock()
	if a.conn != nil {
		a.conn.Close()
	}
	a.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = a.httpSrv.Shutdown(ctx)
	a.wg.Wait()
	a.logs.Close()
}

// Addr is the local API address (host:port).
func (a *Agent) Addr() string { return a.listener.Addr().String() }

// URL opens the UI and authenticates the webview/browser with the UI token.
func (a *Agent) URL() string { return "http://" + a.Addr() + "/auth?t=" + a.uiToken }

// UIToken is the secret that authorizes local API callers.
func (a *Agent) UIToken() string { return a.uiToken }

// Status is a one-word connection state for tray icons and notifications.
func (a *Agent) Status() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status
}

func (a *Agent) setStatus(status, errMsg string) {
	a.mu.Lock()
	changed := a.status != status || a.connErr != errMsg
	a.status, a.connErr = status, errMsg
	a.mu.Unlock()
	if changed {
		a.events.publish("state", a.stateView())
	}
}

func (a *Agent) nudge() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

func (a *Agent) notify(title, body string) {
	if a.opt.Hooks.Notify != nil && a.st.get().Settings.Notify {
		go a.opt.Hooks.Notify(title, body)
	}
}

// housekeeping runs periodic tasks: session reports, idle link cleanup,
// retrying p2p-only messages, transfer pruning.
func (a *Agent) housekeeping(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	n := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		n++
		a.links.tick()
		if n%6 == 0 { // every 30 s
			a.retryPending()
			a.xfers.prune()
		}
	}
}

// ---------- state view for the UI ----------

type StateView struct {
	Status   string    `json:"status"`
	Error    string    `json:"error,omitempty"`
	Server   string    `json:"server"`
	Device   DeviceRef `json:"device"`
	Settings Settings  `json:"settings"`
	Version  string    `json:"version"`
	Platform string    `json:"platform"`
	Caps     []string  `json:"caps"`
	Unread   int       `json:"unread"`
}

type DeviceRef struct {
	ID          string `json:"id"`
	DID         string `json:"did"`
	Name        string `json:"name"`
	Level       string `json:"level"`
	Fingerprint string `json:"fingerprint"`
	BoxPubKey   string `json:"box_pubkey"`
}

func (a *Agent) stateView() StateView {
	s := a.st.get()
	a.mu.Lock()
	v := StateView{Status: a.status, Error: a.connErr}
	level := a.level
	a.mu.Unlock()
	v.Server = s.Server
	v.Device = DeviceRef{ID: s.DeviceID, DID: s.DID, Name: s.Name, Level: level, Fingerprint: s.fingerprint(), BoxPubKey: s.boxPub()}
	v.Settings = s.Settings
	v.Version = a.opt.Version
	v.Platform = a.opt.Platform
	v.Unread = a.msgs.unreadTotal()
	if a.opt.Hooks.SetAutostart != nil {
		v.Caps = append(v.Caps, "autostart")
	}
	if a.opt.Hooks.OpenPath != nil {
		v.Caps = append(v.Caps, "open")
	}
	return v
}

// ---------- server helpers ----------

// api performs a REST call with the device token, logging in again once if
// the token expired.
func (a *Agent) api(ctx context.Context, method, path string, body, out any) error {
	err := a.apiOnce(ctx, method, path, body, out)
	var ae *client.APIError
	if errors.As(err, &ae) && ae.Status == http.StatusUnauthorized {
		if lerr := a.login(ctx); lerr != nil {
			return lerr
		}
		err = a.apiOnce(ctx, method, path, body, out)
	}
	return err
}

func (a *Agent) apiOnce(ctx context.Context, method, path string, body, out any) error {
	cl := a.newClient()
	return cl.Do(ctx, method, "/api/v1"+path, body, out)
}

// newClient returns an SDK client bound to the saved identity and token.
func (a *Agent) newClient() *client.Client {
	s := a.st.get()
	cl := client.NewWithPin(s.Server, s.ServerPin)
	cl.DeviceID = s.DeviceID
	cl.Key = s.identity()
	a.mu.Lock()
	cl.Token = a.token
	a.mu.Unlock()
	return cl
}

func (a *Agent) currentConn() *client.Conn {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.conn
}

// sendFrame writes a frame on the device socket if connected.
func (a *Agent) sendFrame(t string, payload any) error {
	c := a.currentConn()
	if c == nil {
		return errOffline
	}
	return c.Send(t, payload)
}

func (a *Agent) request(ctx context.Context, t string, payload, out any) error {
	c := a.currentConn()
	if c == nil {
		return errOffline
	}
	return c.Request(ctx, t, payload, out)
}

func (a *Agent) myID() string { return a.st.get().DeviceID }

var errOffline = errors.New("not connected to the server")

// reportSession sends a session.report (best effort).
func (a *Agent) reportSession(r proto.SessionReport) {
	_ = a.sendFrame(proto.TSessionReport, r)
}
