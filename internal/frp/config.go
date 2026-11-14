// Package frp integrates fatedier/frp: meshportd acts as the frps server
// plugin (authorising every frpc login and proxy against the database),
// generates frpc/frps configuration, polls frps dashboards for statistics
// and supervises locally managed frps processes.
package frp

import (
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"meshport/internal/store"
)

var validProto = map[string]bool{"tcp": true, "udp": true, "http": true, "https": true, "stcp": true, "xtcp": true}

var nameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,47}$`)
var domainRe = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)
var subdomainRe = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// PortRange is one element of a node's allow_ports list.
type PortRange struct{ Start, End int }

func ParsePorts(s string) ([]PortRange, error) {
	var out []PortRange
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lo, hi, isRange := strings.Cut(part, "-")
		a, err := strconv.Atoi(strings.TrimSpace(lo))
		if err != nil {
			return nil, fmt.Errorf("bad port %q", part)
		}
		b := a
		if isRange {
			if b, err = strconv.Atoi(strings.TrimSpace(hi)); err != nil {
				return nil, fmt.Errorf("bad port range %q", part)
			}
		}
		if a < 1 || b > 65535 || a > b {
			return nil, fmt.Errorf("bad port range %q", part)
		}
		out = append(out, PortRange{a, b})
	}
	return out, nil
}

func portAllowed(ranges []PortRange, p int) bool {
	if len(ranges) == 0 {
		return true
	}
	for _, r := range ranges {
		if p >= r.Start && p <= r.End {
			return true
		}
	}
	return false
}

// ValidateMapping checks a mapping against its node and existing mappings.
func ValidateMapping(db *store.DB, m *store.Mapping, node *store.Node) error {
	if !nameRe.MatchString(m.Name) {
		return fmt.Errorf("name must be 1-48 characters: letters, digits, '-' or '_'")
	}
	if !validProto[m.Proto] {
		return fmt.Errorf("proto must be one of tcp, udp, http, https, stcp, xtcp")
	}
	if m.LocalIP == "" {
		m.LocalIP = "127.0.0.1"
	}
	if net.ParseIP(m.LocalIP) == nil && !domainRe.MatchString(m.LocalIP) && m.LocalIP != "localhost" {
		return fmt.Errorf("local_ip must be an IP address or hostname")
	}
	if m.LocalPort < 1 || m.LocalPort > 65535 {
		return fmt.Errorf("local_port must be 1-65535")
	}
	if m.BandwidthLimit != "" && !regexp.MustCompile(`^\d+(KB|MB)$`).MatchString(m.BandwidthLimit) {
		return fmt.Errorf("bandwidth_limit must look like 512KB or 10MB")
	}
	switch m.Proto {
	case "tcp", "udp":
		m.Domains.V, m.Subdomain, m.SecretKey = []string{}, "", ""
		if m.RemotePort < 1 || m.RemotePort > 65535 {
			return fmt.Errorf("remote_port must be 1-65535 for %s", m.Proto)
		}
		ranges, err := ParsePorts(node.AllowPorts)
		if err != nil {
			return fmt.Errorf("node allow_ports: %w", err)
		}
		if !portAllowed(ranges, m.RemotePort) {
			return fmt.Errorf("remote_port %d is outside the ports allowed on %s (%s)", m.RemotePort, node.Name, node.AllowPorts)
		}
		if m.RemotePort == node.BindPort || m.RemotePort == node.VhostHTTPPort || m.RemotePort == node.VhostHTTPSPort {
			return fmt.Errorf("remote_port %d is used by frps itself", m.RemotePort)
		}
		family := "tcp"
		if m.Proto == "udp" {
			family = "udp"
		}
		n := db.Count(`SELECT COUNT(*) FROM mappings WHERE node_id=? AND remote_port=? AND id!=? AND (CASE WHEN proto='udp' THEN 'udp' ELSE 'tcp' END)=? AND proto IN ('tcp','udp')`, node.ID, m.RemotePort, m.ID, family)
		if n > 0 {
			return fmt.Errorf("remote_port %d/%s is already used on %s", m.RemotePort, family, node.Name)
		}
	case "http", "https":
		m.RemotePort, m.SecretKey = 0, ""
		if m.Proto == "http" && node.VhostHTTPPort == 0 {
			return fmt.Errorf("node %s has no vhost HTTP port", node.Name)
		}
		if m.Proto == "https" && node.VhostHTTPSPort == 0 {
			return fmt.Errorf("node %s has no vhost HTTPS port", node.Name)
		}
		clean := []string{}
		for _, d := range m.Domains.V {
			d = strings.ToLower(strings.TrimSpace(d))
			if d == "" {
				continue
			}
			if !domainRe.MatchString(d) {
				return fmt.Errorf("invalid domain %q", d)
			}
			clean = append(clean, d)
		}
		sort.Strings(clean)
		m.Domains.V = clean
		m.Subdomain = strings.ToLower(strings.TrimSpace(m.Subdomain))
		if m.Subdomain != "" {
			if node.SubdomainHost == "" {
				return fmt.Errorf("node %s has no subdomain_host; use a custom domain", node.Name)
			}
			if !subdomainRe.MatchString(m.Subdomain) {
				return fmt.Errorf("invalid subdomain %q", m.Subdomain)
			}
		}
		if len(clean) == 0 && m.Subdomain == "" {
			return fmt.Errorf("http/https mappings need a domain or a subdomain")
		}
		others, err := db.Mappings(`m.node_id=? AND m.id!=? AND m.proto=?`, node.ID, m.ID, m.Proto)
		if err != nil {
			return err
		}
		for _, o := range others {
			for _, d := range o.Domains.V {
				for _, c := range clean {
					if c == d {
						return fmt.Errorf("domain %s is already used by mapping %s", d, o.Name)
					}
				}
			}
			if m.Subdomain != "" && o.Subdomain == m.Subdomain {
				return fmt.Errorf("subdomain %s is already used by mapping %s", m.Subdomain, o.Name)
			}
		}
	case "stcp", "xtcp":
		m.RemotePort, m.Domains.V, m.Subdomain = 0, []string{}, ""
		if len(m.SecretKey) < 8 {
			return fmt.Errorf("secret_key of at least 8 characters is required for %s", m.Proto)
		}
	}
	n := db.Count(`SELECT COUNT(*) FROM mappings WHERE device_id=? AND name=? AND id!=?`, m.DeviceID, m.Name, m.ID)
	if n > 0 {
		return fmt.Errorf("this device already has a mapping named %s", m.Name)
	}
	return nil
}

// ---------- TOML generation ----------

// tq quotes a TOML basic string.
func tq(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

func tlist(items []string) string {
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = tq(s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

// ClientConfig renders the frpc.toml a device runs for one frps node.
func ClientConfig(authToken string, d *store.Device, node *store.Node, maps []*store.Mapping) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Generated by meshportd for %s (%s) on node %s. Do not edit; fetch again after changes.\n", d.Name, d.DID, node.Name)
	fmt.Fprintf(&b, "serverAddr = %s\nserverPort = %d\nuser = %s\nloginFailExit = false\n\n", tq(node.PublicAddr), node.BindPort, tq(d.ID))
	fmt.Fprintf(&b, "auth.method = \"token\"\nauth.token = %s\n\n", tq(authToken))
	fmt.Fprintf(&b, "metadatas.device_id = %s\nmetadatas.frp_secret = %s\n\n", tq(d.ID), tq(d.FRPSecret))
	b.WriteString("transport.tls.enable = true\ntransport.heartbeatInterval = 10\ntransport.heartbeatTimeout = 40\n")
	for _, m := range maps {
		if !m.Enabled || m.NodeID != node.ID {
			continue
		}
		fmt.Fprintf(&b, "\n[[proxies]]\nname = %s\ntype = %s\nlocalIP = %s\nlocalPort = %d\n", tq(m.Name), tq(m.Proto), tq(m.LocalIP), m.LocalPort)
		switch m.Proto {
		case "tcp", "udp":
			fmt.Fprintf(&b, "remotePort = %d\n", m.RemotePort)
		case "http", "https":
			if len(m.Domains.V) > 0 {
				fmt.Fprintf(&b, "customDomains = %s\n", tlist(m.Domains.V))
			}
			if m.Subdomain != "" {
				fmt.Fprintf(&b, "subdomain = %s\n", tq(m.Subdomain))
			}
		case "stcp", "xtcp":
			fmt.Fprintf(&b, "secretKey = %s\nallowUsers = [\"*\"]\n", tq(m.SecretKey))
		}
		fmt.Fprintf(&b, "transport.useEncryption = %t\ntransport.useCompression = %t\n", m.UseEncryption, m.UseCompression)
		if m.BandwidthLimit != "" {
			fmt.Fprintf(&b, "transport.bandwidthLimit = %s\n", tq(m.BandwidthLimit))
		}
	}
	return b.String()
}

// ServerConfig renders frps.toml for a node meshportd manages.
func ServerConfig(authToken string, n *store.Node, pluginAddr string) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "# Generated by meshportd for node %s. Changes are overwritten on restart.\n", n.Name)
	fmt.Fprintf(&b, "bindPort = %d\n", n.BindPort)
	if n.VhostHTTPPort > 0 {
		fmt.Fprintf(&b, "vhostHTTPPort = %d\n", n.VhostHTTPPort)
	}
	if n.VhostHTTPSPort > 0 {
		fmt.Fprintf(&b, "vhostHTTPSPort = %d\n", n.VhostHTTPSPort)
	}
	if n.SubdomainHost != "" {
		fmt.Fprintf(&b, "subDomainHost = %s\n", tq(n.SubdomainHost))
	}
	ranges, err := ParsePorts(n.AllowPorts)
	if err != nil {
		return "", err
	}
	if len(ranges) > 0 {
		parts := []string{}
		for _, r := range ranges {
			if r.Start == r.End {
				parts = append(parts, fmt.Sprintf("{ single = %d }", r.Start))
			} else {
				parts = append(parts, fmt.Sprintf("{ start = %d, end = %d }", r.Start, r.End))
			}
		}
		fmt.Fprintf(&b, "allowPorts = [%s]\n", strings.Join(parts, ", "))
	}
	fmt.Fprintf(&b, "\nauth.method = \"token\"\nauth.token = %s\n", tq(authToken))
	host, port, err := net.SplitHostPort(n.DashboardAddr)
	if err != nil {
		return "", fmt.Errorf("dashboard_addr: %w", err)
	}
	fmt.Fprintf(&b, "\nwebServer.addr = %s\nwebServer.port = %s\nwebServer.user = %s\nwebServer.password = %s\n", tq(host), port, tq(n.DashboardUser), tq(n.DashboardPass))
	b.WriteString("\nlog.to = \"console\"\nlog.level = \"info\"\nlog.disablePrintColor = true\n")
	fmt.Fprintf(&b, "\n[[httpPlugins]]\nname = \"meshport\"\naddr = %s\npath = %s\nops = [\"Login\", \"NewProxy\", \"CloseProxy\", \"Ping\", \"NewUserConn\"]\n",
		tq(pluginAddr), tq(PluginPath(n)))
	return b.String(), nil
}

// PluginPath is the HTTP path frps calls on meshportd for a node.
func PluginPath(n *store.Node) string { return "/frps-plugin/" + n.ID + "/" + n.PluginToken }
