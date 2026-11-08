package shares

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// Share sizes: `du` reads every file's metadata, which takes minutes on a
// big share and an old Atom. So it runs in the background, every 6 hours,
// at the lowest CPU and disk priority (nice / ionice idle), and the UI shows
// the last result.

type ShareSize struct {
	Bytes   int64     `json:"bytes"`
	Updated time.Time `json:"updated"`
}

func (s *Service) RunSizes(ctx context.Context) {
	select { // let the NAS finish starting first
	case <-ctx.Done():
		return
	case <-time.After(2 * time.Minute):
	}
	for {
		for _, sh := range s.Store.Read().Shares {
			c, cancel := context.WithTimeout(ctx, time.Hour)
			out, err := s.Run(c, "nice", "-n", "19", "ionice", "-c", "3", "du", "-sxb", sh.Path)
			cancel()
			if f := strings.Fields(string(out)); err == nil && len(f) > 0 {
				n, _ := strconv.ParseInt(f[0], 10, 64)
				s.sizesMu.Lock()
				s.sizes[sh.Name] = ShareSize{Bytes: n, Updated: time.Now()}
				s.sizesMu.Unlock()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(6 * time.Hour):
		}
	}
}

func (s *Service) size(name string) *ShareSize {
	s.sizesMu.Lock()
	defer s.sizesMu.Unlock()
	if v, ok := s.sizes[name]; ok {
		return &v
	}
	return nil
}

// SMBSessions counts connected SMB sessions (smbstatus --json, Samba 4.16+).
func (s *Service) SMBSessions(ctx context.Context) int {
	out, err := s.Run(ctx, "smbstatus", "--json", "-b")
	if err != nil {
		return 0
	}
	var doc struct {
		Sessions map[string]json.RawMessage `json:"sessions"`
	}
	if json.Unmarshal(out, &doc) != nil {
		return 0
	}
	return len(doc.Sessions)
}
