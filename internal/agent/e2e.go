package agent

import (
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/nacl/box"
)

// End-to-end encryption.
//
// Messages: NaCl box (X25519 + XSalsa20-Poly1305) between the sender's box
// key and the recipient's box_pubkey published by the server. The wire body
// is base64(nonce(24) || box). Because box authenticates the sender's key,
// a message that opens with the expected peer key really came from that peer.
//
// Files: a random 32-byte key per file, XChaCha20-Poly1305 over 32 KiB
// chunks. Chunk i uses nonce = prefix(16) || uint64be(i) and the additional
// data marks the final chunk, so truncation and reordering are detected.
// The key and prefix travel inside the boxed file envelope. The same
// ciphertext format is used for P2P data channels and relay uploads, so the
// server and TURN servers only ever see ciphertext.

const fileChunk = 32 << 10

// Envelope is the plaintext inside every sealed message.
type Envelope struct {
	V    int      `json:"v"`
	K    string   `json:"k"` // text | file | ack | read | accept | reject
	MID  string   `json:"mid,omitempty"`
	TS   int64    `json:"ts,omitempty"`
	Text string   `json:"text,omitempty"`
	File *FileRef `json:"file,omitempty"`
	IDs  []string `json:"ids,omitempty"`
}

// FileRef describes a file inside an envelope.
type FileRef struct {
	TID    string `json:"tid"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"` // plaintext hash (hex)
	Key    string `json:"key,omitempty"`
	Nonce  string `json:"nonce,omitempty"` // 16-byte chunk nonce prefix
	Via    string `json:"via"`             // p2p | relay
	FileID string `json:"file_id,omitempty"`
}

func decodeKey32(s string) (*[32]byte, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != 32 {
		return nil, errors.New("invalid 32-byte key")
	}
	var k [32]byte
	copy(k[:], b)
	return &k, nil
}

func seal(env *Envelope, peerPub string, myPriv *[32]byte) (string, error) {
	pub, err := decodeKey32(peerPub)
	if err != nil {
		return "", errors.New("peer has no encryption key yet (it must run a current client)")
	}
	pt, err := json.Marshal(env)
	if err != nil {
		return "", err
	}
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	out := box.Seal(nonce[:], pt, &nonce, pub, myPriv)
	return base64.StdEncoding.EncodeToString(out), nil
}

func open(body, peerPub string, myPriv *[32]byte) (*Envelope, error) {
	pub, err := decodeKey32(peerPub)
	if err != nil {
		return nil, errors.New("unknown sender key")
	}
	raw, err := base64.StdEncoding.DecodeString(body)
	if err != nil || len(raw) < 24+box.Overhead {
		return nil, errors.New("malformed ciphertext")
	}
	var nonce [24]byte
	copy(nonce[:], raw[:24])
	pt, ok := box.Open(nil, raw[24:], &nonce, pub, myPriv)
	if !ok {
		return nil, errors.New("decryption failed (wrong key or tampered message)")
	}
	var env Envelope
	if err := json.Unmarshal(pt, &env); err != nil {
		return nil, err
	}
	return &env, nil
}

// ---------- file chunk encryption ----------

type fileCipher struct {
	aead   cipher.AEAD
	prefix []byte
	size   int64 // plaintext size
}

func newFileKey() (key, prefix string) {
	k := make([]byte, 32)
	p := make([]byte, 16)
	_, _ = rand.Read(k)
	_, _ = rand.Read(p)
	return base64.StdEncoding.EncodeToString(k), base64.StdEncoding.EncodeToString(p)
}

func newFileCipher(ref *FileRef) (*fileCipher, error) {
	k, err := base64.StdEncoding.DecodeString(ref.Key)
	if err != nil || len(k) != 32 {
		return nil, errors.New("invalid file key")
	}
	p, err := base64.StdEncoding.DecodeString(ref.Nonce)
	if err != nil || len(p) != 16 {
		return nil, errors.New("invalid file nonce")
	}
	aead, err := chacha20poly1305.NewX(k)
	if err != nil {
		return nil, err
	}
	return &fileCipher{aead: aead, prefix: p, size: ref.Size}, nil
}

