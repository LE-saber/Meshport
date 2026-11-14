package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"

	"meshport/pkg/client"
	"meshport/pkg/proto"
)

// File transfers. Every file is encrypted with its own key (see e2e.go).
//
// P2P: the sender seals a "file" envelope over the link, waits for the
// receiver's "accept", streams encrypted chunks over a dedicated data
// channel "f:<tid>" and finishes with the text frame "end". The receiver
// verifies the plaintext SHA-256 and answers with an "ack" envelope.
//
// Relay: the sender encrypts to a temp file, uploads it with the resumable
// /files API and then sends the "file" envelope (with the server file id)
// through the message relay. The receiver downloads with Range requests,
// resuming at chunk boundaries after network errors.

// SendFile queues a file from a local path.
func (a *Agent) SendFile(peer, path, name, mode string, removeAfter bool) (Message, error) {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return Message{}, errors.New("找不到檔案")
	}
	if a.peer(peer) == nil {
		return Message{}, errors.New("找不到這台裝置，或無權傳送給它")
	}
	if !validMode(mode) {
		mode = a.st.get().modeFor(peer)
	}
	if name == "" {
		name = filepath.Base(path)
	}
	sum, err := fileSHA256(path)
	if err != nil {
		return Message{}, err
	}
	key, nonce := newFileKey()
	tid := newID("x")
	ref := &FileRef{TID: tid, Name: name, Size: st.Size(), SHA256: sum, Key: key, Nonce: nonce}
	m := &Message{ID: newID("m"), Peer: peer, Out: true, Kind: "file", File: ref, TS: nowMS(), Mode: mode, Status: "sending", Local: path, XferID: tid}
	a.xfers.put(&Transfer{ID: tid, Peer: peer, PeerName: a.peerName(peer), Dir: "send", Name: name, Size: st.Size(), State: "queued", MsgID: m.ID, Started: nowMS(), Local: path})
	if removeAfter {
		outboxMu.Lock()
		outboxFiles[tid] = path
		outboxMu.Unlock()
	}
	a.msgs.add(m)
	a.events.publish("message", a.msgView(*m))
	go a.deliver(m.ID)
	return a.msgView(*m), nil
}

var (
	outboxMu    sync.Mutex
	outboxFiles = map[string]string{} // tid -> temp copy uploaded from the UI
)

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (a *Agent) setXfer(id string, fn func(*Transfer)) {
	if t, ok := a.xfers.mutate(id, fn); ok {
		a.events.publish("transfer", t)
	}
}

// progress updates done bytes and the rate (published at most ~4x/s).
func (a *Agent) progress(id string, done int64) {
	if t, ok := a.xfers.mutate(id, func(t *Transfer) {
		t.Done = done
		if t.lastTick.IsZero() {
			t.lastTick = time.Now()
			t.lastDone = done
		}
		if dt := time.Since(t.lastTick); dt > 250*time.Millisecond {
			t.RateBps = int64(float64(done-t.lastDone) / dt.Seconds())
			t.lastTick = time.Now()
			t.lastDone = done
		}
	}); ok && (time.Since(t.lastTick) < 20*time.Millisecond || done == t.Size) {
		a.events.publish("transfer", t)
	}
}

func (a *Agent) finishXfer(id, state, errMsg string) {
	a.setXfer(id, func(t *Transfer) {
		t.State, t.Error, t.Finished = state, errMsg, nowMS()
		if state == "done" {
			t.Done, t.RateBps = t.Size, 0
		}
	})
	if state == "done" {
		outboxMu.Lock()
		p := outboxFiles[id]
		delete(outboxFiles, id)
		outboxMu.Unlock()
		if p != "" {
			_ = os.Remove(p)
		}
	}
}

