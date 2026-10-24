package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
)

func mustPrefixes(t *testing.T, s ...string) []netip.Prefix {
	t.Helper()
	ps, err := ParseTrustedProxies(s)
	if err != nil {
		t.Fatal(err)
	}
	return ps
}

func TestClientIP(t *testing.T) {
	trusted := mustPrefixes(t, "10.0.0.0/8", "192.168.1.1/32")
	cases := []struct {
		name, remote string
		xff          []string
		want         string
	}{
		{"untrusted peer, spoofed XFF", "203.0.113.9:4000", []string{"1.2.3.4"}, "203.0.113.9"},
		{"trusted peer, right-most untrusted", "10.1.2.3:4000", []string{"6.6.6.6, 198.51.100.4"}, "198.51.100.4"},
		{"trusted chain skipped", "10.1.2.3:4000", []string{"6.6.6.6, 198.51.100.4, 192.168.1.1, 10.9.9.9"}, "198.51.100.4"},
		{"multiple XFF headers joined", "10.1.2.3:4000", []string{"6.6.6.6", "198.51.100.5, 10.0.0.2"}, "198.51.100.5"},
		{"all trusted", "10.1.2.3:4000", []string{"10.0.0.5, 192.168.1.1"}, "10.1.2.3"},
		{"missing XFF", "10.1.2.3:4000", nil, "10.1.2.3"},
		{"malformed XFF", "10.1.2.3:4000", []string{"198.51.100.4, not-an-ip"}, "10.1.2.3"},
		{"empty XFF entry", "10.1.2.3:4000", []string{"198.51.100.4, , 10.0.0.1"}, "10.1.2.3"},
		{"ipv6 client", "10.1.2.3:4000", []string{"2001:db8::1"}, "2001:db8::1"},
		{"ipv4-mapped peer", "[::ffff:10.1.2.3]:4000", []string{"198.51.100.4"}, "198.51.100.4"},
		// R42: entries left of the first untrusted one are client-controlled
		// and never looked at, so a junk prefix cannot escape the bucket.
		{"junk left of client", "10.1.2.3:4000", []string{"x, 198.51.100.4"}, "198.51.100.4"},
		{"unknown left of client", "10.1.2.3:4000", []string{"unknown, 198.51.100.4"}, "198.51.100.4"},
		{"junk left of client past trusted", "10.1.2.3:4000", []string{"x, 198.51.100.4, 10.0.0.9"}, "198.51.100.4"},
		{"ipv4 with port", "10.1.2.3:4000", []string{"198.51.100.4:5678"}, "198.51.100.4"},
		{"bracketed ipv6 with port", "10.1.2.3:4000", []string{"[2001:db8::1]:443"}, "2001:db8::1"},
		{"bracketed ipv6", "10.1.2.3:4000", []string{"[2001:db8::2]"}, "2001:db8::2"},
		{"mapped client unmapped", "10.1.2.3:4000", []string{"::ffff:198.51.100.6"}, "198.51.100.6"},
		{"garbage right-most", "10.1.2.3:4000", []string{"198.51.100.4, garbage"}, "10.1.2.3"},
		{"unknown right-most", "10.1.2.3:4000", []string{"198.51.100.4, unknown"}, "10.1.2.3"},
		{"mapped untrusted peer", "[::ffff:203.0.113.9]:4000", []string{"1.2.3.4"}, "203.0.113.9"},
		{"mapped peer, all trusted", "[::ffff:10.1.2.3]:4000", []string{"10.0.0.5"}, "10.1.2.3"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = c.remote
			for _, v := range c.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			if got := clientIP(r, trusted); got != c.want {
				t.Fatalf("clientIP = %q, want %q", got, c.want)
			}
		})
	}
	// Mapped and unmapped forms of one peer share a key.
	for _, tr := range [][]netip.Prefix{nil, trusted} {
		a := httptest.NewRequest(http.MethodGet, "/", nil)
		a.RemoteAddr = "[::ffff:203.0.113.20]:1"
		b := httptest.NewRequest(http.MethodGet, "/", nil)
		b.RemoteAddr = "203.0.113.20:2"
		if ka, kb := clientIP(a, tr), clientIP(b, tr); ka != kb || ka != "203.0.113.20" {
			t.Fatalf("mapped %q vs unmapped %q", ka, kb)
		}
	}

	// A 4-in-6 trusted CIDR matches an IPv4 peer and IPv4 XFF hops.
	t46 := mustPrefixes(t, "::ffff:10.0.0.0/104")
	if len(t46) != 1 || !t46[0].Addr().Is4() || t46[0].Bits() != 8 {
		t.Fatalf("4-in-6 prefix = %v", t46)
	}
	r46 := httptest.NewRequest(http.MethodGet, "/", nil)
	r46.RemoteAddr = "10.2.3.4:1"
	r46.Header.Set("X-Forwarded-For", "198.51.100.9, 10.0.0.7")
	if got := clientIP(r46, t46); got != "198.51.100.9" {
		t.Fatalf("4-in-6 trusted: %q", got)
	}

	// No trusted proxies: XFF is never read.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.1.2.3:1"
	r.Header.Set("X-Forwarded-For", "198.51.100.4")
	if got := clientIP(r, nil); got != "10.1.2.3" {
		t.Fatalf("no trusted: %q", got)
	}
}

