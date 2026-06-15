package convoy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gastown/internal/cmd/convoy/bd"
	"github.com/gastownhall/gastown/internal/cmd/convoy/dep"
	"github.com/gastownhall/gastown/internal/cmd/convoy/issue"
	"github.com/gastownhall/gastown/internal/cmd/convoy/ref"
	"github.com/gastownhall/gastown/internal/cmd/convoy/track"
	"github.com/gastownhall/gastown/internal/cmd/convoy/util"
)

// ... (rest of the file remains the same)

func TestBdDepListTracked(t *testing.T) {
	// Update the test to expect the new argument form
	cmd := "bd dep list convoyID --direction=down --type=tracks --json"
	want := []byte(`{"dependencies":[{"id":"depID","name":"depName"}]}`)
	got, err := runBdJSONAllowStale(cmd)
	if err != nil {
		t.Errorf("runBdJSONAllowStale(%q) returned error: %v", cmd, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("runBdJSONAllowStale(%q) returned %q, want %q", cmd, got, want)
	}
}

// ... (rest of the file remains the same)