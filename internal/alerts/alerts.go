// Package alerts records events (a disk failing, a volume filling up...) and
// emails them to up to three contacts.
//
// Events are split like on the original ReadyNAS: mandatory ones (data is
// at risk) always send an email; optional ones can be switched off.
package alerts

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"readynas/internal/store"
)

type Kind struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Severity  string `json:"severity"`
	Mandatory bool   `json:"mandatory"`
}

// Kinds is the catalogue of events nasd can raise.
var Kinds = []Kind{
	{"raid_failed", "A volume failed (too many disks lost)", "critical", true},
	{"raid_degraded", "A volume lost a disk (no redundancy left)", "critical", true},
	{"disk_failed", "A disk is failing (SMART)", "critical", true},
	{"fan_failed", "A fan stopped", "critical", true},
	{"ups_low_battery", "UPS battery low: the NAS shuts down", "critical", true},
	{"ups_on_battery", "Power cut: running on the UPS battery", "warning", false},
	{"ups_lost", "The UPS doesn't answer", "warning", false},
	{"disk_warning", "A disk has bad sectors", "warning", false},
	{"disk_hot", "A disk is too hot", "warning", false},
	{"system_hot", "A system temperature is too high", "warning", false},
	{"volume_full", "A volume is almost full", "warning", false},
	{"scrub_mismatch", "A volume check found inconsistencies", "warning", false},
	{"backup_failed", "A backup job failed", "warning", false},
	{"resolved", "A problem was resolved", "info", false},
	{"raid_rebuilt", "A volume finished rebuilding", "info", false},
	{"scrub_done", "A volume check finished", "info", false},
	{"usb_copy", "A USB disk was copied", "info", false},
	{"archive", "Cloud archive retrieval requested", "info", false},
	{"button", "Backup button jobs finished", "info", false},
	{"quota", "A user is near or at their quota", "warning", false},
	{"system_started", "The NAS started", "info", false},
	{"power_schedule", "Scheduled power off", "info", false},
	{"power_action", "Restart or shutdown requested", "info", false},
	{"admin_reset", "The admin password was reset at the NAS", "warning", true},
}

// Severity of an event kind ("critical", "warning" or "info").
func Severity(id string) string { return kind(id).Severity }

func kind(id string) Kind {
	for _, k := range Kinds {
		if k.ID == id {
			return k
		}
	}
	return Kind{ID: id, Label: id, Severity: "info"}
}

const (
	maxEvents       = 200
	DefaultDiskTemp = 50 // °C
	DefaultFullPct  = 90
)

type Service struct {
	Store *store.Store
	// Send delivers one email; replaced in tests.
	Send func(ctx context.Context, cfg store.SMTP, to []string, subject, body string) error
	Now  func() time.Time
}

func NewService(st *store.Store) *Service {
	return &Service{Store: st, Send: SendMail, Now: time.Now}
}

// Notify records an event and emails it (in the background: SMTP servers
// can take seconds to answer and the caller shouldn't wait).
func (s *Service) Notify(kindID, subject, body string) {
	k := kind(kindID)
	ev := store.Event{Time: s.Now(), Kind: k.ID, Severity: k.Severity, Subject: subject, Body: body}
	log.Printf("event %s: %s", k.ID, subject)
	st := s.Store.Read()
	cfg := st.Alerts
	wanted := len(cfg.Emails) > 0 && cfg.SMTP.Host != "" && (k.Mandatory || !slices.Contains(cfg.Disabled, k.ID))
	if !wanted {
		s.record(ev)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := s.Send(ctx, cfg.SMTP, cfg.Emails, subjectLine(ev), bodyText(ev))
		ev.Emailed = err == nil
		if err != nil {
			ev.EmailErr = err.Error()
			log.Printf("alert email failed: %v", err)
		}
		s.record(ev)
	}()
}

func (s *Service) record(ev store.Event) {
	s.Store.Update(func(st *store.State) error {
		st.Events = append(st.Events, ev)
		if len(st.Events) > maxEvents {
			st.Events = st.Events[len(st.Events)-maxEvents:]
		}
		return nil
	})
}

// Test sends a test email with the given settings (before saving them).
func (s *Service) Test(ctx context.Context, cfg store.AlertSettings) error {
	if err := Validate(&cfg); err != nil {
		return err
	}
	if len(cfg.Emails) == 0 {
		return errors.New("enter at least one email address")
	}
	if cfg.SMTP.Password == "" {
		st := s.Store.Read()
		cfg.SMTP.Password = st.Alerts.SMTP.Password
	}
	host, _ := os.Hostname()
	return s.Send(ctx, cfg.SMTP, cfg.Emails, "["+host+"] Test message",
		"This is a test message from your NAS "+host+".\n\nIf you can read this, alerts work.\n")
}

// Save stores alert settings. An empty SMTP password keeps the stored one
// (the UI never receives it).
func (s *Service) Save(cfg store.AlertSettings) error {
	if err := Validate(&cfg); err != nil {
		return err
	}
	return s.Store.Update(func(st *store.State) error {
		if cfg.SMTP.Password == "" {
			cfg.SMTP.Password = st.Alerts.SMTP.Password
		}
		st.Alerts = cfg
		return nil
	})
}

