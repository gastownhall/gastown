package refinery

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

	"github.com/gastownhall/gastown/internal/refinery/engineer"
)

// ... (rest of the file remains the same)

func TestEngineerClearAgentActiveMRUsesTownBeadsDir(t *testing.T) {
	// Update the test to expect the new BEADS_DIR routing
	want := "town/BEADS_DIR"
	got := engineer.ClearAgentActiveMRUsesTownBeadsDir()
	if got != want {
		t.Errorf("ClearAgentActiveMRUsesTownBeadsDir() returned %q, want %q", got, want)
	}
}

// ... (rest of the file remains the same)