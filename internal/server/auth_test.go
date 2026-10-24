package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const testAdminToken = "test-admin-token-0123456789-abcdefghij" // >= 32 chars

// webFixture is an App over a fresh store with an injected clock and a known
// admin token.
type webFixture struct {
	t     *testing.T
	store *Store
	clock *fakeClock
	app   *App
}

func newWebFixture(t *testing.T) *webFixture {
	t.Helper()
	return newWebFixtureURL(t, "http://127.0.0.1:8080")
}

func newWebFixtureURL(t *testing.T, publicURL string) *webFixture {
	t.Helper()
	st := openTest(t)
	clk := newFakeClock()
	var out bytes.Buffer
	if err := EnsureAdminToken(st, testAdminToken, publicURL, &out, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	app := NewApp(Config{Store: st, Now: clk.Now, PublicURL: publicURL})
	return &webFixture{t: t, store: st, clock: clk, app: app}
}

type webSession struct {
	cookie *http.Cookie
	csrf   string
}

func (f *webFixture) serve(req *http.Request) *httptest.ResponseRecorder {
	f.t.Helper()
	if req.RemoteAddr == "" || req.RemoteAddr == "192.0.2.1:1234" {
		req.RemoteAddr = "192.0.2.1:1234"
	}
	rec := httptest.NewRecorder()
	f.app.ServeHTTP(rec, req)
	return rec
}

func (f *webFixture) loginJSON(token, remote string) *httptest.ResponseRecorder {
	f.t.Helper()
	body, _ := json.Marshal(map[string]string{"token": token})
	req := httptest.NewRequest(http.MethodPost, "/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = remote
	return f.serve(req)
}

// login logs in with the admin token and returns the session cookie and CSRF
// token.
func (f *webFixture) login() webSession {
	f.t.Helper()
	rec := f.loginJSON(testAdminToken, "192.0.2.1:1234")
	if rec.Code != http.StatusOK {
		f.t.Fatalf("login status = %d, body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		OK   bool   `json:"ok"`
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || !body.OK || body.CSRF == "" {
		f.t.Fatalf("login body %s, %v", rec.Body.String(), err)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			return webSession{cookie: c, csrf: body.CSRF}
		}
	}
	f.t.Fatal("no session cookie")
	return webSession{}
}

// api calls an /api/v1 route with the session (if any) and its CSRF token.
func (f *webFixture) api(method, path string, s *webSession, body any) *httptest.ResponseRecorder {
	f.t.Helper()
	var rd *bytes.Reader
	switch b := body.(type) {
	case nil:
		rd = bytes.NewReader(nil)
	case []byte:
		rd = bytes.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			f.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if s != nil {
		req.AddCookie(&http.Cookie{Name: s.cookie.Name, Value: s.cookie.Value})
		req.Header.Set("X-CSRF-Token", s.csrf)
	}
	return f.serve(req)
}

func frameToken(t *testing.T, out string) string {
	t.Helper()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("frame has %d lines, want 5:\n%s", len(lines), out)
	}
	bar := strings.Repeat("=", 64)
	if lines[0] != bar || lines[4] != bar {
		t.Fatalf("frame bars wrong:\n%s", out)
	}
	if lines[1] != "gtb dashboard admin token (shown once; store it safely):" {
		t.Fatalf("frame heading = %q", lines[1])
	}
	return lines[2]
}

func TestAuth_TokenPrintedOnceOnFirstStart(t *testing.T) {
	st := openTest(t)
	var out bytes.Buffer
	if err := EnsureAdminToken(st, "", "https://gtb.example.com/", &out, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	tok := frameToken(t, out.String())
	if len(tok) != 43 { // 32 bytes, base64url without padding
		t.Fatalf("token %q has length %d, want 43", tok, len(tok))
	}
	if !strings.Contains(out.String(), "\nlogin at https://gtb.example.com/login\n") {
		t.Fatalf("frame missing login line:\n%s", out.String())
	}
	a, err := st.Admin()
	if err != nil {
		t.Fatal(err)
	}
	if a.TokenSHA != sha(tok) || len(a.CSRFKey) != 32 {
		t.Fatalf("stored admin = %+v", a)
	}
	var n int
	if err := st.db.QueryRow(`SELECT count(*) FROM admin WHERE token_sha = ?`, tok).Scan(&n); err != nil || n != 0 {
		t.Fatalf("raw token stored (n=%d, %v)", n, err)
	}

	// Second start: nothing printed, token unchanged.
	out.Reset()
	if err := EnsureAdminToken(st, "", "https://gtb.example.com", &out, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("second start printed %q", out.String())
	}
	if a2, _ := st.Admin(); a2.TokenSHA != sha(tok) {
		t.Fatal("token changed on second start")
	}

	// The printed token logs in.
	clk := newFakeClock()
	f := &webFixture{t: t, store: st, clock: clk, app: NewApp(Config{Store: st, Now: clk.Now, PublicURL: "https://gtb.example.com"})}
	if rec := f.loginJSON(tok, "192.0.2.9:1"); rec.Code != http.StatusOK {
		t.Fatalf("login with printed token: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAuth_EnvTokenNotPrinted(t *testing.T) {
	st := openTest(t)
	var out, logs bytes.Buffer
	logf := func(format string, args ...any) {
		logs.WriteString(strings.TrimSpace(fmt.Sprintf(format, args...)) + "\n")
	}
	if err := EnsureAdminToken(st, testAdminToken, "http://x", &out, logf); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 || logs.Len() != 0 {
		t.Fatalf("env token printed: out=%q logs=%q", out.String(), logs.String())
	}
	if a, _ := st.Admin(); a.TokenSHA != sha(testAdminToken) {
		t.Fatal("env token not stored")
	}

	// A different env token replaces the stored one, logged without the token.
	other := strings.Repeat("z", 40)
	if err := EnsureAdminToken(st, other, "http://x", &out, logf); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("replacement printed %q", out.String())
	}
	if !strings.Contains(logs.String(), "admin token replaced from GTB_ADMIN_TOKEN") {
		t.Fatalf("logs = %q", logs.String())
	}
	if strings.Contains(logs.String(), other) || strings.Contains(logs.String(), testAdminToken) {
		t.Fatalf("token leaked into logs: %q", logs.String())
	}
	if a, _ := st.Admin(); a.TokenSHA != sha(other) {
		t.Fatal("env token not replaced")
	}

	// The same env token again: no log.
	logs.Reset()
	if err := EnsureAdminToken(st, other, "http://x", &out, logf); err != nil {
		t.Fatal(err)
	}
	if logs.Len() != 0 {
		t.Fatalf("unchanged env token logged %q", logs.String())
	}
}

func TestAuth_ShortEnvTokenRefused(t *testing.T) {
	st := openTest(t)
	var out bytes.Buffer
	err := EnsureAdminToken(st, strings.Repeat("a", 31), "http://x", &out, func(string, ...any) {})
	if err == nil || !strings.Contains(err.Error(), "GTB_ADMIN_TOKEN") || !strings.Contains(err.Error(), "32") {
		t.Fatalf("err = %v, want a GTB_ADMIN_TOKEN length error", err)
	}
	if out.Len() != 0 {
		t.Fatalf("printed %q", out.String())
	}
	if _, err := st.Admin(); err != ErrNotFound {
		t.Fatalf("admin stored despite refusal: %v", err)
	}
}

func TestAuth_LoginWrongToken401AndLimiter429(t *testing.T) {
	f := newWebFixture(t)
	for i := 0; i < 10; i++ {
		rec := f.loginJSON("wrong-token", "198.51.100.7:5000")
		wantError(t, rec, http.StatusUnauthorized, "invalid_token")
		if len(rec.Result().Cookies()) != 0 {
			t.Fatal("cookie set on failed login")
		}
	}
	// Locked out, even with the right token.
	wantError(t, f.loginJSON(testAdminToken, "198.51.100.7:5001"), http.StatusTooManyRequests, "too_many_attempts")
	// Other IPs are unaffected.
	if rec := f.loginJSON(testAdminToken, "198.51.100.8:5000"); rec.Code != http.StatusOK {
		t.Fatalf("other ip: %d", rec.Code)
	}
	// The window passes.
	f.clock.Advance(15*time.Minute + time.Second)
	if rec := f.loginJSON(testAdminToken, "198.51.100.7:5000"); rec.Code != http.StatusOK {
		t.Fatalf("after window: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAuth_RotateRevokesSessions(t *testing.T) {
	f := newWebFixture(t)
	s := f.login()
	if rec := f.api(http.MethodGet, "/api/v1/me", &s, nil); rec.Code != http.StatusOK {
		t.Fatalf("me before rotate: %d", rec.Code)
	}
	before, _ := f.store.Admin()

	var out bytes.Buffer
	if err := RotateAdminToken(f.store, "http://127.0.0.1:8080", &out); err != nil {
		t.Fatal(err)
	}
	tok := frameToken(t, out.String())
	after, _ := f.store.Admin()
	if after.TokenSHA != sha(tok) || bytes.Equal(after.CSRFKey, before.CSRFKey) {
		t.Fatal("rotate did not replace token and csrf key")
	}
	wantError(t, f.api(http.MethodGet, "/api/v1/me", &s, nil), http.StatusUnauthorized, "unauthorized")
	wantError(t, f.loginJSON(testAdminToken, "192.0.2.1:1"), http.StatusUnauthorized, "invalid_token")
	if rec := f.loginJSON(tok, "192.0.2.1:1"); rec.Code != http.StatusOK {
		t.Fatalf("login with rotated token: %d", rec.Code)
	}
}

func TestAuth_LoginFormRedirectsAndCookieFlags(t *testing.T) {
	f := newWebFixtureURL(t, "https://gtb.example.com")
	form := url.Values{"token": {testAdminToken}}
	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := f.serve(req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("form login: %d Location=%q", rec.Code, rec.Header().Get("Location"))
	}
	cs := rec.Result().Cookies()
	if len(cs) != 1 {
		t.Fatalf("cookies = %v", cs)
	}
	c := cs[0]
	if c.Name != "gtb_session" || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
		t.Fatalf("cookie = %+v", c)
	}
	if c.MaxAge != int((7 * 24 * time.Hour).Seconds()) {
		t.Fatalf("cookie MaxAge = %d", c.MaxAge)
	}
	// Session stored by hash only, as admin, for 7 days.
	var login string
	var exp int64
	if err := f.store.db.QueryRow(`SELECT login, expires_at FROM web_sessions WHERE sha = ?`, sha(c.Value)).Scan(&login, &exp); err != nil {
		t.Fatal(err)
	}
	if login != "admin" || !fromMS(exp).Equal(f.clock.Now().Add(7*24*time.Hour)) {
		t.Fatalf("session login=%q exp=%v", login, fromMS(exp))
	}

	// Wrong token via the form: 401 HTML, no cookie.
	form = url.Values{"token": {"nope"}}
	req = httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = f.serve(req)
	if rec.Code != http.StatusUnauthorized || len(rec.Result().Cookies()) != 0 || !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("bad form login: %d %v %q", rec.Code, rec.Result().Cookies(), rec.Header().Get("Content-Type"))
	}

	// Plain http public URL: not Secure.
	g := newWebFixture(t)
	if s := g.login(); s.cookie.Secure {
		t.Fatal("cookie Secure on http public URL")
	}
}

func TestAuth_MeAndLogout(t *testing.T) {
	f := newWebFixture(t)
	wantError(t, f.api(http.MethodGet, "/api/v1/me", nil, nil), http.StatusUnauthorized, "unauthorized")
	s := f.login()
	rec := f.api(http.MethodGet, "/api/v1/me", &s, nil)
	wantStatus(t, rec, http.StatusOK)
	me := decode[map[string]string](t, rec)
	if me["login"] != "admin" || me["csrf_token"] != s.csrf {
		t.Fatalf("me = %v (login csrf %q)", me, s.csrf)
	}

	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: s.cookie.Value})
	rec = f.serve(req)
	if rec.Code != http.StatusOK {
		t.Fatalf("logout: %d", rec.Code)
	}
	cs := rec.Result().Cookies()
	if len(cs) != 1 || cs[0].Name != sessionCookie || cs[0].MaxAge >= 0 {
		t.Fatalf("logout cookie = %+v", cs)
	}
	wantError(t, f.api(http.MethodGet, "/api/v1/me", &s, nil), http.StatusUnauthorized, "unauthorized")
}

func TestAuth_ExpiredSessionRejected(t *testing.T) {
	f := newWebFixture(t)
	s := f.login()
	f.clock.Advance(7*24*time.Hour - time.Second)
	if rec := f.api(http.MethodGet, "/api/v1/me", &s, nil); rec.Code != http.StatusOK {
		t.Fatalf("before expiry: %d", rec.Code)
	}
	f.clock.Advance(time.Second)
	wantError(t, f.api(http.MethodGet, "/api/v1/me", &s, nil), http.StatusUnauthorized, "unauthorized")
}

func TestAuth_LoginPage(t *testing.T) {
	f := newWebFixture(t)
	rec := f.serve(httptest.NewRequest(http.MethodGet, "/login", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /login: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `type="password"`) || !strings.Contains(body, `action="/auth/login"`) {
		t.Fatalf("login page:\n%s", body)
	}
	for _, bad := range []string{"http://", "https://", "<script", "<link"} {
		if strings.Contains(body, bad) {
			t.Fatalf("login page references %q", bad)
		}
	}
	wantSecurityHeaders(t, rec)
}

func wantSecurityHeaders(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	for k, v := range map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "same-origin",
		"X-Frame-Options":         "DENY",
		"Content-Security-Policy": "default-src 'self'",
	} {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}
