package cmd

import (
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/jonbaldie/gastown/internal/doltserver"
)

// dsnOpts captures the optional MySQL DSN query parameters used by gt's
// internal Dolt-server connections. Empty / zero values are omitted from
// the resulting query string.
type dsnOpts struct {
	ParseTime    bool
	Timeout      string // e.g. "5s"
	ReadTimeout  string // e.g. "10s"
	WriteTimeout string // e.g. "30s"
}

func (o dsnOpts) queryString() string {
	var parts []string
	if o.ParseTime {
		parts = append(parts, "parseTime=true")
	}
	if o.Timeout != "" {
		parts = append(parts, "timeout="+o.Timeout)
	}
	if o.ReadTimeout != "" {
		parts = append(parts, "readTimeout="+o.ReadTimeout)
	}
	if o.WriteTimeout != "" {
		parts = append(parts, "writeTimeout="+o.WriteTimeout)
	}
	return strings.Join(parts, "&")
}

// localDoltSocketPath returns a live unix socket path for the given town
// and port, or "" when none is accepting connections.
//
// Managed towns listen on a hashed /tmp/gt-dolt-<id>.sock path. The
// historical Dolt defaults (/tmp/mysql.sock, /tmp/mysql.{port}.sock) remain
// as fallbacks for leftover servers that were started before isolation.
// Never treat /tmp/mysql.sock as this town's socket when a town-scoped
// path exists: that file is shared with other Dolt/MySQL servers.
//
// Declared as a var so unit tests can swap it for a temp-dir socket
// without depending on a real Dolt server.
var localDoltSocketPath = func(port int) string {
	return liveDoltSocketPath("", port)
}

func liveDoltSocketPath(townRoot string, port int) string {
	candidates := make([]string, 0, 3)
	if sock := doltserver.SocketPath(townRoot); sock != "" {
		candidates = append(candidates, sock)
	}
	if port != 0 && port != 3306 {
		candidates = append(candidates, fmt.Sprintf("/tmp/mysql.%d.sock", port))
	}
	// Only use the global default when no town-scoped candidate exists.
	if len(candidates) == 0 {
		candidates = append(candidates, doltserver.DefaultDoltSocketPath)
	}
	for _, p := range candidates {
		if usableUnixSocket(p) {
			return p
		}
	}
	return ""
}

func usableUnixSocket(p string) bool {
	info, err := os.Stat(p)
	if err != nil {
		return false
	}
	if info.Mode()&os.ModeSocket == 0 {
		return false
	}
	conn, err := net.DialTimeout("unix", p, 100*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func formatDoltDSN(user, network, address, dbName string, opts dsnOpts) string {
	if user == "" {
		user = "root"
	}
	qs := opts.queryString()
	dsn := fmt.Sprintf("%s@%s(%s)/%s", user, network, address, dbName)
	if qs == "" {
		return dsn
	}
	return dsn + "?" + qs
}

// buildDoltDSN produces a Go-MySQL-driver DSN that prefers the local
// Dolt unix domain socket when present, falling back to TCP loopback
// otherwise. The dbName, user, port, and query options are substituted
// into both forms.
//
// Rationale: short-lived gt-CLI subcommands over TCP loopback create a
// TIME_WAIT entry per close that lingers ~30s on macOS (2*MSL with
// MSL=15s). On busy rigs (background daemons, cron, periodic gt
// health/doctor/maintain invocations) the count climbs past
// port-monitor alert thresholds and risks port exhaustion. Unix-socket
// transport bypasses TIME_WAIT entirely.
//
// dc-fsue's metadata.json fix only covered the `bd` CLI side. wa-d6f
// tracks the remaining gt-CLI callsites that build their own DSNs
// inline (internal/cmd/health.go, maintain.go, dolt_flatten.go,
// dolt_rebase.go, install.go). This helper is the unified migration
// point so future callsites get socket-first transport for free.
//
// Conservative semantics: callers receive the TCP DSN whenever the
// default Dolt socket is not currently usable at the expected path
// (Windows, no Dolt running, custom socket path). No behavior change for
// setups without a local Dolt.
func buildDoltDSN(user string, port int, dbName string, opts dsnOpts) string {
	if sock := localDoltSocketPath(port); sock != "" {
		return formatDoltDSN(user, "unix", sock, dbName, opts)
	}
	return formatDoltDSN(user, "tcp", fmt.Sprintf("127.0.0.1:%d", port), dbName, opts)
}

// buildDoltDSNFromConfig is a convenience wrapper that pulls user, port,
// and host from a *doltserver.Config (matches the maintain.go /
// dolt_flatten.go / dolt_rebase.go callsite pattern).
func buildDoltDSNFromConfig(c *doltserver.Config, dbName string, opts dsnOpts) string {
	if !c.IsRemote() {
		if sock := liveDoltSocketPath(c.TownRoot, c.Port); sock != "" {
			return formatDoltDSN(c.User, "unix", sock, dbName, opts)
		}
	}
	return formatDoltDSN(c.User, "tcp", c.HostPort(), dbName, opts)
}
