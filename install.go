// Package install contains the logic for initializing a Gas Town instance.
package install

import (
	"fmt"
	"os/exec"
	"strings"
)

// Init initializes the Gas Town instance.
func Init() error {
	cmd := exec.Command("bd", "init", "--reinit-local")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
