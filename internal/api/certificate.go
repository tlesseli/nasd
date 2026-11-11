package api

// HTTPS certificate (see internal/tlsutil):
//
//	GET  /api/certificate         -> tlsutil.Info
//	GET  /api/certificate/ca.crt  -> the NAS's CA certificate, to install on
//	                                 computers (public: no login, nothing secret)
//	POST /api/certificate/show    -> shows the start of the fingerprint on the
//	                                 front display, to check a download

import (
	"net/http"
	"os"
	"strings"
	"time"
)

func (s *Server) certificateRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/certificate", s.requireLogin(s.getCertificate))
	mux.HandleFunc("GET /api/certificate/ca.crt", s.downloadCA)
	mux.Handle("POST /api/certificate/show", s.requireLogin(s.showFingerprint))
}

func (s *Server) getCertificate(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Certs.Info())
}

func (s *Server) downloadCA(w http.ResponseWriter, r *http.Request) {
	host, _ := os.Hostname()
	w.Header().Set("Content-Type", "application/x-x509-ca-cert")
	w.Header().Set("Content-Disposition", `attachment; filename="nasd-`+host+`-ca.crt"`)
	w.Write(s.Certs.CAPEM())
}

// The front display has 16 characters: the first 16 hex digits (64 bits)
// of the fingerprint. Enough for a person to spot a forged certificate.
func (s *Server) showFingerprint(w http.ResponseWriter, r *http.Request) {
	if s.LCD == nil {
		writeError(w, http.StatusNotFound, "no front display")
		return
	}
	fp := strings.ReplaceAll(s.Certs.Info().Fingerprint, ":", "")
	s.LCD.Notice("Cert fingerprint", fp[:16], 10*time.Minute)
	w.WriteHeader(http.StatusNoContent)
}
