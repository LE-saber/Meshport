package policy

import (
	"testing"

	"meshport/internal/store"
)

func dev(id, level, group, user string) *store.Device {
	return &store.Device{ID: id, Level: level, GroupID: group, UserID: user, Status: "active"}
}

func TestVisibilityScopes(t *testing.T) {
	a, b, c := dev("a", LevelGuest, "home", "u1"), dev("b", LevelGuest, "home", "u2"), dev("c", LevelGuest, "office", "u1")
	adm := dev("x", LevelAdmin, "office", "u3")
	if !CanSee(a, c, "all") || CanSee(a, a, "all") {
		t.Fatal("all scope")
	}
	if !CanSee(a, b, "group") || CanSee(a, c, "group") || !CanSee(a, adm, "group") {
		t.Fatal("group scope")
	}
	if !CanSee(a, c, "user") || CanSee(a, b, "user") {
		t.Fatal("user scope")
	}
	b.Status = "disabled"
	if CanSee(a, b, "all") {
		t.Fatal("disabled device visible")
	}
}

func TestGrantRules(t *testing.T) {
	adm, mem, g := dev("a", LevelAdmin, "", ""), dev("m", LevelMember, "", ""), dev("g", LevelGuest, "", "")
	if ok, _ := CanRequestGrant(adm, g, LevelMember); !ok {
		t.Fatal("admin should request member")
	}
	if ok, _ := CanRequestGrant(mem, g, LevelGuest); ok {
		t.Fatal("member must not request")
	}
	if ok, _ := CanRequestGrant(adm, adm, LevelAdmin); ok {
		t.Fatal("self request")
	}
	if ok, _ := CanRequestGrant(adm, g, "root"); ok {
		t.Fatal("unknown level")
	}
	if CanCreateMapping(g) || !CanCreateMapping(mem) || CanManageFRP(mem) || !CanManageFRP(adm) {
		t.Fatal("capabilities")
	}
	if !RoleAtLeast(RoleAdmin, RoleOperator) || RoleAtLeast(RoleViewer, RoleOperator) {
		t.Fatal("roles")
	}
}
