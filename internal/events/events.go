// Package events is the in-process event bus plus the log sink that feeds
// the admin live stream, webhooks, alerting and the persisted service log.
package events

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

type Event struct {
	Type string    `json:"type"`
	Time time.Time `json:"time"`
	Data any       `json:"data"`
}

type Bus struct {
	mu   sync.RWMutex
	next int
	subs map[int]chan Event
}

func NewBus() *Bus { return &Bus{subs: map[int]chan Event{}} }

// Publish never blocks: slow subscribers lose events.
func (b *Bus) Publish(typ string, data any) {
	ev := Event{Type: typ, Time: time.Now().UTC(), Data: data}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (b *Bus) Subscribe(buf int) (<-chan Event, func()) {
	ch := make(chan Event, buf)
	b.mu.Lock()
	id := b.next
	b.next++
	b.subs[id] = ch
	b.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, id)
			b.mu.Unlock()
		})
	}
}

// Match reports whether an event type matches a webhook/stream filter such
// as "*", "device.*" or "grant.approved".
func Match(filters []string, typ string) bool {
	for _, f := range filters {
		if f == "*" || f == typ {
			return true
		}
		if strings.HasSuffix(f, ".*") && strings.HasPrefix(typ, strings.TrimSuffix(f, "*")) {
			return true
		}
	}
	return false
}

// ---------- log sink ----------

type LogEntry struct {
	ID     int64     `json:"id"`
	TS     time.Time `json:"ts"`
	Level  string    `json:"level"`
	Source string    `json:"source"`
	Msg    string    `json:"msg"`
}

// LogSink fans log records out to: the wrapped handler (stderr/journald), a
// ring buffer, a persistence callback and the event bus.
type LogSink struct {
	inner   slog.Handler
	bus     *Bus
	mu      *sync.Mutex
	ring    *[]LogEntry
	persist *func([]LogEntry)
	pending *[]LogEntry
	attrs   []slog.Attr
	group   string
}

const ringSize = 2000

func NewLogSink(inner slog.Handler, bus *Bus) *LogSink {
	ring := make([]LogEntry, 0, ringSize)
	pending := []LogEntry{}
	var p func([]LogEntry)
	return &LogSink{inner: inner, bus: bus, mu: &sync.Mutex{}, ring: &ring, pending: &pending, persist: &p}
}

// SetPersist installs the function that stores batches (called by Run).
func (s *LogSink) SetPersist(f func([]LogEntry)) {
	s.mu.Lock()
	*s.persist = f
	s.mu.Unlock()
}

// Run flushes pending entries to the persistence callback every second.
func (s *LogSink) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			s.flush()
			return
		case <-t.C:
			s.flush()
		}
	}
}

func (s *LogSink) flush() {
	s.mu.Lock()
	batch := *s.pending
	*s.pending = []LogEntry{}
	p := *s.persist
	s.mu.Unlock()
	if len(batch) > 0 && p != nil {
		p(batch)
	}
}

func (s *LogSink) Recent(n int, level string) []LogEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []LogEntry{}
	r := *s.ring
	for i := len(r) - 1; i >= 0 && len(out) < n; i-- {
		if level == "" || r[i].Level == level {
			out = append(out, r[i])
		}
	}
	return out
}

// The sink always keeps info and above for the console, whatever the stderr level.
func (s *LogSink) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= slog.LevelInfo || s.inner.Enabled(ctx, l)
}

func (s *LogSink) Handle(ctx context.Context, r slog.Record) error {
	var err error
	if s.inner.Enabled(ctx, r.Level) {
		err = s.inner.Handle(ctx, r)
	}
	if r.Level < slog.LevelInfo {
		return err
	}
	src := ""
	var b strings.Builder
	b.WriteString(r.Message)
	write := func(a slog.Attr) bool {
		if a.Key == "src" {
			src = a.Value.String()
			return true
		}
		b.WriteString(" ")
		if s.group != "" {
			b.WriteString(s.group + ".")
		}
		b.WriteString(a.Key)
		b.WriteString("=")
		b.WriteString(a.Value.String())
		return true
	}
	for _, a := range s.attrs {
		write(a)
	}
	r.Attrs(write)
	lvl := map[slog.Level]string{slog.LevelDebug: "debug", slog.LevelInfo: "info", slog.LevelWarn: "warn", slog.LevelError: "error"}[r.Level]
	if lvl == "" {
		lvl = strings.ToLower(r.Level.String())
	}
	e := LogEntry{TS: r.Time.UTC(), Level: lvl, Source: src, Msg: b.String()}
	s.mu.Lock()
	if len(*s.ring) >= ringSize {
		*s.ring = append((*s.ring)[:0], (*s.ring)[ringSize/4:]...)
	}
	*s.ring = append(*s.ring, e)
	*s.pending = append(*s.pending, e)
	s.mu.Unlock()
	if s.bus != nil {
		s.bus.Publish("log", e)
	}
	return err
}

func (s *LogSink) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := *s
	c.inner = s.inner.WithAttrs(attrs)
	c.attrs = append(append([]slog.Attr{}, s.attrs...), attrs...)
	return &c
}

func (s *LogSink) WithGroup(name string) slog.Handler {
	c := *s
	c.inner = s.inner.WithGroup(name)
	c.group = name
	return &c
}
