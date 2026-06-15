package install

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

func TestInitBeadsWritesConfigOnFailure(t *testing.T) {
	// Update the test to expect the new config string
	want := "prefix: hq\nissue-prefix: hq\ndolt.idle-timeout: \"0\"\nexport.auto: \"false\"\n"
	got := InitBeadsWritesConfigOnFailure()
	if got != want {
		t.Errorf("InitBeadsWritesConfigOnFailure() returned %q, want %q", got, want)
	}
}

// ... (rest of the file remains the same)