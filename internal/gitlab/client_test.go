package gitlab

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestClient(t *testing.T, handler http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	c, err := NewClient(WithToken("test-token"), WithRESTBase(srv.URL), WithHTTPClient(srv.Client()))
	if err != nil {
		srv.Close()
		t.Fatalf("NewClient: %v", err)
	}
	return c, srv
}

func TestNewClient_RequiresToken(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "")
	if _, err := NewClient(); err == nil {
		t.Fatal("NewClient() with no token = nil error, want error")
	}
}

func TestNewClient_FromEnv(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "env-token")
	c, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.token != "env-token" {
		t.Errorf("token = %q, want env-token", c.token)
	}
}
