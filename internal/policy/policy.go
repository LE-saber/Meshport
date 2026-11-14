// Package policy centralises every permission decision so the REST API, the
// WebSocket hub and the frps plugin all apply identical rules.
package policy

import "meshport/internal/store"

// Device levels (what a device may do on the mesh).
const (
	LevelAdmin  = "admin"  // messaging, tunnels, manage frps, request levels for other devices
	LevelMember = "member" // messaging and own frpc tunnels
	LevelGuest  = "guest"  // messages and files only
)

// Panel roles (what a human may do in the management console / API).
const (
	RoleAdmin    = "admin"    // everything, including users, API keys and system settings
	RoleOperator = "operator" // day-to-day operations: devices, comms, FRP, approvals, security
	RoleViewer   = "viewer"   // read-only console access
	RoleUser     = "user"     // owns devices, no console access
)

var levelRank = map[string]int{LevelGuest: 1, LevelMember: 2, LevelAdmin: 3}
var roleRank = map[string]int{RoleUser: 0, RoleViewer: 1, RoleOperator: 2, RoleAdmin: 3}

func ValidLevel(l string) bool { return levelRank[l] > 0 }
func ValidRole(r string) bool  { _, ok := roleRank[r]; return ok }
func LevelRank(l string) int   { return levelRank[l] }
func RoleRank(r string) int    { return roleRank[r] }

// RoleAtLeast reports whether role has at least the privileges of min.
func RoleAtLeast(role, min string) bool { return roleRank[role] >= roleRank[min] }

func Active(d *store.Device) bool { return d != nil && d.Status == "active" }

// CanSee decides discovery visibility between two devices.
func CanSee(a, b *store.Device, scope string) bool {
	if !Active(a) || !Active(b) || a.ID == b.ID {
		return false
	}
	if a.Level == LevelAdmin || b.Level == LevelAdmin {
		return true
	}
	switch scope {
	case "group":
		return a.GroupID != "" && a.GroupID == b.GroupID
	case "user":
		return a.UserID != "" && a.UserID == b.UserID
	default:
		return true
	}
}

// CanMessage: every level may exchange messages and files with visible peers.
func CanMessage(a, b *store.Device, scope string) bool { return CanSee(a, b, scope) }

// CanCreateMapping: members and admins may publish their own services.
func CanCreateMapping(d *store.Device) bool {
	return Active(d) && levelRank[d.Level] >= levelRank[LevelMember]
}

// CanManageFRP: admin-level devices may manage frps nodes and stop any proxy.
func CanManageFRP(d *store.Device) bool { return Active(d) && d.Level == LevelAdmin }

// CanRequestGrant: admin-level devices may request levels for other devices,
// never above their own level and never for themselves.
func CanRequestGrant(req, target *store.Device, level string) (bool, string) {
	switch {
	case !Active(req):
		return false, "requesting device is not active"
	case req.Level != LevelAdmin:
		return false, "only admin-level devices may request levels for other devices"
	case target == nil:
		return false, "target device not found"
	case req.ID == target.ID:
		return false, "a device cannot request a level for itself"
	case target.Status == "banned":
		return false, "target device is banned"
	case !ValidLevel(level):
		return false, "unknown level"
	case levelRank[level] > levelRank[req.Level]:
		return false, "cannot request a level above your own"
	}
	return true, ""
}

// LevelDef documents levels for the console's level table.
type LevelDef struct {
	Level        string   `json:"level"`
	Capabilities []string `json:"capabilities"`
}

func LevelDefs() []LevelDef {
	return []LevelDef{
		{LevelAdmin, []string{"message", "file", "frpc", "frps.manage", "grant.request", "discover.all"}},
		{LevelMember, []string{"message", "file", "frpc"}},
		{LevelGuest, []string{"message", "file"}},
	}
}
