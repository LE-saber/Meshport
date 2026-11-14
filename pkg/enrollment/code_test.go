package enrollment

import (
	"encoding/base64"
	"testing"
)

func TestInvitationsAreDistinctAndValidate(t *testing.T) {
	i := Invitation{Version: 1, Server: "https://mesh.example", Token: "mpe_unique_one", Pin: "sha256/" + base64.StdEncoding.EncodeToString(make([]byte, 32))}
	a, err := Encode(i)
	if err != nil {
		t.Fatal(err)
	}
	i.Token = "mpe_unique_two"
	b, err := Encode(i)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("different devices received the same invitation")
	}
	for _, text := range []string{a, "meshport://join#" + a} {
		got, err := Parse(text)
		if err != nil || got.Token != "mpe_unique_one" {
			t.Fatalf("roundtrip: %+v %v", got, err)
		}
	}
	for _, server := range []string{"http://mesh.example", "https://name:secret@mesh.example", "https://mesh.example/?token=bad", "https://mesh.example/admin"} {
		i.Server = server
		if _, err := Encode(i); err == nil {
			t.Fatalf("unsafe origin accepted: %s", server)
		}
	}
	if _, err := Parse(Prefix + "invalid"); err == nil {
		t.Fatal("invalid encoded invitation accepted")
	}
}
