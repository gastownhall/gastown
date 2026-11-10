// This file holds one-time machine enrollment (spec §8): enrollment codes,
// the POST /agent/v1/enroll handler and its brute-force limiter.
package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/agentlink"
)

// DefaultEnrollCodeTTL is the lifetime of an enrollment code created with a
// non-positive ttl.
const DefaultEnrollCodeTTL = 15 * time.Minute

const (
	enrollMaxBody     = 64 << 10
	enrollMaxFailures = 10
	enrollFailWindow  = 15 * time.Minute
)

// errInvalidCode is returned by consumeEnrollCode for an unknown, used or
// expired code. The three cases are deliberately indistinguishable.
var errInvalidCode = errors.New("server: invalid enrollment code")

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

// CreateEnrollCode creates a single-use enrollment code and returns it. The
// code is 128 bits from crypto/rand, uppercase base32 without padding. Only
// its sha256 is stored, so the returned value is the only copy. A ttl <= 0
// means DefaultEnrollCodeTTL.
func (s *Store) CreateEnrollCode(ttl time.Duration) (string, error) {
	return s.createEnrollCode(ttl, time.Now())
}

// createEnrollCode is CreateEnrollCode with an explicit clock.
func (s *Store) createEnrollCode(ttl time.Duration, now time.Time) (string, error) {
	if ttl <= 0 {
		ttl = DefaultEnrollCodeTTL
	}
	b, err := randomBytes(16)
	if err != nil {
		return "", fmt.Errorf("server: generate enroll code: %w", err)
	}
	code := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
	if _, err := s.db.Exec(`INSERT INTO enroll_codes(sha, created_at, expires_at) VALUES (?, ?, ?)`,
		sha256Hex(code), ms(now), ms(now.Add(ttl))); err != nil {
		return "", fmt.Errorf("server: store enroll code: %w", err)
	}
	return code, nil
}

// consumeEnrollCode atomically marks code used by machineID and, in the same
// transaction, registers the machine and its token. It returns errInvalidCode
// when the code is unknown, already used or expired at now.
func (s *Store) consumeEnrollCode(code, machineID, tokenSHA string, h agentlink.Hello, now time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("server: enroll: %w", err)
	}
	defer tx.Rollback()
	t := ms(now)
	res, err := tx.Exec(`UPDATE enroll_codes SET used_at = ?, machine_id = ?
		WHERE sha = ? AND used_at IS NULL AND expires_at > ?`,
		t, machineID, sha256Hex(code), t)
	if err != nil {
		return fmt.Errorf("server: consume enroll code: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("server: consume enroll code: %w", err)
	}
	if n != 1 {
		return errInvalidCode
	}
	if _, err := tx.Exec(`
		INSERT INTO machines(id, hostname, os, arch, gtb_version, protocol, created_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		machineID, h.Hostname, h.OS, h.Arch, h.GtbVersion, h.Protocol, t, t); err != nil {
		return fmt.Errorf("server: enroll machine: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO tokens(sha, machine_id, created_at) VALUES (?, ?, ?)`,
		tokenSHA, machineID, t); err != nil {
		return fmt.Errorf("server: enroll token: %w", err)
	}
	return tx.Commit()
}

// EnrollRequest is the body of POST /agent/v1/enroll.
type EnrollRequest struct {
	Code  string          `json:"code"`
	Hello agentlink.Hello `json:"hello"`
}

// EnrollResponse is the success body of POST /agent/v1/enroll. Token is
// returned exactly once; the server keeps only its sha256.
type EnrollResponse struct {
	MachineID string `json:"machine_id"`
	Token     string `json:"token"`
}

// EnrollHandler returns the handler for POST /agent/v1/enroll. now is the
// clock used for code expiry and the failure limiter (nil means time.Now).
// trusted lists reverse proxies whose X-Forwarded-For identifies the client
// for the limiter (see clientIP). The handler never logs codes or tokens.
func (s *Store) EnrollHandler(now func() time.Time, trusted ...netip.Prefix) http.Handler {
	if now == nil {
		now = time.Now
	}
	return &enrollAPI{store: s, now: now, trusted: trusted, limiter: newFailureLimiter(enrollMaxFailures, enrollFailWindow)}
}

