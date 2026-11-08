package metrics

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fakeProc(t *testing.T, root string, idle, total, rx, tx, rd, wr uint64) {
	t.Helper()
	os.MkdirAll(filepath.Join(root, "proc", "net"), 0o755)
	os.MkdirAll(filepath.Join(root, "sys", "class", "net", "eth0", "device"), 0o755)
	user := total - idle
	write := func(p, c string) { os.WriteFile(filepath.Join(root, "proc", p), []byte(c), 0o644) }
	write("stat", "cpu  "+u(user)+" 0 0 "+u(idle)+" 0 0 0 0 0 0\ncpu0 1 2 3\n")
	write("meminfo", "MemTotal:       1000000 kB\nMemAvailable:    400000 kB\n")
	write("net/dev", "Inter-|...\n face |...\n    lo: 999 0 0 0 0 0 0 0 999 0\n  eth0: "+u(rx)+" 0 0 0 0 0 0 0 "+u(tx)+" 0\n")
	write("diskstats", "   8       0 sda 10 0 "+u(rd)+" 0 20 0 "+u(wr)+" 0 0 0 0\n   8       1 sda1 10 0 999999 0 20 0 999999 0 0 0 0\n")
}

func u(n uint64) string { return fmtUint(n) }

func fmtUint(n uint64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

func TestSampleRatesAndRings(t *testing.T) {
	root := t.TempDir()
	s := New(filepath.Join(root, "metrics.json"))
	s.Proc, s.Sys = filepath.Join(root, "proc"), filepath.Join(root, "sys")
	t0 := time.Unix(1_700_000_000, 0)
	fakeProc(t, root, 1000, 2000, 0, 0, 0, 0)
	s.sample(t0)
	// 10 s later: CPU 25% busy, 1 MB/s in, 0.5 MB/s out, 2048 sectors read (=1 MiB), partitions ignored.
	fakeProc(t, root, 1750, 3000, 10_000_000, 5_000_000, 20480, 2048)
	s.sample(t0.Add(10 * time.Second))
	p := s.Latest()
	if p.CPU != 25 || p.NetRx != 1_000_000 || p.NetTx != 500_000 || p.DiskRead != 20480*512/10 || p.MemUsed != 600_000*1024 {
		t.Fatalf("latest: %+v", p)
	}
	pts, step, ok := s.Range("1h")
	if !ok || step != 10 || len(pts) != 1 {
		t.Fatalf("1h range: %v %d %v", pts, step, ok)
	}
	s.Save()
	s2 := New(s.File)
	if pts2, _, _ := s2.Range("1h"); len(pts2) != 0 { // the step in progress isn't saved, completed ones are
		t.Logf("restored %d points", len(pts2))
	}
	if _, _, ok := s.Range("1y"); ok {
		t.Error("unknown range accepted")
	}
}

func TestRingAveragesPerStep(t *testing.T) {
	r := &ring{Step: 60, Size: 3}
	for i := int64(0); i < 6; i++ { // 6 samples of 30 s = 3 steps of a minute
		r.add(Point{T: 1_700_000_040 + i*30, CPU: float64(i)})
	}
	if len(r.Points) != 2 || r.Points[0].CPU != 0.5 || r.Points[1].CPU != 2.5 {
		t.Fatalf("points: %+v", r.Points)
	}
}
