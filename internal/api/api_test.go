package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"readynas/internal/alerts"
	"readynas/internal/auth"
	"readynas/internal/disks"
	"readynas/internal/jobs"
	"readynas/internal/shares"
	"readynas/internal/store"
	"readynas/internal/volumes"
)

func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	fake := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "lsblk" {
			return []byte(`{"blockdevices":[{"name":"sda","path":"/dev/sda","size":100,"type":"disk","mountpoints":[null]}]}`), nil
		}
		return []byte(`{"smart_status":{"passed":true}}`), nil
	}
	s := &Server{
		Store:    st,
		Disks:    disks.NewService(fake),
		Volumes:  &volumes.Service{Sys: t.TempDir(), Fstab: "/nonexistent", MdadmConf: "/nonexistent", MountRoot: t.TempDir(), Run: fake},
		Jobs:     jobs.NewManager(fake),
		Shares:   &shares.Service{Store: st},
		Alerts:   alerts.NewService(st),
		Sessions: auth.NewSessions(time.Minute),
		Throttle: auth.NewThrottle(3, time.Minute),
		UI:       fstest.MapFS{"index.html": {Data: []byte("<h1>ui</h1>")}},
	}
	return s.Handler()
}

func do(h http.Handler, method, path, body, cookie string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("X-Requested-With", "nasd")
	if cookie != "" {
		r.Header.Set("Cookie", cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestSetupLoginFlow(t *testing.T) {
	h := newTestServer(t)

	if w := do(h, "GET", "/api/disks", "", ""); w.Code != 401 {
		t.Fatalf("disks without login: %d", w.Code)
	}
	if w := do(h, "POST", "/api/setup", `{"password":"short"}`, ""); w.Code != 400 {
		t.Fatalf("short password accepted: %d", w.Code)
	}
	w := do(h, "POST", "/api/setup", `{"password":"correct horse"}`, "")
	if w.Code != 200 {
		t.Fatalf("setup: %d %s", w.Code, w.Body)
	}
	cookie := strings.Split(w.Header().Get("Set-Cookie"), ";")[0]

	if w := do(h, "POST", "/api/setup", `{"password":"another one"}`, ""); w.Code != 409 {
		t.Fatalf("second setup must be refused: %d", w.Code)
	}
	if w := do(h, "GET", "/api/disks", "", cookie); w.Code != 200 || !strings.Contains(w.Body.String(), "/dev/sda") {
		t.Fatalf("disks: %d %s", w.Code, w.Body)
	}
	if w := do(h, "GET", "/api/me", "", cookie); !strings.Contains(w.Body.String(), `"admin"`) {
		t.Fatalf("me: %s", w.Body)
	}
	if w := do(h, "POST", "/api/login", `{"username":"admin","password":"correct horse"}`, ""); w.Code != 200 {
		t.Fatalf("login: %d", w.Code)
	}
}

func TestLoginThrottle(t *testing.T) {
	h := newTestServer(t)
	do(h, "POST", "/api/setup", `{"password":"correct horse"}`, "")
	for i := 0; i < 3; i++ {
		do(h, "POST", "/api/login", `{"username":"admin","password":"nope"}`, "")
	}
	if w := do(h, "POST", "/api/login", `{"username":"admin","password":"correct horse"}`, ""); w.Code != 429 {
		t.Fatalf("want 429 after 3 failures, got %d", w.Code)
	}
}

func TestCSRFGuard(t *testing.T) {
	h := newTestServer(t)
	r := httptest.NewRequest("POST", "/api/setup", strings.NewReader("password=x"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 415 {
		t.Fatalf("form post must be rejected, got %d", w.Code)
	}
}

func TestSPAFallback(t *testing.T) {
	h := newTestServer(t)
	if w := do(h, "GET", "/disks", "", ""); !strings.Contains(w.Body.String(), "<h1>ui</h1>") {
		t.Fatalf("unknown route should serve index.html: %s", w.Body)
	}
}

func TestRandomPassword(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		pw := randomPassword()
		if len(pw) != 11 || pw[5] != '-' || len(pw) < minPasswordLength {
			t.Fatalf("bad format %q", pw)
		}
		for _, c := range strings.ReplaceAll(pw, "-", "") {
			if strings.ContainsRune("01ilo", c) {
				t.Fatalf("look-alike character in %q", pw)
			}
		}
		seen[pw] = true
	}
	if len(seen) < 50 {
		t.Fatal("passwords repeat")
	}
}