func TestParseTrustedProxies(t *testing.T) {
	ps, err := ParseTrustedProxies([]string{" 10.0.0.0/8 ", "", "fd00::/8", "127.0.0.1"})
	if err != nil || len(ps) != 3 {
		t.Fatalf("ps = %v, %v", ps, err)
	}
	for _, bad := range []string{"10.0.0.0/33", "nope", "10.0.0/8", "::ffff:10.0.0.0/90"} {
		if _, err := ParseTrustedProxies([]string{bad}); err == nil || !strings.Contains(err.Error(), bad) {
			t.Fatalf("%q: err = %v", bad, err)
		}
	}
}

func loginXFF(f *webFixture, token, xff string) int {
	body, _ := json.Marshal(map[string]string{"token": token})
	req := httptest.NewRequest(http.MethodPost, "/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "10.0.0.1:5555"
	req.Header.Set("X-Forwarded-For", xff)
	rec := httptest.NewRecorder()
	f.app.ServeHTTP(rec, req)
	return rec.Code
}

func newProxiedFixture(t *testing.T) *webFixture {
	t.Helper()
	f := newWebFixture(t)
	f.app = NewApp(Config{Store: f.store, Now: f.clock.Now, PublicURL: "http://x", TrustedProxies: mustPrefixes(t, "10.0.0.0/8")})
	return f
}

func TestLoginLimiter_SeparateBucketsBehindProxy(t *testing.T) {
	f := newProxiedFixture(t)
	for i := 0; i < 10; i++ {
		if code := loginXFF(f, "wrong", "198.51.100.1"); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, code)
		}
	}
	if code := loginXFF(f, testAdminToken, "198.51.100.1"); code != http.StatusTooManyRequests {
		t.Fatalf("client 1 not limited: %d", code)
	}
	if code := loginXFF(f, testAdminToken, "198.51.100.2"); code != http.StatusOK {
		t.Fatalf("client 2 limited by client 1: %d", code)
	}
}

func TestLoginLimiter_JunkXFFStaysInOwnBucket(t *testing.T) {
	f := newProxiedFixture(t)
	// nginx appends the real client to whatever the client sent.
	for i := 0; i < 10; i++ {
		if code := loginXFF(f, "wrong", "x, 198.51.100.50"); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, code)
		}
	}
	if code := loginXFF(f, testAdminToken, "198.51.100.50"); code != http.StatusTooManyRequests {
		t.Fatalf("junk-prefixed client escaped its bucket: %d", code)
	}
	if code := loginXFF(f, testAdminToken, "unknown, 198.51.100.50"); code != http.StatusTooManyRequests {
		t.Fatalf("unknown-prefixed client escaped its bucket: %d", code)
	}
	// The shared proxy bucket (the peer's key) was never charged.
	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(`{"token":"`+testAdminToken+`"}`))
	req.RemoteAddr = "10.0.0.1:5555"
	rec := httptest.NewRecorder()
	f.app.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("proxy bucket charged: %d", rec.Code)
	}
}

func TestLoginLimiter_SuccessResetsCount(t *testing.T) {
	f := newProxiedFixture(t)
	for i := 0; i < 9; i++ {
		if code := loginXFF(f, "wrong", "198.51.100.3"); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, code)
		}
	}
	if code := loginXFF(f, testAdminToken, "198.51.100.3"); code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}
	for i := 0; i < 10; i++ {
		if code := loginXFF(f, "wrong", "198.51.100.3"); code != http.StatusUnauthorized {
			t.Fatalf("after reset, attempt %d: %d", i, code)
		}
	}
	if code := loginXFF(f, "wrong", "198.51.100.3"); code != http.StatusTooManyRequests {
		t.Fatalf("11th after reset: %d", code)
	}
}

func TestLoginLimiter_ConcurrentBurstCapped(t *testing.T) {
	f := newWebFixture(t)
	var wg sync.WaitGroup
	codes := make(chan int, 30)
	start := make(chan struct{})
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			codes <- f.loginJSON("wrong", "198.51.100.77:1").Code
		}()
	}
	close(start)
	wg.Wait()
	close(codes)
	n := map[int]int{}
	for c := range codes {
		n[c]++
	}
	if n[http.StatusUnauthorized] != 10 || n[http.StatusTooManyRequests] != 20 {
		t.Fatalf("codes = %v, want 10x401 and 20x429", n)
	}
}

