// Package meshcore exposes the Meshport client core to Android through
// gomobile (gomobile bind -target=android ./mobile/meshcore).
//
// The Kotlin app runs the core inside a foreground service and shows the
// core's local web UI in a WebView:
//
//	val url = Meshcore.start(filesDir.path, downloadDir, "Android 15", "1.2.0", platform)
//	webView.loadUrl(url)
package meshcore

import (
	"errors"
	"sync"

	"meshport/internal/agent"
)

// Platform is implemented by the Android app.
type Platform interface {
	// Show posts a notification (new message, received file).
	Show(title, body string)
	// SetAutostart stores whether the service starts after boot.
	SetAutostart(on bool)
	// OpenFile opens a received file with another app.
	OpenFile(path string)
}

var (
	mu sync.Mutex
	ag *agent.Agent
)

// Start runs the client core and returns the UI URL (including the local
// access token). Calling Start again returns the running instance's URL.
func Start(dataDir, downloadDir, osVersion, appVersion string, p Platform) (string, error) {
	mu.Lock()
	defer mu.Unlock()
	if ag != nil {
		return ag.URL(), nil
	}
	if dataDir == "" {
		return "", errors.New("dataDir is required")
	}
	hooks := agent.Hooks{}
	if p != nil {
		hooks.Notify = p.Show
		hooks.SetAutostart = func(on bool) error { p.SetAutostart(on); return nil }
		hooks.OpenPath = func(path string) error { p.OpenFile(path); return nil }
	}
	a, err := agent.New(agent.Options{
		DataDir: dataDir, Listen: "127.0.0.1:0", Version: appVersion, OS: osVersion, Kind: "phone",
		DownloadDir: downloadDir, Platform: "android", Hooks: hooks,
	})
	if err != nil {
		return "", err
	}
	if err := a.Start(); err != nil {
		return "", err
	}
	ag = a
	return a.URL(), nil
}

// Stop shuts the core down (closes P2P links, stops frpc).
func Stop() {
	mu.Lock()
	a := ag
	ag = nil
	mu.Unlock()
	if a != nil {
		a.Stop()
	}
}

// URL returns the UI URL of the running core, or "" if stopped.
func URL() string {
	mu.Lock()
	defer mu.Unlock()
	if ag == nil {
		return ""
	}
	return ag.URL()
}

// Status returns setup | pending | connecting | online | offline | disabled,
// or "stopped".
func Status() string {
	mu.Lock()
	defer mu.Unlock()
	if ag == nil {
		return "stopped"
	}
	return ag.Status()
}
