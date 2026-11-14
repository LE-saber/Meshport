package agent

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/curve25519"
)

// ---------- persisted state ----------

type Settings struct {
	Language    string            `json:"language"`     // zh-TW | zh-CN
	Mode        string            `json:"mode"`         // default delivery mode: auto | p2p | relay
	PeerModes   map[string]string `json:"peer_modes"`   // per-conversation override
	DownloadDir string            `json:"download_dir"` // where received files go
	FRPC        bool              `json:"frpc"`         // run the embedded frpc
	Autostart   bool              `json:"autostart"`
	Notify      bool              `json:"notify"`
	Theme       string            `json:"theme"` // system | light | dark
}

type State struct {
	ServerPin string   `json:"server_pin,omitempty"`
	Server    string   `json:"server"`
	DeviceID  string   `json:"device_id"`
	DID       string   `json:"did"`
	Name      string   `json:"name"`
	Level     string   `json:"level"`
	Status    string   `json:"status"`   // active | pending
	Identity  string   `json:"identity"` // base64 ed25519 seed (32 bytes)
	BoxKey    string   `json:"box_key"`  // base64 x25519 private key
	Settings  Settings `json:"settings"`
}

func (s State) identity() ed25519.PrivateKey {
	seed, err := base64.StdEncoding.DecodeString(s.Identity)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil
	}
	return ed25519.NewKeyFromSeed(seed)
}

func (s State) boxPriv() *[32]byte {
	b, err := base64.StdEncoding.DecodeString(s.BoxKey)
	if err != nil || len(b) != 32 {
		return nil
	}
	var k [32]byte
	copy(k[:], b)
	return &k
}

func (s State) boxPub() string {
	k := s.boxPriv()
	if k == nil {
		return ""
	}
	pub, err := curve25519.X25519(k[:], curve25519.Basepoint)
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(pub)
}

func (s State) fingerprint() string {
	k := s.identity()
	if k == nil {
		return ""
	}
	h := sha256.Sum256(k.Public().(ed25519.PublicKey))
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(h[:])
}

func (s State) modeFor(peer string) string {
	if m := s.Settings.PeerModes[peer]; validMode(m) {
		return m
	}
	if validMode(s.Settings.Mode) {
		return s.Settings.Mode
	}
	return "auto"
}

func validMode(m string) bool { return m == "auto" || m == "p2p" || m == "relay" }

type stateStore struct {
	path string
	mu   sync.Mutex
	s    State
}

func loadState(path string) (*stateStore, error) {
	st := &stateStore{path: path}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		st.s.Settings = Settings{Mode: "auto", FRPC: true, Notify: true, Theme: "system"}
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(b, &st.s); err != nil {
			return nil, fmt.Errorf("state.json: %w", err)
		}
	}
	if st.s.Settings.PeerModes == nil {
		st.s.Settings.PeerModes = map[string]string{}
	}
	changed := false
	if st.s.identity() == nil {
		seed := make([]byte, ed25519.SeedSize)
		_, _ = rand.Read(seed)
		st.s.Identity = base64.StdEncoding.EncodeToString(seed)
		changed = true
	}
	if st.s.boxPriv() == nil {
		k := make([]byte, 32)
		_, _ = rand.Read(k)
		st.s.BoxKey = base64.StdEncoding.EncodeToString(k)
		changed = true
	}
	if changed {
		if err := st.saveLocked(); err != nil {
			return nil, err
		}
	}
	return st, nil
}

// get returns a copy (maps are shared read-only; mutate only via update).
func (st *stateStore) get() State {
	st.mu.Lock()
	defer st.mu.Unlock()
	s := st.s
	pm := make(map[string]string, len(s.Settings.PeerModes))
	for k, v := range s.Settings.PeerModes {
		pm[k] = v
	}
	s.Settings.PeerModes = pm
	return s
}

