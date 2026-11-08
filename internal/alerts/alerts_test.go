package alerts

import (
	"bufio"
	"context"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"readynas/internal/store"
)

func newTest(t *testing.T) (*Service, *[]string, *sync.WaitGroup) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "state.json"))
	var sent []string
	var wg sync.WaitGroup
	var mu sync.Mutex
	s := NewService(st)
	s.Send = func(ctx context.Context, cfg store.SMTP, to []string, subject, body string) error {
		mu.Lock()
		sent = append(sent, subject)
		mu.Unlock()
		wg.Done()
		return nil
	}
	return s, &sent, &wg
}

func TestValidate(t *testing.T) {
	ok := store.AlertSettings{Emails: []string{"me@example.com", " "}, SMTP: store.SMTP{Host: "smtp.example.com"}}
	if err := Validate(&ok); err != nil || len(ok.Emails) != 1 || ok.SMTP.Port != 587 || ok.SMTP.Security != "starttls" {
		t.Fatalf("defaults: %v %+v", err, ok)
	}
	for label, cfg := range map[string]store.AlertSettings{
		"bad email":         {Emails: []string{"not-an-email"}, SMTP: store.SMTP{Host: "h"}},
		"header injection":  {Emails: []string{"a@b.c\r\nBcc: x@y.z"}, SMTP: store.SMTP{Host: "h"}},
		"too many":          {Emails: []string{"a@b.c", "b@b.c", "c@b.c", "d@b.c"}, SMTP: store.SMTP{Host: "h"}},
		"no server":         {Emails: []string{"a@b.c"}},
		"mandatory off":     {Disabled: []string{"raid_degraded"}},
		"silly temperature": {DiskTempLimit: 120},
	} {
		if err := Validate(&cfg); err == nil {
			t.Errorf("%s: expected an error", label)
		}
	}
}

func TestNotifyRespectsSwitches(t *testing.T) {
	s, sent, wg := newTest(t)
	s.Save(store.AlertSettings{Emails: []string{"me@example.com"}, SMTP: store.SMTP{Host: "smtp.example.com", Password: "pw"},
		Disabled: []string{"volume_full"}})

	wg.Add(1)
	s.Notify("raid_degraded", "Volume media is degraded", "details") // mandatory: emailed
	s.Notify("volume_full", "Volume media is 95% full", "details")   // switched off: recorded only
	wg.Wait()
	time.Sleep(50 * time.Millisecond) // let the background goroutine record the event

	if len(*sent) != 1 || !strings.Contains((*sent)[0], "ALERT: Volume media is degraded") {
		t.Errorf("sent: %v", *sent)
	}
	st := s.Store.Read()
	if len(st.Events) != 2 {
		t.Fatalf("both events must be recorded, got %d", len(st.Events))
	}
	if s.Settings().SMTP.Password != "" {
		t.Error("Settings must not return the SMTP password")
	}
	s.Save(store.AlertSettings{Emails: []string{"me@example.com"}, SMTP: store.SMTP{Host: "smtp.example.com"}})
	st = s.Store.Read()
	if st.Alerts.SMTP.Password != "pw" {
		t.Error("an empty password must keep the stored one")
	}
}

// A minimal SMTP server, enough to check what SendMail transmits.
func TestSendMail(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		w := func(s string) { conn.Write([]byte(s + "\r\n")) }
		w("220 test")
		var data strings.Builder
		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if inData {
				if line == ".\r\n" {
					inData = false
					w("250 ok")
					got <- data.String()
					continue
				}
				data.WriteString(line)
				continue
			}
			switch cmd := strings.ToUpper(strings.Fields(line)[0]); cmd {
			case "EHLO", "HELO":
				w("250 test")
			case "DATA":
				inData = true
				w("354 go")
			case "QUIT":
				w("221 bye")
				return
			default:
				w("250 ok")
			}
		}
	}()
	port, _ := strconv.Atoi(strings.Split(ln.Addr().String(), ":")[1])
	cfg := store.SMTP{Host: "127.0.0.1", Port: port, Security: "none", From: "nas@example.com"}
	if err := SendMail(context.Background(), cfg, []string{"me@example.com"}, "Déjà vu: disk failing", "line 1\nline 2\n"); err != nil {
		t.Fatal(err)
	}
	msg := <-got
	for _, want := range []string{"From: nas@example.com", "To: me@example.com", "Subject: =?utf-8?q?D=C3=A9j=C3=A0", "line 1\r\nline 2"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
}
