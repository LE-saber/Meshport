package frp

import (
	"strings"
	"testing"

	"meshport/internal/store"
)

func TestParsePorts(t *testing.T) {
	r, err := ParsePorts("6000-7000, 9987")
	if err != nil || len(r) != 2 || !portAllowed(r, 6500) || !portAllowed(r, 9987) || portAllowed(r, 8000) {
		t.Fatalf("ranges %v %v", r, err)
	}
	for _, bad := range []string{"0-10", "70000", "9-1", "a-b"} {
		if _, err := ParsePorts(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
	if !portAllowed(nil, 1) {
		t.Fatal("empty allow list should allow everything")
	}
}

func TestTOMLQuoting(t *testing.T) {
	if got := tq("a\"b\\c\n\x01"); got != `"a\"b\\c\n\u0001"` {
		t.Fatalf("tq = %s", got)
	}
}

func TestServerConfig(t *testing.T) {
	n := &store.Node{ID: "frps_x", Name: "tpe", BindPort: 7000, VhostHTTPPort: 80, AllowPorts: "6000-7000,9987", DashboardAddr: "127.0.0.1:7500",
		DashboardUser: "u", DashboardPass: "p\"q", PluginToken: "tok"}
	conf, err := ServerConfig("secret", n, "127.0.0.1:7401")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"bindPort = 7000", `auth.token = "secret"`, `{ start = 6000, end = 7000 }`, `{ single = 9987 }`,
		`webServer.password = "p\"q"`, `path = "/frps-plugin/frps_x/tok"`, `"NewProxy"`} {
		if !strings.Contains(conf, want) {
			t.Fatalf("missing %q in\n%s", want, conf)
		}
	}
}
