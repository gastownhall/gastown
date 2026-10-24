// This file holds dashboard auth (spec §8, ruling R40): the admin token, the
// login page and endpoints, web sessions and per-session CSRF tokens. There is
// no OAuth: the maintainer logs in with the admin token, which the server
// keeps only as a sha256.
package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
)

const (
	// sessionCookie is the dashboard session cookie name.
	sessionCookie = "gtb_session"
	// SessionTTL is the lifetime of a dashboard session.
	SessionTTL = 7 * 24 * time.Hour
	// adminLogin is the login recorded for every dashboard session and used
	// as the Actor of dashboard approvals.
	adminLogin = "admin"
	// MinAdminTokenLen is the minimum length of GTB_ADMIN_TOKEN.
	MinAdminTokenLen = 32

	loginMaxBody     = 4 << 10
	loginMaxFailures = 10
	loginFailWindow  = 15 * time.Minute
)

// Admin is the admin table's single row.
type Admin struct {
	TokenSHA  string
	CSRFKey   []byte
	CreatedAt time.Time
}

// Admin returns the stored admin token hash and CSRF key, or ErrNotFound.
func (s *Store) Admin() (*Admin, error) {
	var a Admin
	var created sql.NullInt64
	err := s.db.QueryRow(`SELECT token_sha, csrf_key, created_at FROM admin WHERE id = 1`).Scan(&a.TokenSHA, &a.CSRFKey, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("server: admin: %w", err)
	}
	if created.Valid {
		a.CreatedAt = fromMS(created.Int64)
	}
	return &a, nil
}

