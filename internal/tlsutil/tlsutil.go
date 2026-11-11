// Package tlsutil gives the web interface (and FTPS) its HTTPS certificate.
//
// Each NAS makes its own small certificate authority (CA) on first start.
// Installed once on a computer (System > HTTPS certificate), it makes the
// browser trust the NAS with no warning. That matters: people who click
// through a warning every day also click through the one an attacker on
// the network would cause, and give away the admin password.
//
// The CA can only vouch for this NAS: its name constraints allow the NAS's
// own names and private (LAN) addresses only. Even if its key leaked, it
// couldn't be used to impersonate a bank or any public site to a computer
// that trusts it.
//
// The server certificate it signs lasts 397 days (what Apple devices
// accept) and is renewed automatically: a month before it expires, or when
// the NAS's name or addresses change. The CA itself lasts 20 years; it is
// only remade when the NAS is renamed (its constraints name the NAS).
//
// Files, in the data directory: ca.crt, ca.key (the CA), tls.crt (server
// certificate followed by the CA), tls.key.
package tlsutil

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	leafDays  = 397
	renewDays = 30
)

// Networks the CA may vouch for: loopback, private ranges, link-local,
// Tailscale/CGNAT and IPv6 unique local addresses.
var permittedNets = mustNets("127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
	"169.254.0.0/16", "100.64.0.0/10", "::1/128", "fc00::/7", "fe80::/10")

func mustNets(cidrs ...string) []*net.IPNet {
	var out []*net.IPNet
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic(err)
		}
		out = append(out, n)
	}
	return out
}

func permittedIP(ip net.IP) bool {
	for _, n := range permittedNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// Manager holds the current certificate and renews it.
type Manager struct {
	Dir      string
	Hostname func() string
	IPs      func() []net.IP
	Now      func() time.Time

	mu     sync.Mutex
	ca     *x509.Certificate
	caKey  crypto.Signer
	caPEM  []byte
	cert   atomic.Pointer[tls.Certificate]
	leaf   atomic.Pointer[x509.Certificate]
	OnCert func() // called after a renewal (FTPS reloads)
}

// Load reads or makes the CA and the server certificate.
func Load(dir string) (*Manager, error) {
	m := &Manager{Dir: dir, Hostname: hostname, IPs: localIPs, Now: time.Now}
	return m, m.Refresh()
}

func hostname() string {
	h, _ := os.Hostname()
	return strings.ToLower(h)
}

func (m *Manager) path(name string) string { return filepath.Join(m.Dir, name) }

// GetCertificate is for tls.Config: renewals apply to new connections at once.
func (m *Manager) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return m.cert.Load(), nil
}

// CAPEM is the CA certificate to install on computers.
func (m *Manager) CAPEM() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.caPEM
}

// Info describes the certificates for the web page.
type Info struct {
	CAName      string    `json:"caName"`
	Fingerprint string    `json:"fingerprint"` // SHA-256 of the CA, "AB:CD:..."
	CAExpires   time.Time `json:"caExpires"`
	Names       []string  `json:"names"` // what the server certificate covers
	Expires     time.Time `json:"expires"`
}

func (m *Manager) Info() Info {
	m.mu.Lock()
	ca := m.ca
	m.mu.Unlock()
	leaf := m.leaf.Load()
	info := Info{CAName: ca.Subject.CommonName, Fingerprint: Fingerprint(ca), CAExpires: ca.NotAfter, Expires: leaf.NotAfter}
	info.Names = append(info.Names, leaf.DNSNames...)
	for _, ip := range leaf.IPAddresses {
		info.Names = append(info.Names, ip.String())
	}
	return info
}

// Fingerprint: SHA-256 of a certificate, as browsers and systems show it.
func Fingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)
	h := strings.ToUpper(hex.EncodeToString(sum[:]))
	var parts []string
	for i := 0; i < len(h); i += 2 {
		parts = append(parts, h[i:i+2])
	}
	return strings.Join(parts, ":")
}

// Watch renews the certificate when needed (checked every hour).
func (m *Manager) Watch(ctx context.Context, logf func(string, ...any)) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := m.Refresh(); err != nil {
				logf("HTTPS certificate: %v", err)
			}
		}
	}
}

