package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/agentlink"
)

func testHello() agentlink.Hello {
	return agentlink.Hello{MachineID: "client-chosen", Hostname: "box", OS: "linux", Arch: "amd64", GtbVersion: "0.1.0", Protocol: agentlink.ProtocolVersion}
}

func enrollBody(t *testing.T, code string, h agentlink.Hello) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"code": code, "hello": h})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func doEnroll(h http.Handler, remote string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/agent/v1/enroll", bytes.NewReader(body))
	if remote != "" {
		req.RemoteAddr = remote
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestCreateEnrollCode_Format(t *testing.T) {
	s := openTest(t)
	re := regexp.MustCompile(`^[A-Z2-7]{26}$`)
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		code, err := s.CreateEnrollCode(0)
		if err != nil {
			t.Fatal(err)
		}
		if !re.MatchString(code) {
			t.Fatalf("code %q is not 128-bit unpadded uppercase base32", code)
		}
		if seen[code] {
			t.Fatalf("duplicate code %q", code)
		}
		seen[code] = true
		// Only the sha is stored.
		var n int
		if err := s.db.QueryRow(`SELECT count(*) FROM enroll_codes WHERE sha = ?`, code).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatal("plain code stored in enroll_codes")
		}
		var created, expires int64
		if err := s.db.QueryRow(`SELECT created_at, expires_at FROM enroll_codes WHERE sha = ?`, sha(code)).Scan(&created, &expires); err != nil {
			t.Fatalf("code sha not stored: %v", err)
		}
		if got := time.Duration(expires-created) * time.Millisecond; got != 15*time.Minute {
			t.Fatalf("default ttl = %v, want 15m", got)
		}
	}
}

