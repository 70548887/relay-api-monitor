package launcher

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

func (s *Supervisor) startControl() error {
	ln, e := net.Listen("tcp", s.opts.ControlAddress)
	if e != nil {
		return e
	}
	server := &http.Server{Handler: s.controlHandler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second}
	s.mu.Lock()
	s.server = server
	s.opts.ControlAddress = ln.Addr().String()
	s.mu.Unlock()
	if _, e = s.WriteTokenFile(); e != nil {
		ln.Close()
		return e
	}
	if e = s.WriteStateFile(); e != nil {
		ln.Close()
		return e
	}
	go func() { s.serverErr <- server.Serve(ln) }()
	return nil
}
func (s *Supervisor) controlHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleControl(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "protocol": Protocol, "version": s.opts.LauncherVersion})
	}))
	mux.HandleFunc("/status", s.handleControl(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, s.Status()) }))
	mux.HandleFunc("/control/start", s.handleControl(func(w http.ResponseWriter, r *http.Request) {
		e := s.Start(r.Context())
		writeControlResult(w, e, s.Status())
	}))
	mux.HandleFunc("/control/stop", s.handleControl(func(w http.ResponseWriter, r *http.Request) {
		e := s.Stop(r.Context())
		writeControlResult(w, e, s.Status())
	}))
	mux.HandleFunc("/control/restart", s.handleControl(func(w http.ResponseWriter, r *http.Request) {
		e := s.Restart(r.Context())
		writeControlResult(w, e, s.Status())
	}))
	mux.HandleFunc("/control/drain", s.handleControl(func(w http.ResponseWriter, r *http.Request) { e := s.Drain(); writeControlResult(w, e, s.Status()) }))
	mux.HandleFunc("/control/resume", s.handleControl(func(w http.ResponseWriter, r *http.Request) { e := s.Resume(); writeControlResult(w, e, s.Status()) }))
	mux.HandleFunc("/control/replace", s.handleControl(s.handleReplace))
	mux.HandleFunc("/control/rollback", s.handleControl(s.handleRollback))
	mux.HandleFunc("/control/shutdown", s.handleControl(func(w http.ResponseWriter, r *http.Request) {
		if !s.transactionMu.TryLock() {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "operation_in_progress"})
			return
		}
		go func() {
			defer s.transactionMu.Unlock()
			s.opMu.Lock()
			defer s.opMu.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), s.opts.DrainTimeout+2*s.opts.GracePeriod)
			defer cancel()
			if err := s.stopLocked(ctx); err != nil {
				s.setState(StateFailed, err)
				return
			}
			// On drain timeout keep ownership and control available. A shutdown
			// is committed only after both independent child completions.
			s.cancel()
		}()
		writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "protocol": Protocol})
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackRequest(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "loopback_required"})
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func (s *Supervisor) handleControl(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		mutation := strings.HasPrefix(r.URL.Path, "/control/")
		if mutation && r.Method != http.MethodPost || !mutation && r.Method != http.MethodGet {
			if mutation {
				w.Header().Set("Allow", http.MethodPost)
			} else {
				w.Header().Set("Allow", http.MethodGet)
			}
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		next(w, r)
	}
}
func (s *Supervisor) authorized(r *http.Request) bool {
	provided := r.Header.Get("X-RelayLab-Launcher-Token")
	if provided == "" {
		auth := r.Header.Get("Authorization")
		if strings.HasPrefix(auth, "Bearer ") {
			provided = strings.TrimPrefix(auth, "Bearer ")
		}
	}
	a := sha256.Sum256([]byte(provided))
	b := sha256.Sum256([]byte(s.opts.ControlToken))
	return provided != "" && subtle.ConstantTimeCompare(a[:], b[:]) == 1
}
func isLoopbackRequest(r *http.Request) bool {
	host, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Supervisor) handleReplace(w http.ResponseWriter, r *http.Request) {
	var req InstallRequest
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if e := decoder.Decode(&req); e != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	if e := decoder.Decode(&struct{}{}); !errors.Is(e, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	if !operationPattern.MatchString(req.OperationID) || req.StagedPath == "" || req.Size <= 0 || len(req.SHA256) != 64 || req.Version == "" || req.Commit == "" || req.LauncherProtocol != Protocol || req.DatabaseSchema < 1 || req.MinDatabaseSchema < 1 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "required_release_metadata_missing"})
		return
	}
	if !s.transactionMu.TryLock() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "operation_in_progress"})
		return
	}
	t, e := s.newTransaction(req, "update")
	if e != nil {
		s.transactionMu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": e.Error()})
		return
	}
	go func() {
		defer s.transactionMu.Unlock()
		s.opMu.Lock()
		defer s.opMu.Unlock()
		ctx, cancel := context.WithTimeout(s.lifetime, 20*time.Minute)
		defer cancel()
		if e := s.installLocked(ctx, t, false); e != nil {
			s.mu.Lock()
			s.lastError = e
			s.mu.Unlock()
			_ = s.WriteStateFile()
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "protocol": Protocol, "operation_id": req.OperationID})
}
func (s *Supervisor) handleRollback(w http.ResponseWriter, r *http.Request) {
	var requested struct {
		OperationID  string `json:"operation_id"`
		DeferRestart bool   `json:"defer_restart"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if e := decoder.Decode(&requested); e != nil || !operationPattern.MatchString(requested.OperationID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "operation_id_required"})
		return
	}
	if e := decoder.Decode(&struct{}{}); !errors.Is(e, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	if !s.transactionMu.TryLock() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "operation_in_progress"})
		return
	}
	// Persist acceptance before detaching. Selecting/probing a backup runs in the
	// background so the web process can immediately finish its active request.
	req := InstallRequest{OperationID: requested.OperationID, DeferRestart: requested.DeferRestart}
	t, e := s.newTransaction(req, "rollback")
	if e != nil {
		s.transactionMu.Unlock()
		writeJSON(w, 500, map[string]string{"error": e.Error()})
		return
	}
	go func() {
		defer s.transactionMu.Unlock()
		s.opMu.Lock()
		defer s.opMu.Unlock()
		ctx, cancel := context.WithTimeout(s.lifetime, 20*time.Minute)
		defer cancel()
		req, e := s.latestRollback(ctx)
		if e != nil {
			_ = s.phase(t, "failed", 0, e.Error(), "rollback_unavailable")
			return
		}
		req.OperationID = t.ID
		req.DeferRestart = requested.DeferRestart
		t.Request = req
		t.Version = req.Version
		t.Commit = req.Commit
		if e = s.installLocked(ctx, t, false); e != nil {
			s.mu.Lock()
			s.lastError = e
			s.mu.Unlock()
			_ = s.WriteStateFile()
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "protocol": Protocol, "operation_id": requested.OperationID})
}
func writeControlResult(w http.ResponseWriter, e error, status Status) {
	if e != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": e.Error(), "status": status})
		return
	}
	writeJSON(w, http.StatusOK, status)
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