// replaceAdmin stores tokenSHA with a fresh CSRF key and deletes every web
// session, in one transaction. Old sessions and their CSRF tokens stop working.
func (s *Store) replaceAdmin(tokenSHA string, now time.Time) error {
	key, err := randomBytes(32)
	if err != nil {
		return fmt.Errorf("server: generate csrf key: %w", err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("server: store admin token: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		INSERT INTO admin(id, token_sha, csrf_key, created_at) VALUES (1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET token_sha = excluded.token_sha,
			csrf_key = excluded.csrf_key, created_at = excluded.created_at`,
		tokenSHA, key, ms(now)); err != nil {
		return fmt.Errorf("server: store admin token: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM web_sessions`); err != nil {
		return fmt.Errorf("server: revoke web sessions: %w", err)
	}
	return tx.Commit()
}

func newAdminToken() (string, error) {
	b, err := randomBytes(32)
	if err != nil {
		return "", fmt.Errorf("server: generate admin token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// printTokenFrame writes the admin token frame that `kubectl logs` shows.
func printTokenFrame(out io.Writer, token, publicURL string) error {
	bar := strings.Repeat("=", 64)
	_, err := fmt.Fprintf(out, "%s\ngtb dashboard admin token (shown once; store it safely):\n%s\nlogin at %s/login\n%s\n",
		bar, token, strings.TrimRight(publicURL, "/"), bar)
	return err
}

// EnsureAdminToken makes sure the store has an admin token at server start.
//
//   - envToken (GTB_ADMIN_TOKEN) shorter than MinAdminTokenLen is an error.
//   - With no stored token: envToken's sha256 is stored and nothing is
//     printed; without envToken a 256-bit token is generated, stored by
//     sha256 and printed once to out in the token frame.
//   - With a stored token and an envToken that does not match it, the env
//     token replaces it (new CSRF key, all web sessions revoked) and logf is
//     told "admin token replaced from GTB_ADMIN_TOKEN".
//
// No token is ever passed to logf.
func EnsureAdminToken(st *Store, envToken, publicURL string, out io.Writer, logf func(string, ...any)) error {
	if envToken != "" && len(envToken) < MinAdminTokenLen {
		return fmt.Errorf("GTB_ADMIN_TOKEN must be at least %d characters (got %d); refusing to start", MinAdminTokenLen, len(envToken))
	}
	now := time.Now()
	cur, err := st.Admin()
	switch {
	case errors.Is(err, ErrNotFound):
		if envToken != "" {
			return st.replaceAdmin(sha256Hex(envToken), now)
		}
		tok, err := newAdminToken()
		if err != nil {
			return err
		}
		if err := st.replaceAdmin(sha256Hex(tok), now); err != nil {
			return err
		}
		return printTokenFrame(out, tok, publicURL)
	case err != nil:
		return err
	}
	if envToken == "" {
		return nil
	}
	want := sha256Hex(envToken)
	if subtle.ConstantTimeCompare([]byte(want), []byte(cur.TokenSHA)) == 1 {
		return nil
	}
	if err := st.replaceAdmin(want, now); err != nil {
		return err
	}
	logf("admin token replaced from GTB_ADMIN_TOKEN")
	return nil
}

// RotateAdminToken generates and stores a new admin token and CSRF key,
// revokes every web session, and prints the new token in the token frame.
func RotateAdminToken(st *Store, publicURL string, out io.Writer) error {
	tok, err := newAdminToken()
	if err != nil {
		return err
	}
	if err := st.replaceAdmin(sha256Hex(tok), time.Now()); err != nil {
		return err
	}
	return printTokenFrame(out, tok, publicURL)
}

// createWebSession stores a new admin session and returns its id. Only the
// id's sha256 is stored.
func (s *Store) createWebSession(now time.Time) (string, error) {
	b, err := randomBytes(32)
	if err != nil {
		return "", fmt.Errorf("server: generate session id: %w", err)
	}
	id := base64.RawURLEncoding.EncodeToString(b)
	if _, err := s.db.Exec(`INSERT INTO web_sessions(sha, login, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		sha256Hex(id), adminLogin, ms(now), ms(now.Add(SessionTTL))); err != nil {
		return "", fmt.Errorf("server: create session: %w", err)
	}
	return id, nil
}

// webSessionValid reports whether id names a session that has not expired at
// now.
func (s *Store) webSessionValid(id string, now time.Time) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM web_sessions WHERE sha = ? AND expires_at > ?`, sha256Hex(id), ms(now)).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("server: session: %w", err)
	}
	return n == 1, nil
}

func (s *Store) deleteWebSession(id string) error {
	if _, err := s.db.Exec(`DELETE FROM web_sessions WHERE sha = ?`, sha256Hex(id)); err != nil {
		return fmt.Errorf("server: delete session: %w", err)
	}
	return nil
}

// DeleteExpiredSessions deletes web sessions expired at now and returns how
// many it deleted.
func (s *Store) DeleteExpiredSessions(now time.Time) (int, error) {
	res, err := s.db.Exec(`DELETE FROM web_sessions WHERE expires_at <= ?`, ms(now))
	if err != nil {
		return 0, fmt.Errorf("server: delete expired sessions: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// csrfToken is HMAC-SHA256(session id) keyed with the admin CSRF key, hex.
func csrfToken(key []byte, sessionID string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(sessionID))
	return hex.EncodeToString(m.Sum(nil))
}

// session returns the request's valid session id, or "" when the cookie is
// missing, unknown or expired. Bearer tokens are never consulted.
func (a *App) session(r *http.Request) string {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return ""
	}
	ok, err := a.store.webSessionValid(c.Value, a.now())
	if err != nil || !ok {
		return ""
	}
	return c.Value
}

// csrfFor returns the CSRF token for sessionID under the current admin key.
func (a *App) csrfFor(sessionID string) (string, error) {
	adm, err := a.store.Admin()
	if err != nil {
		return "", err
	}
	return csrfToken(adm.CSRFKey, sessionID), nil
}

// checkCSRF reports whether the request's X-CSRF-Token matches sessionID.
func (a *App) checkCSRF(r *http.Request, sessionID string) bool {
	got := r.Header.Get("X-CSRF-Token")
	if got == "" {
		return false
	}
	want, err := a.csrfFor(sessionID)
	if err != nil {
		return false
	}
	return hmac.Equal([]byte(got), []byte(want))
}

func (a *App) secureCookies() bool { return strings.HasPrefix(a.publicURL, "https://") }

func isFormPost(r *http.Request) bool {
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return mt == "application/x-www-form-urlencoded" || mt == "multipart/form-data"
}

// login: POST /auth/login. The token comes as form field "token" or JSON
// {"token"}. Failures count per IP: 10 per 15 minutes, then 429.
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	form := isFormPost(r)
	fail := func(status int, code, msg string) {
		if form {
			a.loginPage(w, status, msg)
			return
		}
		writeError(w, status, code)
	}
	ip := clientIP(r, a.trusted)
	now := a.now()
	// Reserve the attempt as a failure up front so a parallel burst cannot
	// exceed the limit. A wrong token keeps it; a success clears the
	// client's count; anything else (bad body, internal error) releases it.
	release, ok := a.loginLimiter.reserve(ip, now)
	if !ok {
		fail(http.StatusTooManyRequests, "too_many_attempts", "Too many attempts. Try again later.")
		return
	}
	counted := false
	defer func() {
		if !counted {
			release()
		}
	}()
	r.Body = http.MaxBytesReader(w, r.Body, loginMaxBody)
	var token string
	if form {
		if err := r.ParseForm(); err != nil {
			fail(http.StatusBadRequest, "bad_request", "Bad request.")
			return
		}
		token = r.PostForm.Get("token")
	} else {
		var body struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			fail(http.StatusBadRequest, "bad_request", "Bad request.")
			return
		}
		token = body.Token
	}
	adm, err := a.store.Admin()
	if err != nil && !errors.Is(err, ErrNotFound) {
		fail(http.StatusInternalServerError, "internal", "Internal error.")
		return
	}
	got := sha256Hex(token)
	if adm == nil || token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(adm.TokenSHA)) != 1 {
		counted = true
		fail(http.StatusUnauthorized, "invalid_token", "Invalid token.")
		return
	}
	id, err := a.store.createWebSession(now)
	if err != nil {
		fail(http.StatusInternalServerError, "internal", "Internal error.")
		return
	}
	counted = true // cleared below; nothing to release
	a.loginLimiter.clear(ip)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    id,
		Path:     "/",
		MaxAge:   int(SessionTTL / time.Second),
		Expires:  now.Add(SessionTTL),
		HttpOnly: true,
		Secure:   a.secureCookies(),
		SameSite: http.SameSiteStrictMode,
	})
	if form {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "csrf_token": csrfToken(adm.CSRFKey, id)})
}

// logout: POST /auth/logout deletes the session and clears the cookie.
func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		if err := a.store.deleteWebSession(c.Value); err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   a.secureCookies(),
		SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// me: GET /api/v1/me.
func (a *App) me(w http.ResponseWriter, r *http.Request, sessionID string) {
	tok, err := a.csrfFor(sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"login": adminLogin, "csrf_token": tok})
}

// loginTmpl is the self-contained login page: no scripts, styles or
// external assets, so it works under "default-src 'self'".
var loginTmpl = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>gtb login</title>
</head>
<body>
<h1>gtb dashboard</h1>
{{if .}}<p role="alert"><strong>{{.}}</strong></p>{{end}}
<form method="post" action="/auth/login">
<label for="token">Admin token</label><br>
<input id="token" name="token" type="password" autocomplete="current-password" required autofocus size="48"><br><br>
<button type="submit">Log in</button>
</form>
</body>
</html>
`))

func (a *App) loginPage(w http.ResponseWriter, status int, msg string) {
	setHTMLHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = loginTmpl.Execute(w, msg)
}

// setHTMLHeaders sets the security headers every HTML response carries.
func setHTMLHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "default-src 'self'")
	h.Set("Cache-Control", "no-store")
}