type enrollAPI struct {
	store   *Store
	now     func() time.Time
	trusted []netip.Prefix
	limiter *failureLimiter
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func (a *enrollAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	ip := clientIP(r, a.trusted)
	now := a.now()
	// The attempt is reserved as a failure up front, so a parallel burst
	// cannot exceed the limit. Every outcome but an invalid code releases it.
	release, ok := a.limiter.reserve(ip, now)
	if !ok {
		writeError(w, http.StatusTooManyRequests, "too_many_attempts")
		return
	}
	counted := false
	defer func() {
		if !counted {
			release()
		}
	}()

	var req EnrollRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, enrollMaxBody))
	if err := dec.Decode(&req); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "body_too_large")
			return
		}
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	if !sameProtocolMajor(req.Hello.Protocol, agentlink.ProtocolVersion) {
		writeError(w, http.StatusBadRequest, "protocol_mismatch")
		return
	}

	idBytes, err := randomBytes(16)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	tokBytes, err := randomBytes(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	machineID := hex.EncodeToString(idBytes)
	token := base64.RawURLEncoding.EncodeToString(tokBytes)
	hello := req.Hello
	hello.MachineID = machineID // the server assigns identity; ignore the client's

	err = a.store.consumeEnrollCode(req.Code, machineID, sha256Hex(token), hello, now)
	if errors.Is(err, errInvalidCode) {
		counted = true
		writeError(w, http.StatusUnauthorized, "invalid_code")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusOK, EnrollResponse{MachineID: machineID, Token: token})
}

// sameProtocolMajor reports whether two dotted versions share a non-empty
// numeric major component.
func sameProtocolMajor(got, want string) bool {
	major := func(v string) string {
		m, _, _ := strings.Cut(v, ".")
		if m == "" {
			return ""
		}
		for _, c := range m {
			if c < '0' || c > '9' {
				return ""
			}
		}
		return m
	}
	g := major(got)
	return g != "" && g == major(want)
}

// remoteIP strips the port from an http.Request.RemoteAddr.
func remoteIP(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// failureLimiter allows at most max failures per key within window. State is
// in memory and resets on restart.
type failureLimiter struct {
	mu        sync.Mutex
	max       int
	window    time.Duration
	failures  map[string][]time.Time
	lastSweep time.Time
}

func newFailureLimiter(max int, window time.Duration) *failureLimiter {
	return &failureLimiter{max: max, window: window, failures: map[string][]time.Time{}}
}

// prune drops failures for key at or before now-window. Caller holds mu.
func (l *failureLimiter) prune(key string, now time.Time) []time.Time {
	cutoff := now.Add(-l.window)
	fs := l.failures[key]
	i := 0
	for i < len(fs) && !fs[i].After(cutoff) {
		i++
	}
	fs = fs[i:]
	if len(fs) == 0 {
		delete(l.failures, key)
		return nil
	}
	l.failures[key] = fs
	return fs
}

// sweep prunes every key at most once per window, bounding memory to keys
// with recent failures. Caller holds mu.
func (l *failureLimiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < l.window {
		return
	}
	l.lastSweep = now
	for k := range l.failures {
		l.prune(k, now)
	}
}

// reserve atomically checks the limit for key and, if under it, records an
// attempt at now as a failure. It returns ok=false (nothing recorded) when
// key is at the limit. Call release to take the reserved attempt back when
// it turns out not to be a failure; release is idempotent and safe after the
// entry has aged out.
func (l *failureLimiter) reserve(key string, now time.Time) (release func(), ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(now)
	fs := l.prune(key, now)
	if len(fs) >= l.max {
		return func() {}, false
	}
	l.failures[key] = append(fs, now)
	var once sync.Once
	return func() { once.Do(func() { l.remove(key, now) }) }, true
}

// remove drops one failure for key recorded at t.
func (l *failureLimiter) remove(key string, t time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fs := l.failures[key]
	for i := len(fs) - 1; i >= 0; i-- {
		if fs[i].Equal(t) {
			fs = append(fs[:i:i], fs[i+1:]...)
			break
		}
	}
	if len(fs) == 0 {
		delete(l.failures, key)
		return
	}
	l.failures[key] = fs
}

// clear forgets every failure for key.
func (l *failureLimiter) clear(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, key)
}
