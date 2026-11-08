// Package metrics samples CPU, memory, network and disk throughput every
// 10 seconds from /proc, and keeps a history at four resolutions for the
// Overview charts:
//
//	1h  -> 360 points of 10 s      24h -> 1440 points of 1 min
//	7d  -> 1008 points of 10 min   30d -> 720 points of 1 h
//
// Each coarser ring stores the average of the samples in its step. Storage
// used per volume is recorded hourly for 90 days (growth and "full in…").
// Everything is saved to a JSON file every 10 minutes, so a restart keeps
// the charts.
package metrics

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Point is one sample (rates are per second).
type Point struct {
	T         int64   `json:"t"`        // unix seconds
	CPU       float64 `json:"cpu"`      // %
	MemUsed   float64 `json:"memUsed"`  // bytes
	NetRx     float64 `json:"netRx"`    // bytes/s
	NetTx     float64 `json:"netTx"`    // bytes/s
	DiskRead  float64 `json:"diskRead"` // bytes/s
	DiskWrite float64 `json:"diskWrite"`
}

type ring struct {
	Step   int64   `json:"step"` // seconds
	Size   int     `json:"size"`
	Points []Point `json:"points"`
	acc    []Point // samples of the step in progress
}

func (r *ring) add(p Point) {
	if len(r.acc) > 0 && p.T/r.Step != r.acc[0].T/r.Step {
		avg := average(r.acc)
		avg.T = (r.acc[0].T / r.Step) * r.Step
		r.Points = append(r.Points, avg)
		if len(r.Points) > r.Size {
			r.Points = r.Points[len(r.Points)-r.Size:]
		}
		r.acc = r.acc[:0]
	}
	r.acc = append(r.acc, p)
}

func average(ps []Point) Point {
	var a Point
	for _, p := range ps {
		a.CPU += p.CPU
		a.MemUsed += p.MemUsed
		a.NetRx += p.NetRx
		a.NetTx += p.NetTx
		a.DiskRead += p.DiskRead
		a.DiskWrite += p.DiskWrite
	}
	n := float64(len(ps))
	a.CPU, a.MemUsed, a.NetRx, a.NetTx, a.DiskRead, a.DiskWrite = a.CPU/n, a.MemUsed/n, a.NetRx/n, a.NetTx/n, a.DiskRead/n, a.DiskWrite/n
	return a
}

// StoragePoint: bytes used per volume at a time.
type StoragePoint struct {
	T    int64            `json:"t"`
	Used map[string]int64 `json:"used"`
}

type Sampler struct {
	Proc    string // /proc
	Sys     string // /sys
	File    string // where the history is saved
	Storage func(ctx context.Context) map[string]int64

	mu      sync.Mutex
	Rings   map[string]*ring `json:"rings"`
	History []StoragePoint   `json:"storage"`
	prev    counters
	hasPrev bool
	latest  Point
}

var Ranges = map[string][2]int64{ // name -> step seconds, points
	"1h": {10, 360}, "24h": {60, 1440}, "7d": {600, 1008}, "30d": {3600, 720},
}

func New(file string) *Sampler {
	s := &Sampler{Proc: "/proc", Sys: "/sys", File: file, Rings: map[string]*ring{}}
	for name, r := range Ranges {
		s.Rings[name] = &ring{Step: r[0], Size: int(r[1])}
	}
	if data, err := os.ReadFile(file); err == nil {
		var saved struct {
			Rings   map[string]*ring `json:"rings"`
			History []StoragePoint   `json:"storage"`
		}
		if json.Unmarshal(data, &saved) == nil {
			for name, r := range saved.Rings {
				if cur, ok := s.Rings[name]; ok && r != nil {
					cur.Points = r.Points
				}
			}
			s.History = saved.History
		}
	}
	return s
}

func (s *Sampler) Run(ctx context.Context) {
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	lastSave, lastStorage := time.Now(), time.Time{}
	for {
		now := time.Now()
		s.sample(now)
		if s.Storage != nil && now.Sub(lastStorage) >= time.Hour {
			s.recordStorage(now, s.Storage(ctx))
			lastStorage = now
		}
		if now.Sub(lastSave) >= 10*time.Minute {
			s.Save()
			lastSave = now
		}
		select {
		case <-ctx.Done():
			s.Save()
			return
		case <-tick.C:
		}
	}
}

func (s *Sampler) recordStorage(now time.Time, used map[string]int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.History = append(s.History, StoragePoint{T: now.Unix(), Used: used})
	if len(s.History) > 90*24 {
		s.History = s.History[len(s.History)-90*24:]
	}
}

type counters struct {
	t                     time.Time
	cpuIdle, cpuTotal     uint64
	netRx, netTx          uint64
	diskRead, diskWritten uint64 // sectors (512 bytes)
}

