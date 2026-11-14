// Package proto defines the WebSocket frames exchanged between meshportd and
// devices (desktop/mobile clients). It is shared by the server and the Go SDK.
//
// Every frame is a JSON object {"t": <type>, "id": <optional request id>, "d": <payload>}.
// Requests that carry an "id" are answered with a frame of type "ack" or
// "error" carrying the same id.
package proto

import "encoding/json"

type Frame struct {
	T  string          `json:"t"`
	ID string          `json:"id,omitempty"`
	D  json.RawMessage `json:"d,omitempty"`
}

// Frame types sent by a device.
const (
	TMsgSend       = "msg.send"       // MsgSend           -> ack MsgAck
	TMsgDelivered  = "msg.delivered"  // Receipt
	TMsgRead       = "msg.read"       // Receipt
	TSignal        = "signal"         // Signal (forwarded verbatim to the peer)
	TSessionReport = "session.report" // SessionReport
	TCmdResult     = "cmd.result"     // CmdResult
	TPresenceSet   = "presence.set"   // PresenceSet (LAN IP / NAT type hints)
	TPing          = "ping"
)

// Frame types sent by the server.
const (
	THello        = "hello"         // Hello (first frame after connect)
	TAck          = "ack"           // reply to a request id
	TError        = "error"         // Error reply to a request id
	TMsgNew       = "msg.new"       // Message
	TMsgReceipt   = "msg.receipt"   // Receipt (delivered/read by the peer)
	TPresence     = "presence"      // Presence
	TCmd          = "cmd"           // Cmd (remote management)
	TFRPConfig    = "frp.config"    // FRPConfigChanged
	TLevelChanged = "level.changed" // LevelChanged
	TGrantUpdate  = "grant.update"  // GrantUpdate
	TSessionClose = "session.close" // SessionClose (admin disconnected a session)
	TKick         = "kick"          // Kick (device disabled/banned/deleted)
	TPong         = "pong"
	TFileOffer    = "file.offer" // FileOffer (relay upload is ready to download)
)

// Delivery modes chosen by users.
const (
	ModeP2P   = "p2p"   // only direct connections; the server never carries content
	ModeRelay = "relay" // always via the server
	ModeAuto  = "auto"  // P2P first, fall back to the server relay
)

type Hello struct {
	DeviceID   string   `json:"device_id"`
	DID        string   `json:"did"`
	Level      string   `json:"level"`
	ServerTime int64    `json:"server_time"`
	Queued     int      `json:"queued"`      // relay messages waiting for this device
	Heartbeat  int      `json:"heartbeat_s"` // send ping at least this often
	Caps       []string `json:"caps"`        // server capabilities
}

type MsgSend struct {
	To          string `json:"to"`
	Mode        string `json:"mode"`
	ClientID    string `json:"cid"`
	ContentType string `json:"ct"`   // text | file-ref | receipt | custom MIME
	Body        string `json:"body"` // base64 ciphertext (end-to-end encrypted by the clients)
	Session     string `json:"sid,omitempty"`
}

type MsgAck struct {
	ClientID string `json:"cid"`
	ID       string `json:"id"`
	Status   string `json:"status"` // delivered | queued
}

type Message struct {
	ID          string `json:"id"`
	From        string `json:"from"`
	To          string `json:"to"`
	Mode        string `json:"mode"`
	ContentType string `json:"ct"`
	Body        string `json:"body"`
	CreatedAt   int64  `json:"ts"`
}

type Receipt struct {
	IDs  []string `json:"ids"`
	Kind string   `json:"kind,omitempty"` // delivered | read (server -> device)
	By   string   `json:"by,omitempty"`
}

// Signal carries WebRTC/ICE negotiation (SDP offers/answers, candidates) or
// any other P2P rendezvous payload. The server only routes it.
type Signal struct {
	To      string          `json:"to,omitempty"`
	From    string          `json:"from,omitempty"`
	Session string          `json:"sid"`
	Kind    string          `json:"kind"` // offer | answer | candidate | bye | custom
	Mode    string          `json:"mode,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// SessionReport is sent by either side of a conversation whenever the
// transport state changes, and periodically (<= every 5 s) while active.
type SessionReport struct {
	Session   string `json:"sid"`
	Peer      string `json:"peer"`
	Mode      string `json:"mode"`
	Path      string `json:"path"`  // p2p | relay | nego | fail | idle
	Via       string `json:"via"`   // e.g. "UDP hole punch · 192.168.1.x", "TURN relay-tpe"
	State     string `json:"state"` // up | nego | fail | idle | closed
	Latency   *int   `json:"lat_ms,omitempty"`
	RateBps   int64  `json:"rate_bps"`
	Sent      int    `json:"sent"`
	Delivered int    `json:"delivered"`
	Read      int    `json:"read"`
	Failed    int    `json:"failed"`
	File      *File  `json:"file,omitempty"`
	Reason    string `json:"reason,omitempty"` // failure reason
}

type File struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Done      int64  `json:"done"`
	RateBps   int64  `json:"rate_bps"`
	Direction string `json:"dir,omitempty"` // send | recv
}

type Presence struct {
	DeviceID string `json:"device_id"`
	Online   bool   `json:"online"`
	LastSeen int64  `json:"last_seen"`
}

type PresenceSet struct {
	LanIP   string `json:"lan_ip,omitempty"`
	NATType string `json:"nat_type,omitempty"`
}

type Cmd struct {
	ID   string            `json:"cmd_id"`
	Name string            `json:"name"` // reload-frpc | rotate-key | refresh-peers | disconnect | collect-logs | restart-client
	Args map[string]string `json:"args,omitempty"`
}

type CmdResult struct {
	ID     string `json:"cmd_id"`
	OK     bool   `json:"ok"`
	Output string `json:"output,omitempty"`
}

type FRPConfigChanged struct {
	Version int64 `json:"version"`
}

type LevelChanged struct {
	Level  string `json:"level"`
	Reason string `json:"reason"`
}

type GrantUpdate struct {
	GrantID string `json:"grant_id"`
	Status  string `json:"status"`
	Target  string `json:"target"`
	Level   string `json:"level"`
}

type SessionClose struct {
	Session string `json:"sid"`
	Reason  string `json:"reason"`
}

type Kick struct {
	Reason string `json:"reason"`
}

type FileOffer struct {
	FileID string `json:"file_id"`
	From   string `json:"from"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Encode builds a frame.
func Encode(t, id string, payload any) ([]byte, error) {
	var raw json.RawMessage
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	return json.Marshal(Frame{T: t, ID: id, D: raw})
}

// Messages signed with the device identity key (ed25519). Clients sign the
// exact bytes; base64 (standard) encodes keys and signatures.

// RegisterMessage proves key possession at registration (ts = unix seconds).
func RegisterMessage(pubkey string, ts int64) []byte {
	return []byte("meshport-register:" + pubkey + ":" + itoa(ts))
}

// DeviceLoginMessage is signed to answer a login challenge.
func DeviceLoginMessage(deviceID, nonce string) []byte {
	return []byte("meshport-login:" + deviceID + ":" + nonce)
}

// RotateMessage is signed by both the old and the new key during rotation.
func RotateMessage(deviceID, newPub string) []byte {
	return []byte("meshport-rotate:" + deviceID + ":" + newPub)
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