// runFileSend delivers an outgoing file message according to its mode.
func (a *Agent) runFileSend(msgID string) {
	m, ok := a.msgs.get(msgID)
	if !ok || m.File == nil {
		return
	}
	tid := m.File.TID
	if a.xfers.get(tid) == nil { // after a restart
		a.xfers.put(&Transfer{ID: tid, Peer: m.Peer, PeerName: a.peerName(m.Peer), Dir: "send", Name: m.File.Name, Size: m.File.Size, State: "queued", MsgID: m.ID, Started: nowMS(), Local: m.Local})
	}
	if _, err := os.Stat(m.Local); err != nil {
		a.setMsg(msgID, func(m *Message) { m.Status, m.Error = "failed", "原始檔案已不存在" })
		a.finishXfer(tid, "failed", "原始檔案已不存在")
		return
	}
	ctx, cancel := context.WithCancel(a.ctx)
	defer cancel()
	a.setXfer(tid, func(t *Transfer) { t.State, t.Error, t.Done, t.cancel = "running", "", 0, cancel })
	a.setMsg(msgID, func(m *Message) { m.Status, m.Error = "sending", "" })
	p := a.peerFresh(ctx, m.Peer)
	if p == nil {
		a.setMsg(msgID, func(m *Message) { m.Status, m.Error = "failed", "對方已不在可見範圍內" })
		a.finishXfer(tid, "failed", "對方已不在可見範圍內")
		return
	}
	var err error
	switch m.Mode {
	case proto.ModeRelay:
		err = a.sendFileRelay(ctx, m, p)
	case proto.ModeP2P:
		var l *link
		if !p.Online {
			err = errors.New("等待對方上線以建立直連")
		} else if l, err = a.links.ensure(ctx, p.ID, proto.ModeP2P, 15*time.Second); err == nil {
			err = a.sendFileP2P(ctx, m, p, l)
		}
		if err != nil && ctx.Err() == nil {
			a.setMsg(msgID, func(m *Message) { m.Status, m.Error = "pending", "直連傳送未完成："+err.Error() })
			a.setXfer(tid, func(t *Transfer) { t.State, t.Error = "queued", err.Error() })
			return
		}
	default:
		err = errors.New("no link")
		if p.Online {
			if l, lerr := a.links.ensure(ctx, p.ID, proto.ModeAuto, 8*time.Second); lerr == nil {
				err = a.sendFileP2P(ctx, m, p, l)
				if errors.Is(err, errDeclined) {
					break
				}
			}
		}
		if err != nil && !errors.Is(err, errDeclined) && ctx.Err() == nil {
			a.log.Info("file falls back to relay", "tid", tid, "reason", err)
			a.setXfer(tid, func(t *Transfer) { t.Done = 0 })
			err = a.sendFileRelay(ctx, m, p)
		}
	}
	switch {
	case ctx.Err() != nil:
		a.setMsg(msgID, func(m *Message) { m.Status, m.Error = "failed", "已取消" })
		a.finishXfer(tid, "canceled", "")
	case err != nil:
		a.setMsg(msgID, func(m *Message) { m.Status, m.Error = "failed", friendlyErr(err).Error() })
		a.finishXfer(tid, "failed", friendlyErr(err).Error())
	default:
		a.finishXfer(tid, "done", "")
	}
}

var errDeclined = errors.New("對方拒絕接收")