func (st *stateStore) update(fn func(*State)) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	fn(&st.s)
	return st.saveLocked()
}

func (st *stateStore) saveLocked() error {
	b, err := json.MarshalIndent(st.s, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(st.path, b, 0o600)
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func loadOrCreateToken(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(b))) >= 32 {
		return strings.TrimSpace(string(b)), nil
	}
	tok := randHex(24)
	return tok, writeFileAtomic(path, []byte(tok), 0o600)
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newID(prefix string) string { return prefix + "_" + randHex(8) }

func nowMS() int64 { return time.Now().UnixMilli() }

// ---------- messages ----------

// Message is one chat entry as stored locally and shown in the UI.
type Message struct {
	ID       string   `json:"id"` // client-generated, travels inside the E2E envelope
	Peer     string   `json:"peer"`
	Out      bool     `json:"out"`
	Kind     string   `json:"kind"` // text | file
	Text     string   `json:"text,omitempty"`
	File     *FileRef `json:"file,omitempty"`
	TS       int64    `json:"ts"`
	Mode     string   `json:"mode"`
	Path     string   `json:"path,omitempty"`      // p2p | turn | relay
	Status   string   `json:"status"`              // pending | sending | sent | queued | delivered | read | failed | received | seen
	ServerID string   `json:"server_id,omitempty"` // relay message id
	Error    string   `json:"error,omitempty"`
	Local    string   `json:"local,omitempty"` // file path on this device
	XferID   string   `json:"xfer,omitempty"`
}

const historyKeep = 1000

// messageStore keeps per-peer conversations in memory and appends every
// change to messages/<peer>.jsonl (the last record for an id wins).
type messageStore struct {
	dir   string
	mu    sync.Mutex
	conv  map[string][]*Message
	byID  map[string]*Message
	bySrv map[string]*Message
}

func openMessageStore(dir string) (*messageStore, error) {
	ms := &messageStore{dir: dir, conv: map[string][]*Message{}, byID: map[string]*Message{}, bySrv: map[string]*Message{}}
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if err := ms.load(f); err != nil {
			return nil, err
		}
	}
	return ms, nil
}

func (ms *messageStore) load(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	byID := map[string]*Message{}
	var order []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	lines := 0
	for sc.Scan() {
		lines++
		var m Message
		if json.Unmarshal(sc.Bytes(), &m) != nil || m.ID == "" {
			continue
		}
		if _, ok := byID[m.ID]; !ok {
			order = append(order, m.ID)
		}
		mm := m
		byID[m.ID] = &mm
	}
	list := make([]*Message, 0, len(order))
	for _, id := range order {
		m := byID[id]
		if m.Status == "sending" { // interrupted by a restart
			m.Status, m.Error = "failed", "interrupted"
		}
		list = append(list, m)
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].TS < list[j].TS })
	if len(list) > historyKeep {
		list = list[len(list)-historyKeep:]
	}
	if len(list) == 0 {
		return nil
	}
	peer := list[0].Peer
	ms.conv[peer] = list
	for _, m := range list {
		ms.byID[m.ID] = m
		if m.ServerID != "" {
			ms.bySrv[m.ServerID] = m
		}
	}
	if lines > 2*len(list)+50 { // compact
		var b strings.Builder
		for _, m := range list {
			j, _ := json.Marshal(m)
			b.Write(j)
			b.WriteByte('\n')
		}
		_ = writeFileAtomic(path, []byte(b.String()), 0o600)
	}
	return nil
}