func TestEnroll_Success(t *testing.T) {
	s := openTest(t)
	code, err := s.CreateEnrollCode(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	h := s.EnrollHandler(time.Now)
	rec := doEnroll(h, "", enrollBody(t, code, testHello()))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type %q", ct)
	}
	var resp struct {
		MachineID string `json:"machine_id"`
		Token     string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(resp.MachineID) {
		t.Fatalf("machine_id %q is not 128-bit hex", resp.MachineID)
	}
	raw, err := base64.RawURLEncoding.DecodeString(resp.Token)
	if err != nil || len(raw) != 32 {
		t.Fatalf("token is not 256-bit base64url: %v len=%d", err, len(raw))
	}
	m, err := s.MachineByToken(sha(resp.Token))
	if err != nil {
		t.Fatalf("token not stored by sha: %v", err)
	}
	if m.ID != resp.MachineID || m.Hostname != "box" {
		t.Fatalf("machine %+v", m)
	}
	if _, err := s.Machine("client-chosen"); err != ErrNotFound {
		t.Fatalf("client-supplied machine id was used: %v", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM tokens WHERE sha = ?`, resp.Token).Scan(&n); err != nil || n != 0 {
		t.Fatalf("plain token stored (n=%d err=%v)", n, err)
	}
	var usedBy string
	if err := s.db.QueryRow(`SELECT machine_id FROM enroll_codes WHERE sha = ?`, sha(code)).Scan(&usedBy); err != nil || usedBy != resp.MachineID {
		t.Fatalf("code not marked used by machine: %q %v", usedBy, err)
	}
}

func TestEnroll_CodeSingleUse(t *testing.T) {
	s := openTest(t)
	code, _ := s.CreateEnrollCode(0)
	h := s.EnrollHandler(time.Now)
	if rec := doEnroll(h, "", enrollBody(t, code, testHello())); rec.Code != http.StatusOK {
		t.Fatalf("first use: %d", rec.Code)
	}
	rec := doEnroll(h, "", enrollBody(t, code, testHello()))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("second use: %d", rec.Code)
	}
	if strings.TrimSpace(rec.Body.String()) != `{"error":"invalid_code"}` {
		t.Fatalf("body %q", rec.Body)
	}
}

func TestEnroll_Expired(t *testing.T) {
	s := openTest(t)
	code, _ := s.CreateEnrollCode(0)
	h := s.EnrollHandler(func() time.Time { return time.Now().Add(16 * time.Minute) })
	rec := doEnroll(h, "", enrollBody(t, code, testHello()))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired: %d", rec.Code)
	}
}

func TestEnroll_InvalidCodesIndistinguishable(t *testing.T) {
	s := openTest(t)
	used, _ := s.CreateEnrollCode(0)
	expired, _ := s.CreateEnrollCode(time.Minute)
	later := time.Now().Add(2 * time.Minute)
	h := s.EnrollHandler(func() time.Time { return later })
	if rec := doEnroll(h, "", enrollBody(t, used, testHello())); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	var bodies []string
	for _, c := range []string{"NOSUCHCODE", used, expired, ""} {
		rec := doEnroll(h, "", enrollBody(t, c, testHello()))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("code %q: %d", c, rec.Code)
		}
		bodies = append(bodies, rec.Body.String())
	}
	for _, b := range bodies[1:] {
		if b != bodies[0] {
			t.Fatalf("bodies differ: %q vs %q", b, bodies[0])
		}
	}
}

func TestEnroll_ConcurrentConsume(t *testing.T) {
	s := openTest(t)
	code, _ := s.CreateEnrollCode(0)
	h := s.EnrollHandler(time.Now)
	body := enrollBody(t, code, testHello())
	var wg sync.WaitGroup
	codes := make([]int, 10)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = doEnroll(h, "", body).Code
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			ok++
		case http.StatusUnauthorized:
		default:
			t.Fatalf("unexpected status %d", c)
		}
	}
	if ok != 1 {
		t.Fatalf("%d of 10 concurrent consumes succeeded, want 1 (%v)", ok, codes)
	}
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM machines`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("machines = %d (%v), want 1", n, err)
	}
}

func TestEnroll_ProtocolMismatch(t *testing.T) {
	s := openTest(t)
	code, _ := s.CreateEnrollCode(0)
	h := s.EnrollHandler(time.Now)
	for _, p := range []string{"2.0.0", "0.9.0", "", "garbage"} {
		hello := testHello()
		hello.Protocol = p
		rec := doEnroll(h, "", enrollBody(t, code, hello))
		if rec.Code != http.StatusBadRequest || strings.TrimSpace(rec.Body.String()) != `{"error":"protocol_mismatch"}` {
			t.Fatalf("protocol %q: %d %s", p, rec.Code, rec.Body)
		}
	}
	// Same major, different minor is accepted; the code was not burned.
	hello := testHello()
	hello.Protocol = "1.7.3"
	if rec := doEnroll(h, "", enrollBody(t, code, hello)); rec.Code != http.StatusOK {
		t.Fatalf("same major: %d %s", rec.Code, rec.Body)
	}
}

func TestEnroll_RateLimit(t *testing.T) {
	s := openTest(t)
	good, _ := s.CreateEnrollCode(time.Hour)
	now := time.Now()
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	h := s.EnrollHandler(clock)
	for i := 0; i < 10; i++ {
		if rec := doEnroll(h, "203.0.113.5:4000", enrollBody(t, "WRONG", testHello())); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, rec.Code)
		}
	}
	rec := doEnroll(h, "203.0.113.5:4001", enrollBody(t, good, testHello()))
	if rec.Code != http.StatusTooManyRequests || strings.TrimSpace(rec.Body.String()) != `{"error":"too_many_attempts"}` {
		t.Fatalf("11th attempt: %d %s", rec.Code, rec.Body)
	}
	// The blocked attempt did not consume the code: another IP can use it.
	other, _ := s.CreateEnrollCode(time.Hour)
	if rec := doEnroll(h, "198.51.100.7:1", enrollBody(t, other, testHello())); rec.Code != http.StatusOK {
		t.Fatalf("other IP: %d", rec.Code)
	}
	// After the window passes, the first IP may try again.
	mu.Lock()
	now = now.Add(15*time.Minute + time.Second)
	mu.Unlock()
	if rec := doEnroll(h, "203.0.113.5:4002", enrollBody(t, good, testHello())); rec.Code != http.StatusOK {
		t.Fatalf("after window: %d %s", rec.Code, rec.Body)
	}
}

func TestEnroll_BadRequests(t *testing.T) {
	s := openTest(t)
	h := s.EnrollHandler(time.Now)

	big := bytes.Repeat([]byte("a"), 64<<10+1)
	body := append([]byte(`{"code":"`), big...)
	body = append(body, []byte(`"}`)...)
	if rec := doEnroll(h, "", body); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize: %d", rec.Code)
	}
	if rec := doEnroll(h, "", []byte(`{not json`)); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json: %d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/agent/v1/enroll", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", rec.Code)
	}
}
