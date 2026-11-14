package api

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"meshport/internal/store"
)

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func echoOnce(addr string) (string, error) {
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return "", err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Write([]byte("ping\n")); err != nil {
		return "", err
	}
	line, err := bufio.NewReader(c).ReadString('\n')
	return strings.TrimSpace(line), err
}

func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", d, what)
}

// TestFRPEndToEnd runs real frps (supervised by meshportd) and frpc binaries.
// Set MESHPORT_FRPS and MESHPORT_FRPC to the binaries to enable it.
func TestFRPEndToEnd(t *testing.T) {
	frps, frpc := os.Getenv("MESHPORT_FRPS"), os.Getenv("MESHPORT_FRPC")
	if frps == "" || frpc == "" {
		t.Skip("set MESHPORT_FRPS and MESHPORT_FRPC to run the frp end-to-end test")
	}
	e := newEnv(t)
	e.srv.cfg.FRP.Binary = frps

	// local services to publish
	echo, _ := net.Listen("tcp", "127.0.0.1:0")
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				line, _ := bufio.NewReader(c).ReadString('\n')
				fmt.Fprintf(c, "echo:%s", line)
			}(c)
		}
	}()
	web, _ := net.Listen("tcp", "127.0.0.1:0")
	defer web.Close()
	go http.Serve(web, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "hello from "+r.Host) }))

	bind, dash, vhost, remote, rogue := freePort(t), freePort(t), freePort(t), freePort(t), freePort(t)
	var nodeResp struct{ Node nodeView }
	e.must(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/frp/nodes", map[string]any{
		"name": "local", "public_addr": "127.0.0.1", "bind_port": bind, "vhost_http_port": vhost,
		"allow_ports": fmt.Sprintf("%d,%d", remote, rogue), "dashboard_addr": fmt.Sprintf("127.0.0.1:%d", dash), "managed": true}, &nodeResp))
	waitFor(t, "frps to listen", 10*time.Second, func() bool {
		c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", bind))
		if err == nil {
			c.Close()
		}
		return err == nil
	})

	dev := e.device("e2e-box", "member")
	var tcpMap, httpMap store.Mapping
	e.must(dev.Do(e.ctx(), "POST", "/api/v1/me/mappings", map[string]any{"name": "echo", "node": "local", "proto": "tcp",
		"local_port": echo.Addr().(*net.TCPAddr).Port, "remote_port": remote}, &tcpMap))
	e.must(dev.Do(e.ctx(), "POST", "/api/v1/me/mappings", map[string]any{"name": "site", "node": "local", "proto": "http",
		"local_port": web.Addr().(*net.TCPAddr).Port, "domains": []string{"e2e.test"}}, &httpMap))

	var toml string
	e.must(dev.Do(e.ctx(), "GET", "/api/v1/me/frpc/"+nodeResp.Node.ID, nil, &toml))
	// a misbehaving client adds a proxy the server never registered
	toml += fmt.Sprintf("\n[[proxies]]\nname = \"rogue\"\ntype = \"tcp\"\nlocalIP = \"127.0.0.1\"\nlocalPort = %d\nremotePort = %d\n", echo.Addr().(*net.TCPAddr).Port, rogue)
	cfgPath := filepath.Join(e.dir, "frpc.toml")
	e.must(os.WriteFile(cfgPath, []byte(toml), 0o600))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, frpc, "-c", cfgPath)
	logf, _ := os.Create(filepath.Join(e.dir, "frpc.log"))
	cmd.Stdout, cmd.Stderr = logf, logf
	e.must(cmd.Start())
	defer func() {
		if t.Failed() {
			b, _ := os.ReadFile(filepath.Join(e.dir, "frpc.log"))
			t.Logf("frpc log:\n%s", b)
		}
	}()

	remoteAddr := fmt.Sprintf("127.0.0.1:%d", remote)
	waitFor(t, "tcp mapping to forward", 20*time.Second, func() bool {
		s, err := echoOnce(remoteAddr)
		return err == nil && s == "echo:ping"
	})
	m, _ := e.srv.db.MappingByID(tcpMap.ID)
	if m.Status != "online" {
		t.Fatalf("mapping status %s", m.Status)
	}
	// http vhost routing through frps
	waitFor(t, "http mapping", 10*time.Second, func() bool {
		req, _ := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/", vhost), nil)
		req.Host = "e2e.test"
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b) == "hello from e2e.test"
	})
	// the unregistered proxy was refused by the plugin
	if _, err := echoOnce(fmt.Sprintf("127.0.0.1:%d", rogue)); err == nil {
		t.Fatal("rogue proxy is reachable")
	}
	if e.srv.db.Count(`SELECT COUNT(*) FROM failures WHERE kind='frp' AND reason LIKE 'unknown proxy rogue%'`) == 0 {
		t.Fatal("rogue proxy rejection not recorded")
	}
	// dashboard polling: node online with version, traffic counted
	e.srv.frp.PollNow()
	n, _ := e.srv.db.NodeByID("local")
	if n.Status != "online" || n.Version != "0.72.0" {
		t.Fatalf("node %s %s %s", n.Status, n.Version, n.LastError)
	}
	m, _ = e.srv.db.MappingByID(tcpMap.ID)
	if m.TrafficIn == 0 && m.TrafficOut == 0 {
		t.Fatalf("no traffic recorded: %+v", m)
	}

	// stop from the console: frps must drop the proxy even though this frpc
	// never reloads its config (enforced via ping rejection + NewProxy check)
	e.must(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/frp/mappings/"+tcpMap.ID+"/stop", nil, nil))
	waitFor(t, "stopped mapping to go away", 40*time.Second, func() bool {
		_, err := echoOnce(remoteAddr)
		return err != nil
	})
	// other mappings come back after the forced reconnect
	waitFor(t, "http mapping after reconnect", 20*time.Second, func() bool {
		req, _ := http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%d/", vhost), nil)
		req.Host = "e2e.test"
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == 200
	})
	// start again -> forwarding resumes after frpc's next registration
	e.must(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/frp/mappings/"+tcpMap.ID+"/start", nil, nil))
	e.srv.frp.Kick(n.ID, dev.DeviceID)
	waitFor(t, "restarted mapping", 40*time.Second, func() bool {
		s, err := echoOnce(remoteAddr)
		return err == nil && s == "echo:ping"
	})
	// supervised process restart from the console
	e.must(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/system/services/frps:local/restart", nil, nil))
	waitFor(t, "frps back after restart", 40*time.Second, func() bool {
		s, err := echoOnce(remoteAddr)
		return err == nil && s == "echo:ping"
	})
}