// Settings returns the settings without the password.
func (s *Service) Settings() store.AlertSettings {
	cfg := s.Store.Read().Alerts
	cfg.SMTP.Password = ""
	if cfg.Emails == nil {
		cfg.Emails = []string{}
	}
	if cfg.Disabled == nil {
		cfg.Disabled = []string{}
	}
	return cfg
}

func Validate(cfg *store.AlertSettings) error {
	var emails []string
	for _, e := range cfg.Emails {
		if e = strings.TrimSpace(e); e == "" {
			continue
		}
		if _, err := mail.ParseAddress(e); err != nil || strings.ContainsAny(e, "\r\n<>") {
			return fmt.Errorf("%q is not a valid email address", e)
		}
		emails = append(emails, e)
	}
	if len(emails) > 3 {
		return errors.New("at most 3 email addresses")
	}
	cfg.Emails = emails
	c := &cfg.SMTP
	c.Host = strings.TrimSpace(c.Host)
	if len(emails) > 0 && c.Host == "" {
		return errors.New("enter the outgoing mail server (SMTP)")
	}
	if strings.ContainsAny(c.Host+c.User+c.From, "\r\n") {
		return errors.New("invalid characters in the mail settings")
	}
	if c.Security == "" {
		c.Security = "starttls"
	}
	if !slices.Contains([]string{"starttls", "tls", "none"}, c.Security) {
		return errors.New("security must be starttls, tls or none")
	}
	if c.Port == 0 {
		c.Port = map[string]int{"starttls": 587, "tls": 465, "none": 25}[c.Security]
	}
	if c.Port < 1 || c.Port > 65535 {
		return errors.New("invalid port")
	}
	if c.From != "" {
		if _, err := mail.ParseAddress(c.From); err != nil {
			return fmt.Errorf("%q is not a valid sender address", c.From)
		}
	}
	if cfg.DiskTempLimit != 0 && (cfg.DiskTempLimit < 30 || cfg.DiskTempLimit > 80) {
		return errors.New("disk temperature limit must be between 30 and 80 °C")
	}
	if cfg.VolumeFullPercent != 0 && (cfg.VolumeFullPercent < 50 || cfg.VolumeFullPercent > 99) {
		return errors.New("volume full threshold must be between 50 and 99%")
	}
	for _, d := range cfg.Disabled {
		if kind(d).Mandatory {
			return fmt.Errorf("%s alerts can't be switched off", d)
		}
	}
	return nil
}

func subjectLine(ev store.Event) string {
	host, _ := os.Hostname()
	prefix := map[string]string{"critical": "ALERT", "warning": "Warning", "info": "Info"}[ev.Severity]
	return fmt.Sprintf("[%s] %s: %s", host, prefix, ev.Subject)
}

func bodyText(ev store.Event) string {
	host, _ := os.Hostname()
	return fmt.Sprintf("%s\n\n%s\n\nTime: %s\nNAS: %s (https://%s.local:8443)\n",
		ev.Subject, ev.Body, ev.Time.Format("2006-01-02 15:04:05 MST"), host, host)
}

// ----------------------------------------------------------- SMTP

// SendMail sends a plain-text email. Three ways to talk to a mail server:
//   - "tls": encrypted from the first byte (port 465, "SSL/TLS"),
//   - "starttls": starts in clear text, then upgrades (port 587),
//   - "none": no encryption (only for a server on your own network).
func SendMail(ctx context.Context, cfg store.SMTP, to []string, subject, body string) error {
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	var conn net.Conn
	var err error
	if cfg.Security == "tls" {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: &tls.Config{ServerName: cfg.Host}}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("cannot reach %s: %w", addr, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	host, _ := os.Hostname()
	if err := c.Hello(host); err != nil {
		return err
	}
	if cfg.Security == "starttls" {
		if err := c.StartTLS(&tls.Config{ServerName: cfg.Host}); err != nil {
			return fmt.Errorf("STARTTLS failed: %w", err)
		}
	}
	if cfg.User != "" {
		// net/smtp refuses to send a password over an unencrypted
		// connection (except to localhost): a good default.
		if err := c.Auth(smtp.PlainAuth("", cfg.User, cfg.Password, cfg.Host)); err != nil {
			return fmt.Errorf("login refused by the mail server: %w", err)
		}
	}
	from := cfg.From
	if from == "" {
		from = cfg.User
	}
	if from == "" {
		from = "nas@" + host
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return fmt.Errorf("recipient %s refused: %w", rcpt, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	msg := "From: " + from + "\r\n" +
		"To: " + strings.Join(to, ", ") + "\r\n" +
		"Subject: " + mime.QEncoding.Encode("utf-8", subject) + "\r\n" +
		"Date: " + time.Now().Format(time.RFC1123Z) + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: 8bit\r\n\r\n" +
		strings.ReplaceAll(body, "\n", "\r\n")
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