func (s *Sampler) sample(now time.Time) {
	c := s.read(now)
	mem := s.memUsed()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasPrev {
		s.prev, s.hasPrev = c, true
		return
	}
	dt := c.t.Sub(s.prev.t).Seconds()
	if dt <= 0 {
		return
	}
	p := Point{T: now.Unix(), MemUsed: mem}
	if dTotal := float64(c.cpuTotal - s.prev.cpuTotal); dTotal > 0 {
		p.CPU = 100 * (1 - float64(c.cpuIdle-s.prev.cpuIdle)/dTotal)
	}
	rate := func(cur, prev uint64, scale float64) float64 {
		if cur < prev { // counter reset (interface or disk gone)
			return 0
		}
		return float64(cur-prev) * scale / dt
	}
	p.NetRx, p.NetTx = rate(c.netRx, s.prev.netRx, 1), rate(c.netTx, s.prev.netTx, 1)
	p.DiskRead, p.DiskWrite = rate(c.diskRead, s.prev.diskRead, 512), rate(c.diskWritten, s.prev.diskWritten, 512)
	s.prev, s.latest = c, p
	for _, r := range s.Rings {
		r.add(p)
	}
}

var wholeDisk = regexp.MustCompile(`^(sd[a-z]+|nvme[0-9]+n[0-9]+)$`)

func (s *Sampler) read(now time.Time) counters {
	c := counters{t: now}
	if f := strings.Fields(firstLine(filepath.Join(s.Proc, "stat"))); len(f) > 5 && f[0] == "cpu" {
		for i, v := range f[1:] {
			n, _ := strconv.ParseUint(v, 10, 64)
			if i < 8 { // user nice system idle iowait irq softirq steal (guest is counted in user)
				c.cpuTotal += n
			}
			if i == 3 || i == 4 { // idle + iowait
				c.cpuIdle += n
			}
		}
	}
	eachLine(filepath.Join(s.Proc, "net", "dev"), func(l string) {
		name, rest, ok := strings.Cut(l, ":")
		name = strings.TrimSpace(name)
		if !ok || !s.physicalNIC(name) {
			return
		}
		if f := strings.Fields(rest); len(f) >= 9 {
			rx, _ := strconv.ParseUint(f[0], 10, 64)
			tx, _ := strconv.ParseUint(f[8], 10, 64)
			c.netRx += rx
			c.netTx += tx
		}
	})
	eachLine(filepath.Join(s.Proc, "diskstats"), func(l string) {
		f := strings.Fields(l)
		if len(f) >= 10 && wholeDisk.MatchString(f[2]) {
			r, _ := strconv.ParseUint(f[5], 10, 64)
			w, _ := strconv.ParseUint(f[9], 10, 64)
			c.diskRead += r
			c.diskWritten += w
		}
	})
	return c
}

func (s *Sampler) physicalNIC(name string) bool {
	_, err := os.Stat(filepath.Join(s.Sys, "class", "net", name, "device"))
	return err == nil
}

func (s *Sampler) memUsed() float64 {
	var total, avail float64
	eachLine(filepath.Join(s.Proc, "meminfo"), func(l string) {
		f := strings.Fields(l)
		if len(f) < 2 {
			return
		}
		v, _ := strconv.ParseFloat(f[1], 64)
		switch f[0] {
		case "MemTotal:":
			total = v * 1024
		case "MemAvailable:":
			avail = v * 1024
		}
	})
	return total - avail
}

// Range returns the points of one range ("1h", "24h", "7d", "30d").
func (s *Sampler) Range(name string) ([]Point, int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.Rings[name]
	if !ok {
		return nil, 0, false
	}
	out := append([]Point{}, r.Points...)
	if len(r.acc) > 0 { // include the step in progress, so new data shows at once
		avg := average(r.acc)
		avg.T = (r.acc[0].T / r.Step) * r.Step
		out = append(out, avg)
	}
	return out, r.Step, true
}

func (s *Sampler) Latest() Point {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.latest
}

func (s *Sampler) StorageHistory() []StoragePoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]StoragePoint{}, s.History...)
}

func (s *Sampler) Save() {
	s.mu.Lock()
	data, err := json.Marshal(map[string]any{"rings": s.Rings, "storage": s.History})
	s.mu.Unlock()
	if err != nil || s.File == "" {
		return
	}
	tmp := s.File + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		os.Rename(tmp, s.File)
	}
}

func firstLine(path string) string {
	b, _ := os.ReadFile(path)
	l, _, _ := strings.Cut(string(b), "\n")
	return l
}

func eachLine(path string, fn func(string)) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fn(sc.Text())
	}
}
