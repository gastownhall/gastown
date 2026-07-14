package gitlab

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestFindOpenMR(t *testing.T) {
	c, srv := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("PRIVATE-TOKEN"); got != "test-token" {
			t.Errorf("PRIVATE-TOKEN = %q, want test-token", got)
		}
		if got := r.URL.EscapedPath(); got != "/projects/group%2Fproject/merge_requests" {
			t.Errorf("path = %q", got)
		}
		q := r.URL.Query()
		if q.Get("state") != "opened" || q.Get("source_branch") != "feature" || q.Get("target_branch") != "main" {
			t.Errorf("query = %v", q)
		}
		_ = json.NewEncoder(w).Encode([]MergeRequest{{IID: 36, State: "opened", SourceBranch: "feature", TargetBranch: "main"}})
	}))
	defer srv.Close()

	mr, err := c.FindOpenMR(context.Background(), "group/project", "feature", "main")
	if err != nil {
		t.Fatalf("FindOpenMR: %v", err)
	}
	if mr.IID != 36 {
		t.Errorf("IID = %d, want 36", mr.IID)
	}
}

func TestFindOpenMR_AmbiguousIsError(t *testing.T) {
	c, srv := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]MergeRequest{{IID: 1}, {IID: 2}})
	}))
	defer srv.Close()

	if _, err := c.FindOpenMR(context.Background(), "group/project", "feature", "main"); err == nil {
		t.Fatal("FindOpenMR() with two matches = nil error, want ambiguity error")
	}
}

func TestFindOpenMR_NoneIsError(t *testing.T) {
	c, srv := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]MergeRequest{})
	}))
	defer srv.Close()

	if _, err := c.FindOpenMR(context.Background(), "group/project", "feature", "main"); err == nil {
		t.Fatal("FindOpenMR() with no matches = nil error, want not-found error")
	}
}

func TestMergeMR(t *testing.T) {
	c, srv := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		if got := r.URL.EscapedPath(); got != "/projects/group%2Fproject/merge_requests/36/merge" {
			t.Errorf("path = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["squash"] != true {
			t.Errorf("squash = %v, want true", body["squash"])
		}
		if body["squash_commit_message"] != "feat: thing" {
			t.Errorf("squash_commit_message = %v", body["squash_commit_message"])
		}
		_ = json.NewEncoder(w).Encode(MergeRequest{IID: 36, State: "merged", SquashCommitSHA: "abc123"})
	}))
	defer srv.Close()

	mr, err := c.MergeMR(context.Background(), "group/project", 36, MergeOptions{Squash: true, SquashCommitMessage: "feat: thing"})
	if err != nil {
		t.Fatalf("MergeMR: %v", err)
	}
	if mr.MergedSHA() != "abc123" {
		t.Errorf("MergedSHA = %q, want abc123", mr.MergedSHA())
	}
}

func TestMergeMR_APIErrorSurfacesStatus(t *testing.T) {
	c, srv := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMethodNotAllowed) // 405: MR not in a mergeable state
		_, _ = w.Write([]byte(`{"message":"405 Method Not Allowed"}`))
	}))
	defer srv.Close()

	_, err := c.MergeMR(context.Background(), "group/project", 36, MergeOptions{Squash: true})
	if err == nil {
		t.Fatal("MergeMR() on 405 = nil error, want error")
	}
	if got := StatusCodeOf(err); got != http.StatusMethodNotAllowed {
		t.Errorf("StatusCodeOf = %d, want 405", got)
	}
}
