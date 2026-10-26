package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"relaylab/internal/runtimecontrol"
)

// The pre-spawn intent closes the gap before a child can publish readiness.
// PID zero is deliberately ambiguous: it must never be treated as no process.
type childIntent struct {
	Role            string    `json:"role"`
	InstanceID      string    `json:"instance_id"`
	Executable      string    `json:"executable"`
	PID             int       `json:"pid"`
	ProcessIdentity string    `json:"process_identity,omitempty"`
	StartedAt       time.Time `json:"started_at"`
}

func (s *Supervisor) childIntentPath(role string) string {
	return filepath.Join(s.opts.DataDir, "launcher-child-"+role+".json")
}
func (s *Supervisor) saveChildIntent(intent childIntent) error {
	b, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	return atomicWrite(s.childIntentPath(intent.Role), b, 0600)
}
func (s *Supervisor) removeChildIntent(role, instance string, pid int) error {
	b, err := os.ReadFile(s.childIntentPath(role))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var intent childIntent
	if err = json.Unmarshal(b, &intent); err != nil {
		return err
	}
	if intent.InstanceID != instance || intent.PID != pid {
		return nil
	}
	if err = removeIfExists(s.childIntentPath(role)); err != nil {
		return err
	}
	return syncDirectory(s.opts.DataDir)
}
func executableMatches(identity, expected string) bool {
	_, executable, ok := strings.Cut(identity, "|")
	if !ok {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(expected); err == nil {
		expected = resolved
	}
	if runtime.GOOS == "windows" {
		if strings.EqualFold(filepath.Clean(executable), filepath.Clean(expected)) {
			return true
		}
		// Windows may return a long image path for an 8.3 launch path.
		actualInfo, actualErr := os.Stat(executable)
		expectedInfo, expectedErr := os.Stat(expected)
		return actualErr == nil && expectedErr == nil && os.SameFile(actualInfo, expectedInfo)
	}
	return filepath.Clean(executable) == filepath.Clean(expected)
}
func (s *Supervisor) recoveryBlocked(cause error) error {
	err := fmt.Errorf("launcher: recovery_required: %w", cause)
	if fenceErr := s.drainLocked(); fenceErr != nil {
		err = errors.Join(err, fenceErr)
	}
	s.setState(StateFailed, err)
	return err
}

func (s *Supervisor) unmanagedIntentError() error {
	for _, role := range []string{"worker", "serve"} {
		managed := false
		for _, child := range s.children() {
			if child != nil && child.role == role {
				managed = true
			}
		}
		if managed {
			continue
		}
		if _, err := os.Stat(s.childIntentPath(role)); err == nil {
			return fmt.Errorf("%s child spawn record still requires recovery", role)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (s *Supervisor) quiesceRegistered(ctx context.Context) error {
	for _, role := range []string{"worker", "serve"} {
		managed := false
		for _, child := range s.children() {
			if child != nil && child.role == role {
				managed = true
			}
		}
		if managed {
			continue
		}
		b, err := os.ReadFile(s.childIntentPath(role))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return s.recoveryBlocked(err)
		}
		var intent childIntent
		if err = json.Unmarshal(b, &intent); err != nil || intent.Role != role || intent.InstanceID == "" || intent.Executable == "" || intent.PID < 0 {
			return s.recoveryBlocked(fmt.Errorf("invalid %s spawn intent", role))
		}
		// A late report can resolve a crash between Start and PID persistence,
		// but only after matching the private generation and actual executable.
		if intent.PID == 0 || intent.ProcessIdentity == "" {
			var report RuntimeReport
			raw, readErr := os.ReadFile(filepath.Join(s.opts.DataDir, "runtime-"+role+".json"))
			if readErr != nil || json.Unmarshal(raw, &report) != nil || report.Role != role || report.InstanceID != intent.InstanceID || report.LauncherProtocol != Protocol || report.PID <= 0 || (intent.PID != 0 && intent.PID != report.PID) || time.Since(report.HeartbeatAt) > 3*time.Second || time.Until(report.HeartbeatAt) > time.Second {
				return s.recoveryBlocked(fmt.Errorf("%s spawn outcome is unknown; waiting for a matching runtime identity", role))
			}
			identity, identityErr := processIdentity(report.PID)
			if identityErr != nil || !executableMatches(identity, intent.Executable) {
				return s.recoveryBlocked(fmt.Errorf("%s late runtime identity cannot be verified", role))
			}
			intent.PID, intent.ProcessIdentity = report.PID, identity
			if err = s.saveChildIntent(intent); err != nil {
				return s.recoveryBlocked(err)
			}
		}
		identity, identityErr := processIdentity(intent.PID)
		if identityErr != nil && !os.IsNotExist(identityErr) {
			return s.recoveryBlocked(identityErr)
		}
		if identityErr == nil {
			if identity != intent.ProcessIdentity || !executableMatches(identity, intent.Executable) {
				return s.recoveryBlocked(fmt.Errorf("%s PID %d no longer has the recorded identity", role, intent.PID))
			}
			if err = s.drainLocked(); err != nil {
				return err
			}
			draining, drainCancel := context.WithTimeout(ctx, s.opts.DrainTimeout)
			for {
				var report RuntimeReport
				raw, reportErr := os.ReadFile(filepath.Join(s.opts.DataDir, "runtime-"+role+".json"))
				// No report means initialization or shutdown: the verified child
				// still receives its stop marker, and actual exit remains mandatory.
				if reportErr != nil || json.Unmarshal(raw, &report) != nil || report.PID != intent.PID || report.InstanceID != intent.InstanceID || time.Since(report.HeartbeatAt) > 3*time.Second || (report.Maintenance && report.Active == 0) {
					break
				}
				current, inspectErr := processIdentity(intent.PID)
				if os.IsNotExist(inspectErr) {
					break
				}
				if inspectErr != nil || current != intent.ProcessIdentity {
					drainCancel()
					return s.recoveryBlocked(fmt.Errorf("%s process identity changed while draining", role))
				}
				select {
				case <-draining.Done():
					drainCancel()
					return s.recoveryBlocked(fmt.Errorf("%s orphan did not drain: %w", role, draining.Err()))
				case <-time.After(50 * time.Millisecond):
				}
			}
			drainCancel()
			// Missing readiness is allowed here: PID plus birth identity identifies
			// the exact child, and the stop marker requests its own cleanup.
			if err = atomicWrite(filepath.Join(s.opts.DataDir, role+".stop.request"), []byte("recovery\n"), 0600); err != nil {
				return err
			}
			closed, cancel := context.WithTimeout(ctx, s.opts.DrainTimeout+s.opts.GracePeriod)
			for {
				current, inspectErr := processIdentity(intent.PID)
				if os.IsNotExist(inspectErr) {
					break
				}
				if inspectErr != nil || current != intent.ProcessIdentity {
					cancel()
					return s.recoveryBlocked(fmt.Errorf("%s process identity changed during recovery", role))
				}
				select {
				case <-closed.Done():
					cancel()
					return s.recoveryBlocked(fmt.Errorf("%s child has not closed: %w", role, closed.Err()))
				case <-time.After(50 * time.Millisecond):
				}
			}
			cancel()
		}
		// The process is positively gone. Its whole-database lifetime lock must
		// also be free before discarding the durable spawn record.
		release, err := runtimecontrol.AcquireUsage(s.opts.DataDir, role)
		if err != nil {
			return s.recoveryBlocked(err)
		}
		err = release()
		if err != nil {
			return s.recoveryBlocked(err)
		}
		if err = s.removeChildIntent(role, intent.InstanceID, intent.PID); err != nil {
			return s.recoveryBlocked(err)
		}
	}
	return nil
}

// Hold both role locks throughout offline backup/restore/replacement. This is
// independent of readiness and excludes children still initializing/cleaning.
func (s *Supervisor) holdDataUsage() (func(), error) {
	var releases []func() error
	releaseAll := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			_ = releases[i]()
		}
	}
	for _, role := range []string{"serve", "worker"} {
		release, err := runtimecontrol.AcquireUsage(s.opts.DataDir, role)
		if err != nil {
			releaseAll()
			return nil, s.recoveryBlocked(err)
		}
		releases = append(releases, release)
	}
	return releaseAll, nil
}
