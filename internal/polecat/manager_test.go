package polecat

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

	"github.com/gastownhall/gastown/internal/polecat/manager"
)

// ... (rest of the file remains the same)

func TestManagerAgentLifecycleUsesTownBeadsDir(t *testing.T) {
	// Update the test to expect the new BEADS_DIR routing
	want := "town/BEADS_DIR"
	got := manager.AgentLifecycleUsesTownBeadsDir()
	if got != want {
		t.Errorf("AgentLifecycleUsesTownBeadsDir() returned %q, want %q", got, want)
	}
}

// ... (rest of the file remains the same)