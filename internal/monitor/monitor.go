// Package monitor samples host and platform metrics every 2 seconds, keeps
// one hour in memory for live charts and persists per-minute averages.
package monitor

import (
	"bufio"
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"meshport/internal/events"
	"meshport/internal/store"
)

const (
	Interval = 2 * time.Second
	ringLen  = 1800 // 1 hour at 2 s
	keepDays = 8
)

// Source supplies platform gauges (implemented by the server wiring).
type Source func() map[string]float64

type Point struct {
	T int64   `json:"t"`
	V float64 `json:"v"`
}

type Monitor struct {
	db      *store.DB
	bus     *events.Bus
	dataDir string
	source  Source

	mu      sync.RWMutex
	ring    map[string][]Point
	minute  map[string][]float64
	last    map[string]float64
	prevCPU [2]uint64 // idle, total
	prevNet [2]uint64 // rx, tx bytes
	prevAt  time.Time
	started time.Time
}

func New(db *store.DB, bus *events.Bus, dataDir string, src Source) *Monitor {
	return &Monitor{db: db, bus: bus, dataDir: dataDir, source: src,
		ring: map[string][]Point{}, minute: map[string][]float64{}, last: map[string]float64{}, started: time.Now()}
}

func (m *Monitor) Uptime() time.Duration { return time.Since(m.started) }

// Run samples until ctx is cancelled.
func (m *Monitor) Run(ctx context.Context) {
	t := time.NewTicker(Interval)
	defer t.Stop()
	m.sample()
	curMinute := time.Now().Unix() / 60
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			snap := m.sample()
			m.bus.Publish("metrics.tick", snap)
			if mn := now.Unix() / 60; mn != curMinute {
				m.flushMinute(curMinute * 60)
				curMinute = mn
			}
		}
	}
}

// Latest returns the most recent value of every metric.
func (m *Monitor) Latest() map[string]float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]float64, len(m.last))
	for k, v := range m.last {
		out[k] = v
	}
	return out
}

