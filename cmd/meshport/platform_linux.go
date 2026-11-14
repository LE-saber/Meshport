//go:build linux && !android

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

func osName() string {
	f, err := os.Open("/etc/os-release")
	if err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if v, ok := strings.CutPrefix(sc.Text(), "PRETTY_NAME="); ok {
				return strings.Trim(v, `"`)
			}
		}
	}
	return "Linux"
}

func deviceKind() string {
	if m, _ := filepath.Glob("/sys/class/power_supply/BAT*"); len(m) > 0 {
		return "laptop"
	}
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return "server"
	}
	return "desktop"
}

func isTerminal() bool {
	fi, err := os.Stderr.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func logFatal(error) {}

func startDetached(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// openWindow prefers a Chromium-based browser in --app mode (a plain window
// without tabs) with its own profile, else the default browser.
func openWindow(url, profile string) error {
	for _, b := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "microsoft-edge", "microsoft-edge-stable", "brave-browser"} {
		if p, err := exec.LookPath(b); err == nil {
			return startDetached(p, "--app="+url, "--user-data-dir="+profile, "--no-first-run", "--no-default-browser-check", "--window-size=1100,760", "--class=Meshport")
		}
	}
	if p, err := exec.LookPath("xdg-open"); err == nil {
		return startDetached(p, url)
	}
	return fmt.Errorf("no browser found; open %s manually", url)
}

func openPath(p string) error { return startDetached("xdg-open", p) }

var (
	notifyMu   sync.Mutex
	notifyLast time.Time
)

func notify(title, body string) {
	notifyMu.Lock()
	if time.Since(notifyLast) < 1500*time.Millisecond {
		notifyMu.Unlock()
		return
	}
	notifyLast = time.Now()
	notifyMu.Unlock()
	if p, err := exec.LookPath("notify-send"); err == nil {
		_ = exec.Command(p, "--app-name=Meshport", "--", title, body).Run()
	}
}

func autostartFile() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "autostart", "meshport.desktop")
}

func setAutostart(on bool) error {
	f := autostartFile()
	if f == "" {
		return fmt.Errorf("no config directory")
	}
	if !on {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		return err
	}
	desktop := fmt.Sprintf("[Desktop Entry]\nType=Application\nName=Meshport\nComment=Meshport private mesh client\nExec=\"%s\" --minimized\nIcon=meshport\nTerminal=false\nX-GNOME-Autostart-enabled=true\n", exe)
	return os.WriteFile(f, []byte(desktop), 0o644)
}
