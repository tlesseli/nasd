// Package system reads basic machine stats straight from /proc, the
// kernel's virtual filesystem: no tool to call, just text files to parse.
package system

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

type Info struct {
	Hostname      string    `json:"hostname"`
	Kernel        string    `json:"kernel"`
	CPU           string    `json:"cpu"`
	UptimeSeconds int64     `json:"uptimeSeconds"`
	Load          []float64 `json:"load"` // 1, 5, 15 min averages
	MemTotalBytes int64     `json:"memTotalBytes"`
	MemAvailBytes int64     `json:"memAvailableBytes"`
}

func Read() Info {
	var info Info
	info.Hostname, _ = os.Hostname()
	info.Kernel = firstLine("/proc/sys/kernel/osrelease")

	if f := strings.Fields(firstLine("/proc/uptime")); len(f) > 0 {
		up, _ := strconv.ParseFloat(f[0], 64)
		info.UptimeSeconds = int64(up)
	}
	if f := strings.Fields(firstLine("/proc/loadavg")); len(f) >= 3 {
		for _, s := range f[:3] {
			v, _ := strconv.ParseFloat(s, 64)
			info.Load = append(info.Load, v)
		}
	}
	eachLine("/proc/meminfo", func(l string) {
		f := strings.Fields(l) // "MemTotal:  1015000 kB"
		if len(f) < 2 {
			return
		}
		kb, _ := strconv.ParseInt(f[1], 10, 64)
		switch f[0] {
		case "MemTotal:":
			info.MemTotalBytes = kb * 1024
		case "MemAvailable:":
			info.MemAvailBytes = kb * 1024
		}
	})
	eachLine("/proc/cpuinfo", func(l string) {
		if info.CPU == "" && strings.HasPrefix(l, "model name") {
			if _, v, ok := strings.Cut(l, ":"); ok {
				info.CPU = strings.TrimSpace(v)
			}
		}
	})
	return info
}

func firstLine(path string) string {
	data, _ := os.ReadFile(path)
	line, _, _ := strings.Cut(string(data), "\n")
	return strings.TrimSpace(line)
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
