package api

import (
	"net"
	"strings"
	"sync"
	"time"

	"meshport/internal/store"
)

// ACL caches allow/deny CIDR rules and temporary brute-force blocks.
//
//   - deny rules block every request (API, WebSocket, frpc logins, visitors of published services)
//   - allow rules, when present, restrict console/API-token access to those networks;
//     devices are not affected so phones on mobile networks keep working
type ACL struct {
	db    *store.DB
	mu    sync.RWMutex
	deny  []*net.IPNet
	allow []*net.IPNet
	temp  map[string]time.Time
}

func newACL(db *store.DB) *ACL { return &ACL{db: db, temp: map[string]time.Time{}} }

func ParseCIDR(s string) (*net.IPNet, error) {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "/") {
		if strings.Contains(s, ":") {
			s += "/128"
		} else {
			s += "/32"
		}
	}
	_, n, err := net.ParseCIDR(s)
	return n, err
}

func (a *ACL) Reload() error {
	rows, err := a.db.Query(`SELECT action, cidr FROM acl_rules`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var deny, allow []*net.IPNet
	for rows.Next() {
		var action, cidr string
		if err := rows.Scan(&action, &cidr); err != nil {
			return err
		}
		n, err := ParseCIDR(cidr)
		if err != nil {
			continue
		}
		if action == "deny" {
			deny = append(deny, n)
		} else {
			allow = append(allow, n)
		}
	}
	a.mu.Lock()
	a.deny, a.allow = deny, allow
	a.mu.Unlock()
	return rows.Err()
}

func contains(list []*net.IPNet, ip net.IP) bool {
	for _, n := range list {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// Denied reports whether ip is on the deny list.
func (a *ACL) Denied(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return contains(a.deny, ip)
}

// AllowedConsole applies the allow list to console users and API tokens.
func (a *ACL) AllowedConsole(ipStr string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if len(a.allow) == 0 {
		return true
	}
	ip := net.ParseIP(ipStr)
	return ip != nil && (ip.IsLoopback() || contains(a.allow, ip))
}

// Block temporarily refuses logins from an address.
func (a *ACL) Block(ip string, d time.Duration) {
	a.mu.Lock()
	a.temp[ip] = time.Now().Add(d)
	a.mu.Unlock()
}

func (a *ACL) Blocked(ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	until, ok := a.temp[ip]
	if !ok {
		return false
	}
	if time.Now().After(until) {
		delete(a.temp, ip)
		return false
	}
	return true
}

// TempBlocks lists active temporary blocks.
func (a *ACL) TempBlocks() map[string]time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := map[string]time.Time{}
	now := time.Now()
	for ip, t := range a.temp {
		if t.After(now) {
			out[ip] = t
		}
	}
	return out
}

func (a *ACL) Unblock(ip string) {
	a.mu.Lock()
	delete(a.temp, ip)
	a.mu.Unlock()
}

func parseIP(s string) net.IP { return net.ParseIP(s) }

func isLoopback(s string) bool {
	ip := net.ParseIP(s)
	return ip != nil && ip.IsLoopback()
}