func (ms *messageStore) file(peer string) string {
	safe := strings.Map(func(r rune) rune {
		if r == '_' || r == '-' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, peer)
	return filepath.Join(ms.dir, safe+".jsonl")
}

func (ms *messageStore) appendLocked(m *Message) {
	f, err := os.OpenFile(ms.file(m.Peer), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	j, _ := json.Marshal(m)
	_, _ = f.Write(append(j, '\n'))
	f.Close()
}

// add stores a copy of a new message; returns false if the id already
// exists (dedupe). The caller keeps ownership of m.
func (ms *messageStore) add(in *Message) bool {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if _, ok := ms.byID[in.ID]; ok {
		return false
	}
	cp := *in
	if in.File != nil {
		f := *in.File
		cp.File = &f
	}
	m := &cp
	ms.conv[m.Peer] = append(ms.conv[m.Peer], m)
	if l := ms.conv[m.Peer]; len(l) > historyKeep {
		for _, old := range l[:len(l)-historyKeep] {
			delete(ms.byID, old.ID)
			delete(ms.bySrv, old.ServerID)
		}
		ms.conv[m.Peer] = l[len(l)-historyKeep:]
	}
	ms.byID[m.ID] = m
	if m.ServerID != "" {
		ms.bySrv[m.ServerID] = m
	}
	ms.appendLocked(m)
	return true
}

// update applies fn to a message and persists it; returns a copy.
func (ms *messageStore) update(id string, fn func(*Message)) (Message, bool) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	m := ms.byID[id]
	if m == nil {
		return Message{}, false
	}
	fn(m)
	if m.ServerID != "" {
		ms.bySrv[m.ServerID] = m
	}
	ms.appendLocked(m)
	return *m, true
}

func (ms *messageStore) get(id string) (Message, bool) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if m := ms.byID[id]; m != nil {
		return *m, true
	}
	return Message{}, false
}

func (ms *messageStore) idByServer(sid string) string {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if m := ms.bySrv[sid]; m != nil {
		return m.ID
	}
	return ""
}

