//go:build (linux && !android) || windows

// Command meshport is the Meshport desktop client for Windows and Linux.
//
//	meshport                 start (tray icon + app window)
//	meshport --minimized     start in the tray only (used for autostart)
//	meshport --headless      no window, no tray (servers, systemd)
//	meshport setup --server URL --enroll mpe_… [--name NAME]
//	meshport status          print the connection state
//	meshport open            open the window of the running instance
//	meshport version
//
// The UI is the agent's local web app, shown in an app-mode browser window
// (Edge/Chrome/Chromium) or the default browser. No CGO is required.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"meshport/internal/agent"
)

var version = "dev"

const defaultPort = 47100

func defaultDataDir() string {
	if d := os.Getenv("MESHPORT_DATA"); d != "" {
		return d
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ".meshport"
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(dir, "Meshport")
	}
	return filepath.Join(dir, "meshport")
}

func defaultDownloadDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Downloads", "Meshport")
}

func main() {
	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "", "run":
		err = run(args)
	case "setup":
		err = setup(args)
	case "status":
		err = status(args)
	case "open":
		err = openRunning(args)
	case "version":
		fmt.Println("meshport", version, runtime.GOOS+"/"+runtime.GOARCH)
	case "help", "-h", "--help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fatal(err)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `Meshport desktop client

usage:
  meshport [--minimized] [--headless] [--no-tray] [--data DIR] [--listen ADDR]
  meshport setup --server URL (--enroll CODE | --user NAME --password PW [--totp CODE]) [--name NAME]
  meshport status
  meshport open
  meshport version
`)
}

type common struct {
	data   string
	listen string
}

func (c *common) bind(fs *flag.FlagSet) {
	fs.StringVar(&c.data, "data", defaultDataDir(), "data directory (identity, history, logs)")
	fs.StringVar(&c.listen, "listen", fmt.Sprintf("127.0.0.1:%d", defaultPort), "local UI/API address (loopback only)")
}

func newAgent(c common, logStderr bool, hooks agent.Hooks) (*agent.Agent, error) {
	host, _, err := net.SplitHostPort(c.listen)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, errors.New("--listen must be a loopback address")
	}
	return agent.New(agent.Options{
		DataDir: c.data, Listen: c.listen, Version: version, OS: osName(), Kind: deviceKind(),
		DownloadDir: defaultDownloadDir(), Platform: runtime.GOOS, LogStderr: logStderr, Hooks: hooks,
	})
}

// ---------- run ----------

func run(args []string) error {
	fs := flag.NewFlagSet("meshport", flag.ExitOnError)
	var c common
	c.bind(fs)
	minimized := fs.Bool("minimized", false, "start without opening the window")
	headless := fs.Bool("headless", false, "no window and no tray icon")
	noTray := fs.Bool("no-tray", false, "do not show a tray icon")
	_ = fs.Parse(args)

	// single instance: if our agent already listens there, just show it
	if running(c.listen) {
		if *headless {
			return errors.New("another meshport instance is already running on " + c.listen)
		}
		return showRunning(c)
	}
	if !portFree(c.listen) {
		c.listen = "127.0.0.1:0"
	}

	var a *agent.Agent
	hooks := agent.Hooks{
		Notify:       notify,
		SetAutostart: setAutostart,
		OpenPath:     openPath,
	}
	hooks.OpenUI = func() {
		if a != nil {
			if err := openWindow(a.LoginURL(), filepath.Join(c.data, "webview")); err != nil {
				fmt.Fprintln(os.Stderr, "open window:", err)
			}
		}
	}
	if *headless {
		hooks = agent.Hooks{OpenPath: nil}
	}
	var err error
	a, err = newAgent(c, *headless || isTerminal(), hooks)
	if err != nil {
		return err
	}
	if err := a.Start(); err != nil {
		return err
	}
	// keep the autostart entry in sync with the saved preference
	if !*headless {
		_ = setAutostartIfWanted(a)
	}

	if *headless {
		fmt.Printf("meshport %s running (headless), local UI: http://%s (token in %s)\n", version, a.Addr(), filepath.Join(c.data, "ui-token"))
		waitSignal()
		a.Stop()
		return nil
	}
	if !*minimized {
		hooks.OpenUI()
	}
	if *noTray {
		waitSignal()
		a.Stop()
		return nil
	}
	runTray(a, hooks.OpenUI) // blocks until Quit
	a.Stop()
	return nil
}

func setAutostartIfWanted(a *agent.Agent) error {
	st, err := localState(a.Addr(), a.UIToken())
	if err != nil {
		return err
	}
	var v struct {
		Settings struct {
			Autostart bool `json:"autostart"`
		} `json:"settings"`
	}
	if json.Unmarshal(st, &v) == nil && v.Settings.Autostart {
		return setAutostart(true)
	}
	return nil
}

func waitSignal() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	<-ch
}

func portFree(addr string) bool {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

var localHTTP = &http.Client{Timeout: 3 * time.Second}

func running(addr string) bool {
	resp, err := localHTTP.Get("http://" + addr + "/local/ping")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var v struct{ App string }
	return json.NewDecoder(resp.Body).Decode(&v) == nil && v.App == "meshport"
}

func readToken(data string) (string, error) {
	b, err := os.ReadFile(filepath.Join(data, "ui-token"))
	if err != nil {
		return "", errors.New("cannot read the UI token; is meshport set up for this user?")
	}
	return strings.TrimSpace(string(b)), nil
}

func localCall(addr, token, method, path string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequest(method, "http://"+addr+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Meshport-Token", token)
	req.Header.Set("X-Requested-With", "meshport-cli")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := localHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	return data, nil
}

func localState(addr, token string) ([]byte, error) {
	return localCall(addr, token, "GET", "/local/state", nil)
}

func showRunning(c common) error {
	tok, err := readToken(c.data)
	if err != nil {
		return err
	}
	_, err = localCall(c.listen, tok, "POST", "/local/show", nil)
	return err
}

func openRunning(args []string) error {
	fs := flag.NewFlagSet("open", flag.ExitOnError)
	var c common
	c.bind(fs)
	_ = fs.Parse(args)
	if !running(c.listen) {
		return errors.New("meshport is not running")
	}
	return showRunning(c)
}

// ---------- setup / status (CLI) ----------

func setup(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ExitOnError)
	var c common
	c.bind(fs)
	server := fs.String("server", "", "server URL, e.g. https://mesh.example.com")
	enroll := fs.String("enroll", "", "enrollment code (mpe_…)")
	user := fs.String("user", "", "username (self-registration)")
	pass := fs.String("password", "", "password (self-registration)")
	totp := fs.String("totp", "", "MFA code")
	host, _ := os.Hostname()
	name := fs.String("name", host, "device name")
	_ = fs.Parse(args)
	body := agent.SetupRequest{Server: *server, Name: *name, EnrollToken: *enroll, Username: *user, Password: *pass, TOTP: *totp}
	if running(c.listen) { // let the running instance register
		tok, err := readToken(c.data)
		if err != nil {
			return err
		}
		b, _ := json.Marshal(body)
		out, err := localCall(c.listen, tok, "POST", "/local/setup", strings.NewReader(string(b)))
		if err != nil {
			return err
		}
		return printState(out)
	}
	a, err := newAgent(c, false, agent.Hooks{})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := a.Setup(ctx, body)
	if err != nil {
		return err
	}
	out, _ := json.Marshal(st)
	return printState(out)
}

func status(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	var c common
	c.bind(fs)
	_ = fs.Parse(args)
	if !running(c.listen) {
		b, err := os.ReadFile(filepath.Join(c.data, "state.json"))
		if err != nil {
			fmt.Println("not set up and not running")
			return nil
		}
		var s struct {
			Server, DeviceID, Name, Level, Status string
		}
		_ = json.Unmarshal(b, &s)
		fmt.Printf("not running\nserver:  %s\ndevice:  %s (%s)\nlevel:   %s\nstatus:  %s\n", s.Server, s.Name, s.DeviceID, s.Level, s.Status)
		return nil
	}
	tok, err := readToken(c.data)
	if err != nil {
		return err
	}
	out, err := localState(c.listen, tok)
	if err != nil {
		return err
	}
	return printState(out)
}

func printState(raw []byte) error {
	var v agent.StateView
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	fmt.Printf("status:      %s %s\nserver:      %s\ndevice:      %s (%s)\nlevel:       %s\nfingerprint: %s\n",
		v.Status, v.Error, v.Server, v.Device.Name, v.Device.ID, v.Device.Level, v.Device.Fingerprint)
	if v.Status == "pending" {
		fmt.Println("waiting for an administrator to approve this device")
	}
	return nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "meshport:", err)
	logFatal(err)
	os.Exit(1)
}
