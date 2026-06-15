package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gastownhall/gastown/internal/cmd/convoy/bd"
	"github.com/gastownhall/gastown/internal/cmd/convoy/dep"
	"github.com/gastownhall/gastown/internal/cmd/convoy/issue"
	"github.com/gastownhall/gastown/internal/cmd/convoy/ref"
	"github.com/gastownhall/gastown/internal/cmd/convoy/track"
	"github.com/gastownhall/gastown/internal/cmd/convoy/util"
)

// ... (rest of the file remains the same)

func bdDepListTracked(convoyID string, direction string, trackType string) ([]byte, error) {
	// Update the command to use the new argument form
	cmd := fmt.Sprintf("bd dep list %s --direction=%s --type=%s --json", convoyID, direction, trackType)
	return runBdJSONAllowStale(cmd)
}

// ... (rest of the file remains the same)