func (a *Agent) sendFileP2P(ctx context.Context, m Message, p *Peer, l *link) error {
	ref := *m.File
	ref.Via, ref.FileID = "p2p", ""
	body, err := seal(&Envelope{V: 1, K: "file", MID: m.ID, TS: m.TS, File: &ref}, p.BoxPubKey, a.st.get().boxPriv())
	if err != nil {
		return err
	}
	tid := ref.TID
	path := l.pathName()
	a.setXfer(tid, func(t *Transfer) { t.Path = path })
	acceptCh := l.addWaiter(tid)
	defer l.dropWaiter(tid)
	if err := l.sendEnvelope(body); err != nil {
		return err
	}
	select {
	case v := <-acceptCh:
		if v == "reject" {
			return errDeclined
		}
		if v != "accept" {
			return errors.New("P2P 連線中斷")
		}
	case <-time.After(30 * time.Second):
		return errors.New("對方沒有回應")
	case <-ctx.Done():
		return ctx.Err()
	}
	l.mu.Lock()
	pc := l.pc
	l.mu.Unlock()
	dc, err := pc.CreateDataChannel("f:"+tid, nil)
	if err != nil {
		return err
	}
	defer dc.Close()
	opened := make(chan struct{})
	var openOnce sync.Once
	dc.OnOpen(func() { openOnce.Do(func() { close(opened) }) })
	select {
	case <-opened:
	case <-time.After(15 * time.Second):
		return errors.New("檔案通道開啟逾時")
	case <-l.done:
		return errors.New("P2P 連線中斷")
	}
	const highWater, lowWater = 4 << 20, 1 << 20
	low := make(chan struct{}, 1)
	dc.SetBufferedAmountLowThreshold(lowWater)
	dc.OnBufferedAmountLow(func() {
		select {
		case low <- struct{}{}:
		default:
		}
	})
	ackCh := waitAck(m.ID)
	defer ackWaiters.Delete(m.ID)
	fc, err := newFileCipher(m.File)
	if err != nil {
		return err
	}
	f, err := os.Open(m.Local)
	if err != nil {
		return err
	}
	defer f.Close()
	var done int64
	err = fc.encryptStream(f, func(i int64, ct []byte) error {
		for dc.BufferedAmount() > highWater {
			select {
			case <-low:
			case <-time.After(30 * time.Second):
				return errors.New("對方接收停滯")
			case <-l.done:
				return errors.New("P2P 連線中斷")
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if err := dc.Send(ct); err != nil {
			return err
		}
		l.addBytes(len(ct))
		done += int64(len(ct) - fc.aead.Overhead())
		a.progress(tid, done)
		return nil
	})
	if err != nil {
		return err
	}
	if err := dc.SendText("end"); err != nil {
		return err
	}
	select {
	case <-ackCh:
		a.applyReceipt(m.ID, "delivered")
		a.setMsg(m.ID, func(mm *Message) { mm.Path = path })
		return nil
	case <-time.After(2 * time.Minute):
		return errors.New("對方未確認收到檔案")
	case <-l.done:
		return errors.New("P2P 連線在確認前中斷")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *Agent) sendFileRelay(ctx context.Context, m Message, p *Peer) error {
	tid := m.File.TID
	a.setXfer(tid, func(t *Transfer) { t.Path = "relay" })
	fc, err := newFileCipher(m.File)
	if err != nil {
		return err
	}
	// 1. encrypt to a temp file and hash the ciphertext
	encPath := filepath.Join(a.opt.DataDir, "tmp", tid+".enc")
	defer os.Remove(encPath)
	src, err := os.Open(m.Local)
	if err != nil {
		return err
	}
	dst, err := os.OpenFile(encPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		src.Close()
		return err
	}
	h := sha256.New()
	w := io.MultiWriter(dst, h)
	err = fc.encryptStream(src, func(_ int64, ct []byte) error { _, e := w.Write(ct); return e })
	src.Close()
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	encSize := fc.cipherSize()
	// 2. create the upload
	var created struct {
		ID       string `json:"id"`
		ChunkMax int64  `json:"chunk_max"`
	}
	if err := a.api(ctx, "POST", "/files", map[string]any{"to": p.ID, "name": tid + ".bin", "size": encSize, "sha256": hex.EncodeToString(h.Sum(nil))}, &created); err != nil {
		return err
	}
	chunk := created.ChunkMax
	if chunk <= 0 || chunk > 4<<20 {
		chunk = 4 << 20
	}
	// 3. upload with resume
	f, err := os.Open(encPath)
	if err != nil {
		return err
	}
	defer f.Close()
	var off int64
	buf := make([]byte, chunk)
	failures := 0
	for off < encSize {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		n, err := f.ReadAt(buf, off)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		newOff, complete, err := a.uploadChunk(ctx, created.ID, off, buf[:n])
		if err != nil {
			var ae *client.APIError
			if errors.As(err, &ae) && ae.Code == "checksum_mismatch" {
				return errors.New("上傳內容校驗失敗")
			}
			failures++
			if failures > 8 {
				return fmt.Errorf("上傳失敗：%w", err)
			}
			select {
			case <-time.After(time.Duration(failures) * 2 * time.Second):
			case <-ctx.Done():
				return ctx.Err()
			}
			if o, qerr := a.uploadOffset(ctx, created.ID); qerr == nil {
				off = o
			}
			continue
		}
		failures = 0
		off = newOff
		plain := off * fc.size / max64(encSize, 1)
		a.progress(tid, plain)
		if complete {
			break
		}
	}
	// 4. send the envelope through the relay
	ref := *m.File
	ref.Via, ref.FileID = "relay", created.ID
	body, err := seal(&Envelope{V: 1, K: "file", MID: m.ID, TS: m.TS, File: &ref}, p.BoxPubKey, a.st.get().boxPriv())
	if err != nil {
		return err
	}
	mode := m.Mode
	if mode == proto.ModeP2P {
		return errors.New("僅 P2P 模式不可使用伺服器中繼")
	}
	a.sendViaRelay(ctx, m.ID, p.ID, mode, "file-ref", body)
	if mm, _ := a.msgs.get(m.ID); mm.Status == "failed" || mm.Status == "pending" {
		return errors.New(mm.Error)
	}
	return nil
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// rawDo performs a non-JSON request with the device token.
func (a *Agent) rawDo(ctx context.Context, method, path string, hdr http.Header, body []byte) (*http.Response, error) {
	cl := a.newClient()
	httpClient := *cl.HTTP
	httpClient.Timeout = 0 // large transfers are governed by the caller's context
	cl.HTTP = &httpClient  // keep the REST client's timeout unchanged
	do := func() (*http.Response, error) {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, cl.BaseURL+"/api/v1"+path, rd)
		if err != nil {
			return nil, err
		}
		for k, v := range hdr {
			req.Header[k] = v
		}
		a.mu.Lock()
		req.Header.Set("Authorization", "Bearer "+a.token)
		a.mu.Unlock()
		return cl.DoRequest(req)
	}
	resp, err := do()
	if err == nil && resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		if err := a.login(ctx); err != nil {
			return nil, err
		}
		resp, err = do()
	}
	return resp, err
}

func apiErrFrom(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var e struct {
		Error *client.APIError `json:"error"`
	}
	if json.Unmarshal(data, &e) == nil && e.Error != nil {
		e.Error.Status = resp.StatusCode
		return e.Error
	}
	return &client.APIError{Status: resp.StatusCode, Code: "http", Message: strings.TrimSpace(string(data))}
}

func (a *Agent) uploadChunk(ctx context.Context, id string, off int64, data []byte) (int64, bool, error) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	hdr := http.Header{"Upload-Offset": {strconv.FormatInt(off, 10)}, "Content-Type": {"application/offset+octet-stream"}}
	resp, err := a.rawDo(cctx, "PATCH", "/files/"+id, hdr, data)
	if err != nil {
		return 0, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return 0, false, apiErrFrom(resp)
	}
	var out struct {
		Offset   int64 `json:"offset"`
		Complete bool  `json:"complete"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, false, err
	}
	return out.Offset, out.Complete, nil
}

func (a *Agent) uploadOffset(ctx context.Context, id string) (int64, error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	resp, err := a.rawDo(cctx, "GET", "/files/"+id, nil, nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return 0, apiErrFrom(resp)
	}
	return strconv.ParseInt(resp.Header.Get("Upload-Offset"), 10, 64)
}

// ---------- receiving ----------

type incomingP2P struct {
	msgID string
	ref   *FileRef
}

func (a *Agent) startFileReceive(msgID, from string, ref *FileRef, l *link) {
	if ref.Size < 0 || ref.TID == "" {
		return
	}
	path := "relay"
	if l != nil {
		path = l.pathName()
	}
	a.xfers.put(&Transfer{ID: ref.TID, Peer: from, PeerName: a.peerName(from), Dir: "recv", Name: ref.Name, Size: ref.Size, State: "running", MsgID: msgID, Started: nowMS(), Path: path})
	a.setMsg(msgID, func(m *Message) { m.XferID = ref.TID })
	a.events.publish("transfer", a.xfers.get(ref.TID).snapshot())
	if ref.Via == "p2p" {
		if l == nil {
			a.finishXfer(ref.TID, "failed", "P2P 連線不存在")
			return
		}
		l.mu.Lock()
		l.recv[ref.TID] = &incomingP2P{msgID: msgID, ref: ref}
		l.mu.Unlock()
		a.sendControl(from, &Envelope{V: 1, K: "accept", IDs: []string{ref.TID}}, l)
		return
	}
	go a.downloadRelay(msgID, from, ref)
}

// onFileChannel handles an incoming "f:<tid>" data channel.
func (a *Agent) onFileChannel(l *link, dc *webrtc.DataChannel) {
	tid := strings.TrimPrefix(dc.Label(), "f:")
	l.mu.Lock()
	inc := l.recv[tid]
	l.mu.Unlock()
	if inc == nil {
		dc.Close()
		return
	}
	sink, err := a.newFileSink(inc.ref)
	if err != nil {
		a.finishXfer(tid, "failed", err.Error())
		dc.Close()
		return
	}
	var mu sync.Mutex
	finished := false
	var done int64
	finish := func(errMsg string) {
		mu.Lock()
		if finished {
			mu.Unlock()
			return
		}
		finished = true
		mu.Unlock()
		l.mu.Lock()
		delete(l.recv, tid)
		l.mu.Unlock()
		if errMsg != "" {
			sink.abort()
			a.finishXfer(tid, "failed", errMsg)
			return
		}
		final, err := sink.commit(a.st.get().Settings.DownloadDir)
		if err != nil {
			a.finishXfer(tid, "failed", err.Error())
			return
		}
		a.receivedFile(inc.msgID, l.peer, tid, final, inc.ref.Name)
		a.sendControl(l.peer, &Envelope{V: 1, K: "ack", IDs: []string{inc.msgID}}, l)
		go func() { time.Sleep(time.Second); dc.Close() }()
	}
	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		if msg.IsString {
			if string(msg.Data) == "end" {
				if !sink.dw.complete() {
					finish("檔案不完整")
				} else if sink.sum() != strings.ToLower(inc.ref.SHA256) {
					finish("檔案校驗失敗")
				} else {
					finish("")
				}
			}
			return
		}
		l.addBytes(len(msg.Data))
		if _, err := sink.dw.Write(msg.Data); err != nil {
			finish(err.Error())
			dc.Close()
			return
		}
		done = sink.dw.next * fileChunk
		if done > inc.ref.Size {
			done = inc.ref.Size
		}
		a.progress(tid, done)
	})
	dc.OnClose(func() { finish("傳輸中斷") })
}

// fileSink decrypts into a .part file in the download folder.
type fileSink struct {
	f       *os.File
	path    string
	h       hash.Hash
	dw      *decryptWriter
	fc      *fileCipher
	display string
}

func (a *Agent) newFileSink(ref *FileRef) (*fileSink, error) {
	fc, err := newFileCipher(ref)
	if err != nil {
		return nil, err
	}
	dir := a.st.get().Settings.DownloadDir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, ".meshport-"+ref.TID+".part")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	s := &fileSink{f: f, path: path, h: sha256.New(), fc: fc, display: ref.Name}
	s.dw = fc.decryptTo(io.MultiWriter(f, s.h))
	return s, nil
}

func (s *fileSink) sum() string { return hex.EncodeToString(s.h.Sum(nil)) }

func (s *fileSink) abort() {
	s.f.Close()
	_ = os.Remove(s.path)
}

func (s *fileSink) commit(dir string) (string, error) {
	if err := s.f.Close(); err != nil {
		return "", err
	}
	final := uniquePath(dir, s.name())
	if err := os.Rename(s.path, final); err != nil {
		return "", err
	}
	return final, nil
}

func (s *fileSink) name() string { return s.display }

// safeName strips directories and characters that are invalid on Windows.
func safeName(n string) string {
	n = filepath.Base(strings.ReplaceAll(n, "\\", "/"))
	n = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, n)
	n = strings.Trim(n, " .")
	if n == "" {
		n = "file"
	}
	if len(n) > 180 {
		ext := filepath.Ext(n)
		n = n[:180-len(ext)] + ext
	}
	return n
}

func uniquePath(dir, name string) string {
	name = safeName(name)
	p := filepath.Join(dir, name)
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 1; ; i++ {
		if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
			return p
		}
		p = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, i, ext))
	}
}

func (a *Agent) receivedFile(msgID, peer, tid, final, name string) {
	a.setMsg(msgID, func(m *Message) { m.Local = final })
	a.setXfer(tid, func(t *Transfer) { t.Local = final })
	a.finishXfer(tid, "done", "")
	a.log.Info("file received", "from", peer, "name", name, "path", final)
	a.notify(a.peerName(peer), "檔案已接收："+name)
}

func (a *Agent) downloadRelay(msgID, from string, ref *FileRef) {
	tid := ref.TID
	ctx, cancel := context.WithCancel(a.ctx)
	defer cancel()
	a.setXfer(tid, func(t *Transfer) { t.cancel = cancel })
	sink, err := a.newFileSink(ref)
	if err != nil {
		a.finishXfer(tid, "failed", err.Error())
		return
	}
	cipherChunk := int64(fileChunk + sink.fc.aead.Overhead())
	failures := 0
	for !sink.dw.complete() {
		if ctx.Err() != nil {
			sink.abort()
			a.finishXfer(tid, "canceled", "")
			return
		}
		offset := sink.dw.next * cipherChunk
		hdr := http.Header{}
		if offset > 0 {
			hdr.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		}
		resp, err := a.rawDo(ctx, "GET", "/files/"+ref.FileID+"/content", hdr, nil)
		if err == nil && resp.StatusCode >= 300 {
			err = apiErrFrom(resp)
			resp.Body.Close()
			var ae *client.APIError
			if errors.As(err, &ae) && (ae.Status == 404 || ae.Status == 403 || ae.Status == 410) {
				sink.abort()
				a.finishXfer(tid, "failed", "伺服器上的檔案已不存在或已過期")
				return
			}
		}
		if err == nil {
			if offset > 0 && resp.StatusCode != http.StatusPartialContent {
				resp.Body.Close()
				err = errors.New("server ignored Range")
			} else {
				buf := make([]byte, 64<<10)
				for {
					n, rerr := resp.Body.Read(buf)
					if n > 0 {
						if _, werr := sink.dw.Write(buf[:n]); werr != nil {
							resp.Body.Close()
							sink.abort()
							a.finishXfer(tid, "failed", werr.Error())
							return
						}
						done := sink.dw.next * fileChunk
						if done > ref.Size {
							done = ref.Size
						}
						a.progress(tid, done)
					}
					if rerr != nil {
						if !errors.Is(rerr, io.EOF) {
							err = rerr
						}
						break
					}
				}
				resp.Body.Close()
			}
		}
		if sink.dw.complete() {
			break
		}
		if err == nil {
			err = errors.New("下載提前結束")
		}
		failures++
		if failures > 6 {
			sink.abort()
			a.finishXfer(tid, "failed", "下載失敗："+err.Error())
			return
		}
		// resume at the last whole chunk: rewind the partial file and hash
		if rerr := sink.rewind(); rerr != nil {
			sink.abort()
			a.finishXfer(tid, "failed", rerr.Error())
			return
		}
		select {
		case <-time.After(time.Duration(failures) * 2 * time.Second):
		case <-ctx.Done():
		}
	}
	if sink.sum() != strings.ToLower(ref.SHA256) {
		sink.abort()
		a.finishXfer(tid, "failed", "檔案校驗失敗")
		return
	}
	final, err := sink.commit(a.st.get().Settings.DownloadDir)
	if err != nil {
		a.finishXfer(tid, "failed", err.Error())
		return
	}
	a.receivedFile(msgID, from, tid, final, ref.Name)
	dctx, dcancel := context.WithTimeout(a.ctx, 10*time.Second)
	if resp, err := a.rawDo(dctx, "DELETE", "/files/"+ref.FileID, nil, nil); err == nil {
		resp.Body.Close()
	}
	dcancel()
}

// rewind truncates the part file to the decrypted whole chunks and rebuilds
// the running hash so a download can resume at a chunk boundary.
func (s *fileSink) rewind() error {
	keep := s.dw.next * fileChunk
	if keep > s.fc.size {
		keep = s.fc.size
	}
	if err := s.f.Truncate(keep); err != nil {
		return err
	}
	if _, err := s.f.Seek(keep, io.SeekStart); err != nil {
		return err
	}
	rf, err := os.Open(s.path)
	if err != nil {
		return err
	}
	defer rf.Close()
	s.h = sha256.New()
	if _, err := io.CopyN(s.h, rf, keep); err != nil {
		return err
	}
	s.dw.buf = nil
	s.dw.w = io.MultiWriter(s.f, s.h)
	return nil
}

// CancelTransfer stops a running transfer.
func (a *Agent) CancelTransfer(id string) error {
	t := a.xfers.get(id)
	if t == nil {
		return errors.New("找不到傳輸")
	}
	a.xfers.mu.Lock()
	cancel := t.cancel
	a.xfers.mu.Unlock()
	if cancel == nil {
		return errors.New("傳輸未在進行中")
	}
	cancel()
	return nil
}
