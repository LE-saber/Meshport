// Command meshctl is a small device client for testing a Meshport
// deployment: register, log in, list peers, relay messages, watch the
// WebSocket and fetch frpc configuration.
//
// NOTE: messages sent by meshctl are base64 plaintext. Real clients encrypt
// end to end with the peers' box_pubkey before sending.
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"meshport/pkg/client"
	"meshport/pkg/proto"
)

type state struct {
	Server   string `json:"server"`
	DeviceID string `json:"device_id"`
	Key      string `json:"key"` // base64 ed25519 private key
}

func statePath() string {
	if p := os.Getenv("MESHCTL_STATE"); p != "" {
		return p
	}
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "meshctl", "state.json")
}

func load() (*client.Client, error) {
	b, err := os.ReadFile(statePath())
	if err != nil {
		return nil, fmt.Errorf("not registered (run meshctl register): %w", err)
	}
	var st state
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	k, err := base64.StdEncoding.DecodeString(st.Key)
	if err != nil {
		return nil, err
	}
	c := client.New(st.Server)
	c.DeviceID, c.Key = st.DeviceID, ed25519.PrivateKey(k)
	return c, nil
}

func save(c *client.Client) error {
	b, _ := json.MarshalIndent(state{Server: c.BaseURL, DeviceID: c.DeviceID, Key: base64.StdEncoding.EncodeToString(c.Key)}, "", "  ")
	if err := os.MkdirAll(filepath.Dir(statePath()), 0o700); err != nil {
		return err
	}
	return os.WriteFile(statePath(), b, 0o600)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, `usage: meshctl register|peers|send|listen|frpc|api ...
  register -server URL -name NAME (-enroll TOKEN | -user U -password P) [-kind laptop]
  peers
  send <device-id> <text> [-mode relay|auto]
  listen                      print every WebSocket frame (answers ping commands)
  frpc [-node NAME]           print generated frpc.toml
  api METHOD PATH [JSON]      call any device endpoint
state file: $MESHCTL_STATE or ~/.config/meshctl/state.json`)
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := run(ctx, os.Args[1], os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func loggedIn(ctx context.Context) (*client.Client, error) {
	c, err := load()
	if err != nil {
		return nil, err
	}
	return c, c.Login(ctx)
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func run(ctx context.Context, cmd string, args []string) error {
	switch cmd {
	case "register":
		fs := flag.NewFlagSet("register", flag.ExitOnError)
		server := fs.String("server", "", "server URL")
		name := fs.String("name", "", "device name")
		enroll := fs.String("enroll", "", "enrollment token")
		user := fs.String("user", "", "username (self-registration)")
		pw := fs.String("password", "", "password")
		kind := fs.String("kind", "laptop", "device kind")
		_ = fs.Parse(args)
		host, _ := os.Hostname()
		if *name == "" {
			*name = host
		}
		c := client.New(*server)
		d, err := c.Register(ctx, client.RegisterRequest{Name: *name, OS: "meshctl", Kind: *kind, EnrollToken: *enroll, Username: *user, Password: *pw, ClientVersion: "meshctl"})
		if err != nil {
			return err
		}
		if err := save(c); err != nil {
			return err
		}
		fmt.Printf("registered %s (%s) status=%s level=%s\n", d.Name, d.ID, d.Status, d.Level)
		return nil
	case "peers":
		c, err := loggedIn(ctx)
		if err != nil {
			return err
		}
		var out struct {
			Items []map[string]any `json:"items"`
		}
		if err := c.Do(ctx, "GET", "/api/v1/peers", nil, &out); err != nil {
			return err
		}
		for _, p := range out.Items {
			on := "offline"
			if p["online"] == true {
				on = "online"
			}
			fmt.Printf("%-22s %-24s %-7s %s\n", p["id"], p["name"], p["level"], on)
		}
		return nil
	case "send":
		fs := flag.NewFlagSet("send", flag.ExitOnError)
		mode := fs.String("mode", "relay", "relay | auto")
		_ = fs.Parse(args)
		if fs.NArg() < 2 {
			return fmt.Errorf("send <device-id> <text>")
		}
		c, err := loggedIn(ctx)
		if err != nil {
			return err
		}
		cn, err := c.Connect(ctx)
		if err != nil {
			return err
		}
		defer cn.Close()
		ack, err := cn.SendMessage(ctx, fs.Arg(0), *mode, []byte(strings.Join(fs.Args()[1:], " ")))
		if err != nil {
			return err
		}
		fmt.Printf("message %s %s\n", ack.ID, ack.Status)
		return nil
	case "listen":
		c, err := loggedIn(ctx)
		if err != nil {
			return err
		}
		cn, err := c.Connect(ctx)
		if err != nil {
			return err
		}
		defer cn.Close()
		fmt.Printf("connected as %s (level %s, %d queued)\n", cn.Hello.DID, cn.Hello.Level, cn.Hello.Queued)
		for {
			select {
			case <-ctx.Done():
				return nil
			case f, ok := <-cn.Frames():
				if !ok {
					return fmt.Errorf("connection closed")
				}
				fmt.Printf("%s %s %s\n", time.Now().Format("15:04:05"), f.T, string(f.D))
				switch f.T {
				case proto.TMsgNew:
					var m proto.Message
					_ = json.Unmarshal(f.D, &m)
					if b, err := base64.StdEncoding.DecodeString(m.Body); err == nil {
						fmt.Printf("   from %s: %s\n", m.From, b)
					}
					_ = cn.Send(proto.TMsgDelivered, proto.Receipt{IDs: []string{m.ID}})
				case proto.TCmd:
					var cmd proto.Cmd
					_ = json.Unmarshal(f.D, &cmd)
					_ = cn.Send(proto.TCmdResult, proto.CmdResult{ID: cmd.ID, OK: cmd.Name == "ping", Output: "meshctl handled " + cmd.Name})
				}
			}
		}
	case "frpc":
		fs := flag.NewFlagSet("frpc", flag.ExitOnError)
		node := fs.String("node", "", "node name or id")
		_ = fs.Parse(args)
		c, err := loggedIn(ctx)
		if err != nil {
			return err
		}
		if *node != "" {
			var s string
			if err := c.Do(ctx, "GET", "/api/v1/me/frpc/"+*node, nil, &s); err != nil {
				return err
			}
			fmt.Print(s)
			return nil
		}
		var out struct {
			Items []struct {
				Node string `json:"node"`
				TOML string `json:"toml"`
			} `json:"items"`
		}
		if err := c.Do(ctx, "GET", "/api/v1/me/frpc", nil, &out); err != nil {
			return err
		}
		for _, it := range out.Items {
			fmt.Printf("# ===== node %s =====\n%s\n", it.Node, it.TOML)
		}
		return nil
	case "api":
		if len(args) < 2 {
			return fmt.Errorf("api METHOD PATH [JSON]")
		}
		c, err := loggedIn(ctx)
		if err != nil {
			return err
		}
		var body any
		if len(args) > 2 {
			if err := json.Unmarshal([]byte(args[2]), &body); err != nil {
				return fmt.Errorf("body: %w", err)
			}
		}
		var out any
		if err := c.Do(ctx, strings.ToUpper(args[0]), args[1], body, &out); err != nil {
			return err
		}
		printJSON(out)
		return nil
	}
	return fmt.Errorf("unknown command %q", cmd)
}
