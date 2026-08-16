package process

import (
	"os"
	"testing"
)

func TestAlive_CurrentProcess(t *testing.T) {
	if !Alive(os.Getpid()) {
		t.Fatal("current process should be alive")
	}
}

func TestAlive_MissingPID(t *testing.T) {
	if Alive(0) || Alive(-1) || Alive(99999999) {
		t.Fatal("missing pids must be dead")
	}
}
