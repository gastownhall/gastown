package web

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// DashboardOptions controls optional integrations. The default is standalone.
type DashboardOptions struct {
	EmbedParentOrigin string
}

// ValidateEmbedParentOrigin accepts a single literal HTTPS origin, or HTTP on
// loopback for local Canvas installations. Paths, credentials and CSP wildcards
// are deliberately not supported. The value must also be a browser origin so
// it can be used unchanged as postMessage's targetOrigin.
func ValidateEmbedParentOrigin(origin string) error {
	if origin == "" {
		return nil
	}
	invalid := func() error {
		return fmt.Errorf("invalid embed parent origin: use a literal HTTPS origin or loopback HTTP origin without path, credentials, query or fragment")
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.User != nil || u.Opaque != "" || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(origin, "#?\\") {
		return invalid()
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if ip == nil && !regexp.MustCompile(`^[a-z0-9]+(?:[a-z0-9.-]*[a-z0-9])?$`).MatchString(host) {
		return invalid()
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (host == "localhost" || ip != nil && ip.IsLoopback())) {
		return invalid()
	}
	if strings.HasSuffix(u.Host, ":") {
		return invalid()
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port || u.Scheme == "http" && n == 80 || u.Scheme == "https" && n == 443 {
			return invalid()
		}
	}
	if origin != u.Scheme+"://"+u.Host {
		return invalid()
	}
	return nil
}

func dashboardFramePolicy(next http.Handler, parentOrigin string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The query opts this document into the server-configured integration;
		// it can never choose or override the trusted origin.
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'self'")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		if r.URL.Path == "/" && r.URL.Query().Get("embed") == "1" {
			if parentOrigin == "" {
				http.Error(w, "Embedded navigation is not configured", http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Security-Policy", "frame-ancestors "+parentOrigin)
			w.Header().Del("X-Frame-Options")
		}
		next.ServeHTTP(w, r)
	})
}