func (ms *messageStore) list(peer string, before int64, limit int) []Message {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	l := ms.conv[peer]
	out := []Message{}
	for i := len(l) - 1; i >= 0 && len(out) < limit; i-- {
		if before > 0 && l[i].TS >= before {
			continue
		}
		out = append(out, *l[i])
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// pending returns outgoing messages waiting for a transport.
func (ms *messageStore) pending() []Message {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	var out []Message
	for _, l := range ms.conv {
		for _, m := range l {
			if m.Out && m.Status == "pending" {
				out = append(out, *m)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	return out
}

// markSeen flips received messages of a peer to seen and returns them.
func (ms *messageStore) markSeen(peer string) []Message {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	var out []Message
	for _, m := range ms.conv[peer] {
		if !m.Out && m.Status == "received" {
			m.Status = "seen"
			ms.appendLocked(m)
			out = append(out, *m)
		}
	}
	return out
}

type ConvSummary struct {
	Peer   string   `json:"peer"`
	Last   *Message `json:"last,omitempty"`
	Unread int      `json:"unread"`
}

func (ms *messageStore) summaries() map[string]ConvSummary {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	out := map[string]ConvSummary{}
	for peer, l := range ms.conv {
		if len(l) == 0 {
			continue
		}
		last := *l[len(l)-1]
		s := ConvSummary{Peer: peer, Last: &last}
		for _, m := range l {
			if !m.Out && m.Status == "received" {
				s.Unread++
			}
		}
		out[peer] = s
	}
	return out
}

func (ms *messageStore) unreadTotal() int {
	n := 0
	for _, s := range ms.summaries() {
		n += s.Unread
	}
	return n
}

func (ms *messageStore) clear(peer string) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	for _, m := range ms.conv[peer] {
		delete(ms.byID, m.ID)
		delete(ms.bySrv, m.ServerID)
	}
	delete(ms.conv, peer)
	_ = os.Remove(ms.file(peer))
}

// ---------- transfers ----------

type Transfer struct {
	ID       string `json:"id"`
	Peer     string `json:"peer"`
	PeerName string `json:"peer_name"`
	Dir      string `json:"dir"` // send | recv
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Done     int64  `json:"done"`
	RateBps  int64  `json:"rate_bps"`
	Path     string `json:"path"`  // p2p | turn | relay
	State    string `json:"state"` // queued | running | done | failed | canceled
	Error    string `json:"error,omitempty"`
	MsgID    string `json:"msg_id"`
	Started  int64  `json:"started"`
	Finished int64  `json:"finished,omitempty"`
	Local    string `json:"local,omitempty"`

	cancel   func()
	lastTick time.Time
	lastDone int64
}

type transferStore struct {
	mu sync.Mutex
	m  map[string]*Transfer
}

func newTransferStore() *transferStore { return &transferStore{m: map[string]*Transfer{}} }

func (ts *transferStore) put(t *Transfer) {
	ts.mu.Lock()
	ts.m[t.ID] = t
	ts.mu.Unlock()
}

func (ts *transferStore) get(id string) *Transfer {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.m[id]
}

// mutate changes a transfer under the lock and returns a snapshot.
func (ts *transferStore) mutate(id string, fn func(*Transfer)) (Transfer, bool) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	t := ts.m[id]
	if t == nil {
		return Transfer{}, false
	}
	fn(t)
	return t.snapshot(), true
}

func (t *Transfer) snapshot() Transfer {
	c := *t
	c.cancel = nil
	return c
}

func (ts *transferStore) list() []Transfer {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	out := make([]Transfer, 0, len(ts.m))
	for _, t := range ts.m {
		out = append(out, t.snapshot())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started > out[j].Started })
	return out
}

func (ts *transferStore) active(peer string) *Transfer {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for _, t := range ts.m {
		if t.Peer == peer && t.State == "running" {
			s := t.snapshot()
			return &s
		}
	}
	return nil
}

func (ts *transferStore) prune() {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	cut := time.Now().Add(-24 * time.Hour).UnixMilli()
	for id, t := range ts.m {
		if t.Finished > 0 && t.Finished < cut {
			delete(ts.m, id)
		}
	}
}

// ---------- logs ----------

// logRing writes log lines to a size-capped file, optionally stderr, and
// keeps the last lines in memory for the UI and the collect-logs command.
type logRing struct {
	mu     sync.Mutex
	path   string
	f      *os.File
	size   int64
	lines  []string
	stderr bool
}

func newLogRing(path string, stderr bool) (*logRing, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	st, _ := f.Stat()
	var size int64
	if st != nil {
		size = st.Size()
	}
	return &logRing{path: path, f: f, size: size, stderr: stderr}, nil
}

func (l *logRing) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stderr {
		_, _ = os.Stderr.Write(p)
	}
	if l.f != nil {
		if l.size > 5<<20 {
			l.f.Close()
			_ = os.Rename(l.path, l.path+".1")
			l.f, _ = os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			l.size = 0
		}
		if l.f != nil {
			n, _ := l.f.Write(p)
			l.size += int64(n)
		}
	}
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		l.lines = append(l.lines, line)
	}
	if len(l.lines) > 500 {
		l.lines = append([]string(nil), l.lines[len(l.lines)-500:]...)
	}
	return len(p), nil
}

func (l *logRing) tail(n int) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n > len(l.lines) {
		n = len(l.lines)
	}
	return append([]string(nil), l.lines[len(l.lines)-n:]...)
}

func (l *logRing) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		l.f.Close()
		l.f = nil
	}
}

var _ io.Writer = (*logRing)(nil)

// ---------- events (server-sent events to the UI) ----------

type eventHub struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

func newEventHub() *eventHub { return &eventHub{subs: map[chan []byte]struct{}{}} }

func (h *eventHub) subscribe() chan []byte {
	ch := make(chan []byte, 256)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *eventHub) unsubscribe(ch chan []byte) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}

func (h *eventHub) publish(typ string, data any) {
	b, err := json.Marshal(map[string]any{"type": typ, "data": data})
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- b:
		default: // slow consumer; it resyncs on reconnect
		}
	}
}

func (h *eventHub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}