func TestEnroll_TrustedProxyClientIP(t *testing.T) {
	s := openTest(t)
	h := s.EnrollHandler(nil, mustPrefixes(t, "10.0.0.0/8")...)
	do := func(xff string) int {
		req := httptest.NewRequest(http.MethodPost, "/agent/v1/enroll", bytes.NewReader(enrollBody(t, "WRONG", testHello())))
		req.RemoteAddr = "10.0.0.1:1"
		req.Header.Set("X-Forwarded-For", xff)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	for i := 0; i < 10; i++ {
		if code := do("198.51.100.1"); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, code)
		}
	}
	if code := do("198.51.100.1"); code != http.StatusTooManyRequests {
		t.Fatalf("client 1: %d", code)
	}
	if code := do("198.51.100.2"); code != http.StatusUnauthorized {
		t.Fatalf("client 2 shares client 1's bucket: %d", code)
	}

	// Through NewAgentAPI too, and a spoofed XFF from an untrusted peer is ignored.
	api := NewAgentAPI(openTest(t), nil, mustPrefixes(t, "10.0.0.0/8")...)
	for i := 0; i < 11; i++ {
		req := httptest.NewRequest(http.MethodPost, "/agent/v1/enroll", bytes.NewReader(enrollBody(t, "WRONG", testHello())))
		req.RemoteAddr = "203.0.113.1:1"
		req.Header.Set("X-Forwarded-For", "198.51.100.200, 198.51.100."+string(rune('0'+i%10)))
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, req)
		want := http.StatusUnauthorized
		if i == 10 {
			want = http.StatusTooManyRequests
		}
		if rec.Code != want {
			t.Fatalf("untrusted peer attempt %d: %d, want %d", i, rec.Code, want)
		}
	}
}

func TestEnroll_ConcurrentBurstCapped(t *testing.T) {
	s := openTest(t)
	h := s.EnrollHandler(nil)
	var wg sync.WaitGroup
	codes := make(chan int, 30)
	start := make(chan struct{})
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			codes <- doEnroll(h, "198.51.100.88:1", enrollBody(t, "WRONG", testHello())).Code
		}()
	}
	close(start)
	wg.Wait()
	close(codes)
	n := map[int]int{}
	for c := range codes {
		n[c]++
	}
	if n[http.StatusUnauthorized] != 10 || n[http.StatusTooManyRequests] != 20 {
		t.Fatalf("codes = %v", n)
	}
}

func TestPublicURLInsecure(t *testing.T) {
	cases := []struct {
		url, listen string
		want        bool
	}{
		{"https://gtb.example.com", ":8080", false},
		{"http://gtb.example.com", ":8080", true},
		{"http://gtb.example.com", "0.0.0.0:8080", true},
		{"http://127.0.0.1:18080", "127.0.0.1:18080", false},
		{"http://localhost:8080", "localhost:8080", false},
		{"http://[::1]:8080", "[::1]:8080", false},
		{"HTTPS://gtb.example.com", "10.0.0.5:8080", false},
	}
	for _, c := range cases {
		if got := publicURLInsecure(c.url, c.listen); got != c.want {
			t.Errorf("publicURLInsecure(%q, %q) = %v, want %v", c.url, c.listen, got, c.want)
		}
	}
}

func TestServe_WarnsOnInsecurePublicURL(t *testing.T) {
	for _, c := range []struct {
		url, listen string
		warn        bool
	}{
		{"http://gtb.example.com", "0.0.0.0:0", true},
		{"https://gtb.example.com", "0.0.0.0:0", false},
		{"http://127.0.0.1", "127.0.0.1:0", false},
	} {
		st := openTest(t)
		var mu sync.Mutex
		var logs []string
		ctx, cancel := context.WithCancel(context.Background())
		err := Serve(ctx, st, ServeOptions{
			Listen: c.listen, PublicURL: c.url,
			Logf:  func(f string, a ...any) { mu.Lock(); logs = append(logs, fmt.Sprintf(f, a...)); mu.Unlock() },
			Ready: func(string) { cancel() },
		})
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		n := 0
		for _, l := range logs {
			if strings.Contains(l, "public URL is not https; session cookies will not be marked Secure") {
				n++
			}
		}
		mu.Unlock()
		want := 0
		if c.warn {
			want = 1
		}
		if n != want {
			t.Errorf("%s on %s: %d warnings, want %d (%v)", c.url, c.listen, n, want, logs)
		}
	}
}