func (fc *fileCipher) chunks() int64 {
	if fc.size == 0 {
		return 1
	}
	return (fc.size + fileChunk - 1) / fileChunk
}

// cipherSize is the total ciphertext length for the plaintext size.
func (fc *fileCipher) cipherSize() int64 { return fc.size + fc.chunks()*int64(fc.aead.Overhead()) }

func encCipherSize(size int64) int64 {
	n := int64(1)
	if size > 0 {
		n = (size + fileChunk - 1) / fileChunk
	}
	return size + n*chacha20poly1305.Overhead
}

func (fc *fileCipher) nonce(i int64) []byte {
	n := make([]byte, 24)
	copy(n, fc.prefix)
	binary.BigEndian.PutUint64(n[16:], uint64(i))
	return n
}

func (fc *fileCipher) ad(i int64) []byte {
	if i == fc.chunks()-1 {
		return []byte{1}
	}
	return []byte{0}
}

func (fc *fileCipher) sealChunk(i int64, pt []byte) []byte {
	return fc.aead.Seal(nil, fc.nonce(i), pt, fc.ad(i))
}

func (fc *fileCipher) openChunk(i int64, ct []byte) ([]byte, error) {
	pt, err := fc.aead.Open(nil, fc.nonce(i), ct, fc.ad(i))
	if err != nil {
		return nil, errors.New("file chunk failed authentication")
	}
	return pt, nil
}

// cipherChunkLen is the ciphertext length of chunk i.
func (fc *fileCipher) cipherChunkLen(i int64) int {
	plain := int64(fileChunk)
	if i == fc.chunks()-1 {
		plain = fc.size - i*fileChunk
	}
	return int(plain) + fc.aead.Overhead()
}

// encryptStream reads plaintext from r and calls emit for each sealed chunk.
func (fc *fileCipher) encryptStream(r io.Reader, emit func(i int64, ct []byte) error) error {
	buf := make([]byte, fileChunk)
	total := fc.chunks()
	for i := int64(0); i < total; i++ {
		want := fileChunk
		if i == total-1 {
			want = int(fc.size - i*fileChunk)
		}
		if _, err := io.ReadFull(r, buf[:want]); err != nil {
			return err
		}
		if err := emit(i, fc.sealChunk(i, buf[:want])); err != nil {
			return err
		}
	}
	return nil
}

// decryptWriter accepts ciphertext bytes in any split and writes plaintext.
type decryptWriter struct {
	fc   *fileCipher
	w    io.Writer
	buf  []byte
	next int64
}

func (fc *fileCipher) decryptTo(w io.Writer) *decryptWriter { return &decryptWriter{fc: fc, w: w} }

func (d *decryptWriter) Write(p []byte) (int, error) {
	d.buf = append(d.buf, p...)
	for d.next < d.fc.chunks() {
		n := d.fc.cipherChunkLen(d.next)
		if len(d.buf) < n {
			break
		}
		pt, err := d.fc.openChunk(d.next, d.buf[:n])
		if err != nil {
			return 0, err
		}
		if _, err := d.w.Write(pt); err != nil {
			return 0, err
		}
		d.buf = d.buf[n:]
		d.next++
	}
	if d.next >= d.fc.chunks() && len(d.buf) > 0 {
		return 0, errors.New("unexpected data after the final chunk")
	}
	return len(p), nil
}

// complete reports whether every chunk was decrypted.
func (d *decryptWriter) complete() bool { return d.next == d.fc.chunks() && len(d.buf) == 0 }

// skip positions the writer at a chunk boundary (used when resuming).
func (d *decryptWriter) skipTo(chunk int64) { d.next = chunk; d.buf = nil }
