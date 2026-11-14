package api

import (
	"bytes"
	"encoding/base64"
	"image/png"
	"net/http/httptest"
	"strings"
	"testing"

	"meshport/pkg/client"
	"meshport/pkg/enrollment"
)

func TestSingleDevicePinnedInvitation(t *testing.T) {
	e := newEnv(t)
	tlsServer := httptest.NewTLSServer(e.srv.Handler())
	defer tlsServer.Close()
	e.srv.cfg.EnrollmentURL = tlsServer.URL
	e.srv.enrollmentPin = enrollment.Pin(tlsServer.Certificate())
	type invitation struct {
		Token, JoinCode, QR string
		ID                  string
	}
	create := func() invitation {
		var r struct {
			Token string `json:"token"`
			Code  string `json:"join_code"`
			QR    string `json:"qr_png"`
			ID    string `json:"id"`
		}
		e.must(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/enroll-tokens", map[string]any{"level": "member", "uses": 1, "auto_approve": true}, &r))
		return invitation{r.Token, r.Code, r.QR, r.ID}
	}
	a, b := create(), create()
	if a.Token == b.Token || a.JoinCode == b.JoinCode {
		t.Fatal("codes must differ per device")
	}
	payload, err := enrollment.Parse(a.JoinCode)
	e.must(err)
	if payload.Server != tlsServer.URL || payload.Pin != e.srv.enrollmentPin {
		t.Fatal("missing server identity")
	}
	img, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(a.QR, "data:image/png;base64,"))
	e.must(err)
	if _, err := png.Decode(bytes.NewReader(img)); err != nil {
		t.Fatal(err)
	}
	e.expectErr(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/enroll-tokens", map[string]any{"uses": 2}, nil), 400, "bad_request")
	plain := client.New(e.ts.URL)
	_, err = plain.Register(e.ctx(), client.RegisterRequest{Name: "plaintext", EnrollToken: a.Token})
	e.expectErr(err, 403, "forbidden") // must not consume the code
	secure := client.NewWithPin(payload.Server, payload.Pin)
	_, err = secure.Register(e.ctx(), client.RegisterRequest{Name: "encrypted device", EnrollToken: payload.Token})
	e.must(err)
	e.must(secure.Login(e.ctx()))
	cn, err := secure.Connect(e.ctx())
	e.must(err)
	cn.Close()
	other := client.NewWithPin(payload.Server, payload.Pin)
	_, err = other.Register(e.ctx(), client.RegisterRequest{Name: "replay", EnrollToken: payload.Token})
	e.expectErr(err, 401, "invalid_enroll_token")
}

func TestSecureRequestRejectsUntrustedForwardedProto(t *testing.T) {
	e := newEnv(t)
	r := httptest.NewRequest("POST", "http://server/api/v1/devices/register", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	r.RemoteAddr = "198.51.100.4:5000"
	if e.srv.secureRequest(r) {
		t.Fatal("untrusted peer may not claim TLS")
	}
	r.RemoteAddr = "127.0.0.1:5000"
	if !e.srv.secureRequest(r) {
		t.Fatal("trusted TLS gateway not recognized")
	}
	r.Header.Set("X-Forwarded-Proto", "http")
	if e.srv.secureRequest(r) {
		t.Fatal("HTTP gateway must not claim TLS")
	}
}

func TestGrantDurationChoices(t *testing.T) {
	e := newEnv(t)
	device := e.device("duration target", "guest")
	for _, duration := range []string{"1h", "6h", "24h", "7d", "30d", "perm"} {
		e.must(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/grants", map[string]string{"target": device.DeviceID, "level": "member", "duration": duration}, nil))
	}
	e.expectErr(e.admin.Do(e.ctx(), "POST", "/api/v1/admin/grants", map[string]string{"target": device.DeviceID, "level": "member", "duration": "90d"}, nil), 400, "bad_request")
}