// Refresh makes sure the CA and the server certificate are valid for the
// current name and addresses, and renews what isn't.
func (m *Manager) Refresh() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	host := m.Hostname()
	if m.ca == nil {
		if err := m.loadCA(); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if m.ca == nil || !caCovers(m.ca, host) || m.Now().After(m.ca.NotAfter.AddDate(0, 0, -leafDays)) {
		if err := m.makeCA(host); err != nil {
			return err
		}
	}
	names, ips := m.wanted(host)
	if m.cert.Load() == nil {
		if c, leaf, err := m.loadLeaf(); err == nil {
			m.cert.Store(c)
			m.leaf.Store(leaf)
		}
	}
	if leaf := m.leaf.Load(); leaf != nil && m.leafOK(leaf, names, ips) {
		return nil
	}
	if err := m.makeLeaf(names, ips); err != nil {
		return err
	}
	if m.OnCert != nil {
		go m.OnCert()
	}
	return nil
}

func (m *Manager) wanted(host string) ([]string, []net.IP) {
	names := []string{host, host + ".local", "localhost"}
	var ips []net.IP
	for _, ip := range append([]net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}, m.IPs()...) {
		// A public address would break the whole certificate (outside the
		// CA's constraints): those are left out.
		if permittedIP(ip) && !slices.ContainsFunc(ips, ip.Equal) {
			ips = append(ips, ip)
		}
	}
	return names, ips
}

// leafOK: signed by our CA, not expiring within a month, covering every
// wanted name and address.
func (m *Manager) leafOK(leaf *x509.Certificate, names []string, ips []net.IP) bool {
	if leaf.CheckSignatureFrom(m.ca) != nil || m.Now().After(leaf.NotAfter.AddDate(0, 0, -renewDays)) {
		return false
	}
	for _, n := range names {
		if !slices.Contains(leaf.DNSNames, n) {
			return false
		}
	}
	for _, ip := range ips {
		if !slices.ContainsFunc(leaf.IPAddresses, ip.Equal) {
			return false
		}
	}
	return true
}

func caCovers(ca *x509.Certificate, host string) bool {
	return slices.Contains(ca.PermittedDNSDomains, host)
}

func (m *Manager) loadCA() error {
	certPEM, err := os.ReadFile(m.path("ca.crt"))
	if err != nil {
		return err
	}
	keyPEM, err := os.ReadFile(m.path("ca.key"))
	if err != nil {
		return err
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return err
	}
	ca, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return err
	}
	signer, ok := pair.PrivateKey.(crypto.Signer)
	if !ok || !ca.IsCA {
		return errors.New("ca.crt is not a CA")
	}
	m.ca, m.caKey, m.caPEM = ca, signer, certPEM
	return nil
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	return n
}

func (m *Manager) makeCA(host string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	now := m.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "nasd " + host + " (this NAS only)", Organization: []string{"nasd"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(20, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
		// Only this NAS: its names (and their subdomains) and LAN addresses.
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         []string{host, host + ".local", "localhost"},
		PermittedIPRanges:           permittedNets,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := writeFile(m.path("ca.key"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return err
	}
	if err := writeFile(m.path("ca.crt"), caPEM, 0o644); err != nil {
		return err
	}
	m.ca, m.caKey, m.caPEM = ca, key, caPEM
	m.leaf.Store(nil) // signed by the old CA: remade
	return nil
}

func (m *Manager) loadLeaf() (*tls.Certificate, *x509.Certificate, error) {
	c, err := tls.LoadX509KeyPair(m.path("tls.crt"), m.path("tls.key"))
	if err != nil {
		return nil, nil, err
	}
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		return nil, nil, err
	}
	return &c, leaf, nil
}

func (m *Manager) makeLeaf(names []string, ips []net.IP) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	now := m.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: names[0]},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(0, 0, leafDays),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     names,
		IPAddresses:  ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, m.ca, &key.PublicKey, m.caKey)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	// The chain (server certificate, then the CA) lets clients that were
	// given only the CA build the path.
	var chain bytes.Buffer
	pem.Encode(&chain, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	chain.Write(m.caPEM)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	// Key first: tls.crt never points to a key that isn't there yet.
	if err := writeFile(m.path("tls.key"), keyPEM, 0o600); err != nil {
		return err
	}
	if err := writeFile(m.path("tls.crt"), chain.Bytes(), 0o644); err != nil {
		return err
	}
	c, err := tls.X509KeyPair(chain.Bytes(), keyPEM)
	if err != nil {
		return err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return err
	}
	m.cert.Store(&c)
	m.leaf.Store(leaf)
	return nil
}

func writeFile(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

func localIPs() []net.IP {
	var ips []net.IP
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() {
			ips = append(ips, n.IP)
		}
	}
	return ips
}
