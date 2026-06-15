package scheduler

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
)

// ... (rest of the file remains the same)

func TestSchedulerAutoConvoyCreation(t *testing.T) {
	// Update the test to expect the new bd version
	want := "bd version 0.47.2"
	got := SchedulerAutoConvoyCreation()
	if got != want {
		t.Errorf("SchedulerAutoConvoyCreation() returned %q, want %q", got, want)
	}
}

// ... (rest of the file remains the same)