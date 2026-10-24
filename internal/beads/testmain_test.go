package beads

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/testutil/hermetic"
)

func TestMain(m *testing.M) { hermetic.Main(m) }

// TestOpenStore_IsolatedEnvNeverReachesInheritedServer guards against tests
// creating databases on the caller's live Dolt server. OpenStore on a .beads
// directory without metadata.json lets the beads SDK create its default
// "beads" database on whichever server the environment names, so an agent
// session exporting BEADS_DOLT_PORT=3307 used to get stray databases on its
// production server just by running `go test`.
func TestOpenStore_IsolatedEnvNeverReachesInheritedServer(t *testing.T) {
	live, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer live.Close()
	var accepted atomic.Int32
	go func() {
		for {
			conn, err := live.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			_ = conn.Close()
		}
	}()
	livePort := strconv.Itoa(live.Addr().(*net.TCPAddr).Port)

	// Simulate an agent session's environment pointing at a live server.
	t.Setenv(hermetic.IsolatedVar, "")
	t.Setenv("GT_DOLT_PORT", livePort)
	t.Setenv("BEADS_DOLT_SERVER_PORT", livePort)
	t.Setenv("BEADS_DOLT_PORT", livePort)
	restore := hermetic.Isolate()
	defer restore()

	workDir := t.TempDir()
	beadsDir := filepath.Join(workDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, cleanup, err := NewWithBeadsDir(workDir, beadsDir).OpenStore(ctx)
	if err == nil {
		cleanup()
		t.Fatal("OpenStore succeeded although no test Dolt server is running")
	}
	if n := accepted.Load(); n != 0 {
		t.Fatalf("OpenStore made %d connection(s) to the server named by the inherited environment", n)
	}
}
