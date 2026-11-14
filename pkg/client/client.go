// Package client is the Go SDK for Meshport devices. Desktop clients (and
// gomobile builds for Android) use it to register, log in, discover peers,
// relay messages, negotiate P2P sessions and fetch frpc configuration.
package client

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"meshport/pkg/proto"
)

type Client struct {
	BaseURL  string
	HTTP     *http.Client
	Token    string
	DeviceID string
	Key      ed25519.PrivateKey
}

// APIError is a decoded {"error":{...}} response.
type APIError struct {
	Status  int
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string { return fmt.Sprintf("%d %s: %s", e.Status, e.Code, e.Message) }

func New(baseURL string) *Client {
	return &Client{BaseURL: strings.TrimSuffix(baseURL, "/"), HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// NewIdentity creates a fresh ed25519 identity key.
func NewIdentity() ed25519.PrivateKey {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	return priv
}

func PubKey(k ed25519.PrivateKey) string {
	return base64.StdEncoding.EncodeToString(k.Public().(ed25519.PublicKey))
}

func sign(k ed25519.PrivateKey, msg []byte) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(k, msg))
}

// Do performs a JSON request. out may be nil.
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode >= 300 {
		var e struct {
			Error *APIError `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != nil {
			e.Error.Status = resp.StatusCode
			return e.Error
		}
		return &APIError{Status: resp.StatusCode, Code: "http", Message: string(data)}
	}
	if out != nil {
		if s, ok := out.(*string); ok {
			*s = string(data)
			return nil
		}
		return json.Unmarshal(data, out)
	}
	return nil
}

type RegisterRequest struct {
	Name          string `json:"name"`
	OS            string `json:"os,omitempty"`
	Kind          string `json:"kind,omitempty"`
	BoxPubKey     string `json:"box_pubkey,omitempty"`
	EnrollToken   string `json:"enroll_token,omitempty"`
	Username      string `json:"username,omitempty"`
	Password      string `json:"password,omitempty"`
	TOTP          string `json:"totp,omitempty"`
	LanIP         string `json:"lan_ip,omitempty"`
	ClientVersion string `json:"client_version,omitempty"`
}

type Device struct {
	ID     string `json:"id"`
	DID    string `json:"did"`
	Name   string `json:"name"`
	Level  string `json:"level"`
	Status string `json:"status"`
}

// Register enrolls this client's key; the returned status is active or pending.
func (c *Client) Register(ctx context.Context, r RegisterRequest) (*Device, error) {
	if c.Key == nil {
		c.Key = NewIdentity()
	}
	ts := time.Now().Unix()
	pub := PubKey(c.Key)
	body := map[string]any{"pubkey": pub, "ts": ts, "sig": sign(c.Key, proto.RegisterMessage(pub, ts))}
	b, _ := json.Marshal(r)
	_ = json.Unmarshal(b, &body)
	var out struct {
		Device Device `json:"device"`
	}
	if err := c.Do(ctx, "POST", "/api/v1/devices/register", body, &out); err != nil {
		return nil, err
	}
	c.DeviceID = out.Device.ID
	return &out.Device, nil
}

// Login proves possession of the identity key and stores the device token.
func (c *Client) Login(ctx context.Context) error {
	var ch struct {
		Nonce string `json:"nonce"`
	}
	if err := c.Do(ctx, "POST", "/api/v1/devices/auth/challenge", map[string]string{"device_id": c.DeviceID}, &ch); err != nil {
		return err
	}
	var out struct {
		Token string `json:"token"`
	}
	sig := sign(c.Key, proto.DeviceLoginMessage(c.DeviceID, ch.Nonce))
	if err := c.Do(ctx, "POST", "/api/v1/devices/auth/login", map[string]string{"device_id": c.DeviceID, "nonce": ch.Nonce, "signature": sig}, &out); err != nil {
		return err
	}
	c.Token = out.Token
	return nil
}

// RotateKey replaces the identity key; both keys sign the rotation.
func (c *Client) RotateKey(ctx context.Context) error {
	next := NewIdentity()
	np := PubKey(next)
	msg := proto.RotateMessage(c.DeviceID, np)
	err := c.Do(ctx, "POST", "/api/v1/me/device/rotate-key", map[string]string{"new_pubkey": np, "old_sig": sign(c.Key, msg), "new_sig": sign(next, msg)}, nil)
	if err == nil {
		c.Key = next
	}
	return err
}

// ---------- WebSocket ----------

type Conn struct {
	ws      *websocket.Conn
	frames  chan proto.Frame
	seq     atomic.Int64
	mu      sync.Mutex
	waiting map[string]chan proto.Frame
	ctx     context.Context
	cancel  context.CancelFunc
	// Hello is the server's first frame.
	Hello proto.Hello
}

// Connect opens the device WebSocket and waits for the hello frame.
func (c *Client) Connect(ctx context.Context) (*Conn, error) {
	u := "ws" + strings.TrimPrefix(c.BaseURL, "http") + "/api/v1/ws"
	ws, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + c.Token}}})
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(1 << 20)
	cctx, cancel := context.WithCancel(context.Background())
	cn := &Conn{ws: ws, frames: make(chan proto.Frame, 256), waiting: map[string]chan proto.Frame{}, ctx: cctx, cancel: cancel}
	go cn.read()
	select {
	case f := <-cn.frames:
		if f.T != proto.THello {
			cn.Close()
			return nil, fmt.Errorf("expected hello, got %s", f.T)
		}
		_ = json.Unmarshal(f.D, &cn.Hello)
	case <-time.After(5 * time.Second):
		cn.Close()
		return nil, errors.New("no hello from server")
	}
	return cn, nil
}

func (cn *Conn) read() {
	defer close(cn.frames)
	for {
		_, data, err := cn.ws.Read(cn.ctx)
		if err != nil {
			return
		}
		var f proto.Frame
		if json.Unmarshal(data, &f) != nil {
			continue
		}
		if f.ID != "" && (f.T == proto.TAck || f.T == proto.TError || f.T == proto.TPong) {
			cn.mu.Lock()
			ch := cn.waiting[f.ID]
			delete(cn.waiting, f.ID)
			cn.mu.Unlock()
			if ch != nil {
				ch <- f
				continue
			}
		}
		select {
		case cn.frames <- f:
		default: // drop when the application is not reading
		}
	}
}

// Frames delivers server-initiated frames (msg.new, signal, presence, cmd, ...).
func (cn *Conn) Frames() <-chan proto.Frame { return cn.frames }

// Send writes a frame without waiting for a reply.
func (cn *Conn) Send(t string, payload any) error {
	b, err := proto.Encode(t, "", payload)
	if err != nil {
		return err
	}
	return cn.ws.Write(cn.ctx, websocket.MessageText, b)
}

// Request writes a frame with an id and waits for its ack/error.
func (cn *Conn) Request(ctx context.Context, t string, payload any, out any) error {
	id := fmt.Sprintf("r%d", cn.seq.Add(1))
	ch := make(chan proto.Frame, 1)
	cn.mu.Lock()
	cn.waiting[id] = ch
	cn.mu.Unlock()
	b, err := proto.Encode(t, id, payload)
	if err != nil {
		return err
	}
	if err := cn.ws.Write(ctx, websocket.MessageText, b); err != nil {
		return err
	}
	select {
	case f := <-ch:
		if f.T == proto.TError {
			var e proto.Error
			_ = json.Unmarshal(f.D, &e)
			return &APIError{Code: e.Code, Message: e.Message}
		}
		if out != nil && len(f.D) > 0 {
			return json.Unmarshal(f.D, out)
		}
		return nil
	case <-ctx.Done():
		cn.mu.Lock()
		delete(cn.waiting, id)
		cn.mu.Unlock()
		return ctx.Err()
	}
}

// SendMessage relays an (already end-to-end encrypted) payload.
func (cn *Conn) SendMessage(ctx context.Context, to, mode string, ciphertext []byte) (*proto.MsgAck, error) {
	var ack proto.MsgAck
	err := cn.Request(ctx, proto.TMsgSend, proto.MsgSend{To: to, Mode: mode, ClientID: fmt.Sprintf("c%d", time.Now().UnixNano()), ContentType: "text",
		Body: base64.StdEncoding.EncodeToString(ciphertext)}, &ack)
	return &ack, err
}

// Wait returns the next frame of the given type.
func (cn *Conn) Wait(ctx context.Context, t string) (proto.Frame, error) {
	for {
		select {
		case f, ok := <-cn.frames:
			if !ok {
				return proto.Frame{}, errors.New("connection closed")
			}
			if f.T == t {
				return f, nil
			}
		case <-ctx.Done():
			return proto.Frame{}, ctx.Err()
		}
	}
}

func (cn *Conn) Close() {
	cn.cancel()
	_ = cn.ws.Close(websocket.StatusNormalClosure, "bye")
}
