package agent

import (
	"errors"
	"net/http"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
	"meshport/pkg/client"
)

// This password protects interactive disclosure. It is separate from device
// authentication, so background connections never prompt for it.
type keyAccess struct {
	sync.Mutex
	failures     int
	blockedUntil time.Time
}

func (a *Agent) hSetKeyPassword(w http.ResponseWriter, r *http.Request) (any, error) {
	w.Header().Set("Cache-Control", "no-store")
	var req struct {
		Password     string `json:"password"`
		Confirmation string `json:"confirmation"`
	}
	if err := readJSON(r, &req); err != nil {
		return nil, err
	}
	a.keyAccess.Lock()
	defer a.keyAccess.Unlock()
	if a.st.get().KeyViewPasswordHash != "" {
		return nil, errors.New("密鑰密碼已設定，請輸入原密碼查看")
	}
	if utf8.RuneCountInString(req.Password) < 8 || len(req.Password) > 72 {
		return nil, errors.New("密鑰密碼至少 8 個字元，且不超過 72 位元組")
	}
	if req.Password != req.Confirmation {
		return nil, errors.New("兩次輸入的密鑰密碼不一致")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	a.st.mu.Lock()
	a.st.s.KeyViewPasswordHash = string(hash)
	err = a.st.saveLocked()
	if err != nil {
		a.st.s.KeyViewPasswordHash = ""
	}
	a.st.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return map[string]bool{"password_set": true}, nil
}

func (a *Agent) hViewKeys(w http.ResponseWriter, r *http.Request) (any, error) {
	w.Header().Set("Cache-Control", "no-store")
	var req struct {
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil {
		return nil, err
	}
	a.keyAccess.Lock()
	defer a.keyAccess.Unlock()
	if time.Now().Before(a.keyAccess.blockedUntil) {
		return nil, &client.APIError{Status: 429, Code: "too_many_attempts", Message: "請稍後再試"}
	}
	s := a.st.get()
	if s.KeyViewPasswordHash == "" {
		return nil, errors.New("請先設定自己的密鑰密碼")
	}
	if len(req.Password) > 72 || bcrypt.CompareHashAndPassword([]byte(s.KeyViewPasswordHash), []byte(req.Password)) != nil {
		a.keyAccess.failures++
		if a.keyAccess.failures >= 5 {
			a.keyAccess.failures = 0
			a.keyAccess.blockedUntil = time.Now().Add(30 * time.Second)
		}
		return nil, errors.New("密鑰密碼不正確")
	}
	a.keyAccess.failures = 0
	return map[string]string{
		"identity_public_key":  client.PubKey(s.identity()),
		"identity_fingerprint": s.fingerprint(),
		"e2e_public_key":       s.boxPub(),
		"server_fingerprint":   s.ServerPin,
	}, nil
}