func (m *Monitor) sample() map[string]float64 {
	now := time.Now()
	vals := map[string]float64{}
	if idle, total, ok := readCPU(); ok {
		if m.prevCPU[1] > 0 && total > m.prevCPU[1] {
			busy := 1 - float64(idle-m.prevCPU[0])/float64(total-m.prevCPU[1])
			vals["cpu"] = round1(busy * 100)
		}
		m.prevCPU = [2]uint64{idle, total}
	}
	if used, total, ok := readMem(); ok && total > 0 {
		vals["mem"] = round1(float64(used) / float64(total) * 100)
		vals["mem_used_mb"] = float64(used / 1024 / 1024)
		vals["mem_total_mb"] = float64(total / 1024 / 1024)
	}
	if rx, tx, ok := readNet(); ok {
		if !m.prevAt.IsZero() && rx >= m.prevNet[0] && tx >= m.prevNet[1] {
			dt := now.Sub(m.prevAt).Seconds()
			vals["net_rx_bps"] = float64(rx-m.prevNet[0]) / dt
			vals["net_tx_bps"] = float64(tx-m.prevNet[1]) / dt
		}
		m.prevNet = [2]uint64{rx, tx}
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(m.dataDir, &st); err == nil && st.Blocks > 0 {
		total := float64(st.Blocks) * float64(st.Bsize)
		free := float64(st.Bavail) * float64(st.Bsize)
		vals["disk"] = round1((total - free) / total * 100)
		vals["disk_free_gb"] = round1(free / 1e9)
	}
	if l, ok := readLoad(); ok {
		vals["load1"] = l
	}
	m.prevAt = now
	if m.source != nil {
		for k, v := range m.source() {
			vals[k] = v
		}
	}
	m.mu.Lock()
	ts := now.Unix()
	for k, v := range vals {
		r := m.ring[k]
		if len(r) >= ringLen {
			r = append(r[:0], r[len(r)-ringLen+1:]...)
		}
		m.ring[k] = append(r, Point{ts, v})
		m.minute[k] = append(m.minute[k], v)
		m.last[k] = v
	}
	m.mu.Unlock()
	return vals
}

func (m *Monitor) flushMinute(ts int64) {
	m.mu.Lock()
	agg := m.minute
	m.minute = map[string][]float64{}
	m.mu.Unlock()
	tx, err := m.db.Begin()
	if err != nil {
		return
	}
	for k, vs := range agg {
		if len(vs) == 0 {
			continue
		}
		var sum float64
		for _, v := range vs {
			sum += v
		}
		_, _ = tx.Exec(`INSERT OR REPLACE INTO metrics (ts, key, value) VALUES (?,?,?)`, ts, k, round1(sum/float64(len(vs))))
	}
	_, _ = tx.Exec(`DELETE FROM metrics WHERE ts < ?`, time.Now().Add(-keepDays*24*time.Hour).Unix())
	_ = tx.Commit()
}

// Series returns points for a key over a range ("live" = last 5 min at 2 s,
// "1h" at 2 s from memory, "24h"/"7d" from per-minute history), downsampled
// to at most maxPoints buckets.
func (m *Monitor) Series(key, rng string, maxPoints int) []Point {
	if maxPoints <= 0 {
		maxPoints = 120
	}
	now := time.Now()
	var pts []Point
	switch rng {
	case "live", "5m", "1h", "":
		since := now.Add(-time.Hour).Unix()
		if rng == "live" || rng == "5m" {
			since = now.Add(-5 * time.Minute).Unix()
		}
		m.mu.RLock()
		for _, p := range m.ring[key] {
			if p.T >= since {
				pts = append(pts, p)
			}
		}
		m.mu.RUnlock()
	default:
		d := 24 * time.Hour
		if rng == "7d" {
			d = 7 * 24 * time.Hour
		}
		rows, err := m.db.Query(`SELECT ts, value FROM metrics WHERE key=? AND ts>=? ORDER BY ts`, key, now.Add(-d).Unix())
		if err != nil {
			return nil
		}
		for rows.Next() {
			var p Point
			if rows.Scan(&p.T, &p.V) == nil {
				pts = append(pts, p)
			}
		}
		rows.Close()
	}
	return downsample(pts, maxPoints)
}

func downsample(pts []Point, n int) []Point {
	if len(pts) <= n {
		if pts == nil {
			return []Point{}
		}
		return pts
	}
	out := make([]Point, 0, n)
	size := float64(len(pts)) / float64(n)
	for i := 0; i < n; i++ {
		a, b := int(float64(i)*size), int(float64(i+1)*size)
		if b > len(pts) {
			b = len(pts)
		}
		var sum float64
		for _, p := range pts[a:b] {
			sum += p.V
		}
		out = append(out, Point{T: pts[b-1].T, V: round1(sum / float64(b-a))})
	}
	return out
}

func round1(v float64) float64 { return float64(int64(v*10+0.5)) / 10 }

// ---------- /proc readers ----------

func readCPU() (idle, total uint64, ok bool) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return
	}
	fields := strings.Fields(sc.Text())
	if len(fields) < 5 || fields[0] != "cpu" {
		return
	}
	for i, s := range fields[1:] {
		v, _ := strconv.ParseUint(s, 10, 64)
		total += v
		if i == 3 || i == 4 { // idle + iowait
			idle += v
		}
	}
	return idle, total, true
}

func readMem() (used, total uint64, ok bool) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return
	}
	defer f.Close()
	var avail uint64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, _ := strings.Cut(sc.Text(), ":")
		n, _ := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
		switch k {
		case "MemTotal":
			total = n * 1024
		case "MemAvailable":
			avail = n * 1024
		}
	}
	if total == 0 {
		return
	}
	return total - avail, total, true
}

func readNet() (rx, tx uint64, ok bool) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		name, rest, found := strings.Cut(sc.Text(), ":")
		if !found {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "lo" {
			continue
		}
		fs := strings.Fields(rest)
		if len(fs) < 9 {
			continue
		}
		r, _ := strconv.ParseUint(fs[0], 10, 64)
		t, _ := strconv.ParseUint(fs[8], 10, 64)
		rx += r
		tx += t
		ok = true
	}
	return
}

func readLoad() (float64, bool) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, false
	}
	fs := strings.Fields(string(b))
	if len(fs) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fs[0], 64)
	return v, err == nil
}
