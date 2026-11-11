package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTest(t *testing.T, dir string, host string, ips ...string) *Manager {
	m := &Manager{Dir: dir, Hostname: func() string { return host }, Now: time.Now}
	m.IPs = func() []net.IP {
		var out []net.IP
		for _, s := range ips {
			out = append(out, net.ParseIP(s))
		}
		return out
	}
	if err := m.Refresh(); err != nil {
		t.Fatal(err)
	}
	return m
}

func verify(m *Manager, name string) error {
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(m.CAPEM())
	_, err := m.leaf.Load().Verify(x509.VerifyOptions{Roots: pool, DNSName: name})
	return err
}

func TestTrustedByItsCA(t *testing.T) {
	dir := t.TempDir()
	m := newTest(t, dir, "nas", "192.168.0.6", "2a02:a03f::5", "fe80::1")
	for _, name := range []string{"nas", "nas.local", "localhost", "192.168.0.6", "127.0.0.1", "fe80::1"} {
		if err := verify(m, name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A public address is left out (the CA can't vouch for it).
	if err := verify(m, "2a02:a03f::5"); err == nil {
		t.Error("public address in the certificate")
	}
	if fi, _ := os.Stat(filepath.Join(dir, "ca.key")); fi.Mode().Perm() != 0o600 {
		t.Errorf("ca.key mode %v", fi.Mode().Perm())
	}

	// Same files at the next start: same CA, same certificate.
	fp, expires := m.Info().Fingerprint, m.Info().Expires
	m2 := newTest(t, dir, "nas", "192.168.0.6")
	if m2.Info().Fingerprint != fp || !m2.Info().Expires.Equal(expires) {
		t.Error("remade at restart")
	}
	// A new address: new certificate, same CA.
	m3 := newTest(t, dir, "nas", "192.168.1.20")
	if m3.Info().Fingerprint != fp || verify(m3, "192.168.1.20") != nil {
		t.Error("new address not covered")
	}
	// Renamed: a new CA (the old one names the NAS).
	m4 := newTest(t, dir, "vault", "192.168.1.20")
	if m4.Info().Fingerprint == fp || verify(m4, "vault.local") != nil {
		t.Error("rename")
	}
}

// The CA can't be used for anything but this NAS, even with its key.
func TestNameConstraints(t *testing.T) {
	m := newTest(t, t.TempDir(), "nas", "192.168.0.6")
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(m.CAPEM())
	for _, c := range []struct {
		dns string
		ip  string
	}{{"www.bank.example", ""}, {"nas.evil.example", ""}, {"", "8.8.8.8"}} {
		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		tmpl := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: "x"},
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
			ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		name := c.dns
		if c.dns != "" {
			tmpl.DNSNames = []string{c.dns}
		} else {
			tmpl.IPAddresses = []net.IP{net.ParseIP(c.ip)}
			name = c.ip
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, m.ca, &key.PublicKey, m.caKey)
		if err != nil {
			t.Fatal(err)
		}
		forged, _ := x509.ParseCertificate(der)
		if _, err := forged.Verify(x509.VerifyOptions{Roots: pool, DNSName: name}); err == nil {
			t.Errorf("the CA vouched for %s", name)
		}
	}
}

// The self-signed certificate of older versions is replaced.
func TestReplacesSelfSigned(t *testing.T) {
	dir := t.TempDir()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: "nas"}, DNSNames: []string{"nas"},
		NotBefore: time.Now(), NotAfter: time.Now().AddDate(10, 0, 0)}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	os.WriteFile(filepath.Join(dir, "tls.crt"), pemCert(der), 0o644)
	keyDER, _ := x509.MarshalECPrivateKey(key)
	os.WriteFile(filepath.Join(dir, "tls.key"), pemKey(keyDER), 0o600)

	m := newTest(t, dir, "nas", "192.168.0.6")
	if err := verify(m, "nas"); err != nil {
		t.Error(err)
	}
	if m.leaf.Load().NotAfter.After(time.Now().AddDate(0, 0, leafDays+1)) {
		t.Error("old 10-year certificate kept")
	}
}

func TestRenewsBeforeExpiry(t *testing.T) {
	dir := t.TempDir()
	m := newTest(t, dir, "nas", "192.168.0.6")
	first := m.leaf.Load().SerialNumber
	m.Now = func() time.Time { return time.Now().AddDate(0, 0, leafDays-renewDays+1) }
	if err := m.Refresh(); err != nil {
		t.Fatal(err)
	}
	if m.leaf.Load().SerialNumber.Cmp(first) == 0 {
		t.Error("not renewed a month before expiry")
	}
}

func pemCert(der []byte) []byte { return pemBlock("CERTIFICATE", der) }
func pemKey(der []byte) []byte  { return pemBlock("EC PRIVATE KEY", der) }
func pemBlock(typ string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
}
