package cmd

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"time"

	"golang.org/x/term"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/web"
	"github.com/steveyegge/gastown/internal/workspace"
)

var (
	dashboardPort int
	dashboardBind string
	dashboardOpen bool
)

var dashboardCmd = &cobra.Command{
	Use:     "dashboard",
	GroupID: GroupDiag,
	Short:   "Start the convoy tracking web dashboard",
	Long: `Start a web server that displays the convoy tracking dashboard.

The dashboard shows real-time convoy status with:
- Convoy list with status indicators
- Progress tracking for each convoy
- Last activity indicator (green/yellow/red)
- Auto-refresh every 30 seconds via htmx

Example:
  gt dashboard                    # Start on default port 8080
  gt dashboard --port 3000        # Start on port 3000
  gt dashboard --bind 0.0.0.0     # Listen on all interfaces
  gt dashboard --open             # Start and open browser`,
	RunE: runDashboard,
}

func init() {
	dashboardCmd.Flags().IntVar(&dashboardPort, "port", 8080, "HTTP port to listen on")
	defaultBind := "127.0.0.1"
	if os.Getenv("IS_SANDBOX") != "" {
		defaultBind = "0.0.0.0"
	}
	dashboardCmd.Flags().StringVar(&dashboardBind, "bind", defaultBind, "Address to bind to (use 0.0.0.0 for all interfaces)")
	dashboardCmd.Flags().BoolVar(&dashboardOpen, "open", false, "Open browser automatically")
	rootCmd.AddCommand(dashboardCmd)
}

func runDashboard(cmd *cobra.Command, args []string) error {
	// Check if we're in a workspace - if not, run in setup mode
	var handler http.Handler
	var err error
	webCfg := config.DefaultWebTimeoutsConfig()

	townRoot, wsErr := workspace.FindFromCwdOrError()
	if wsErr != nil {
		// No workspace - run in setup mode
		handler, err = web.NewSetupMux()
		if err != nil {
			return fmt.Errorf("creating setup handler: %w", err)
		}
	} else {
		// In a workspace - run normal dashboard

		// Set BEADS_DOLT_PORT and GT_DOLT_PORT so bd/gt subprocesses connect
		// to the actual Dolt SQL server, not the dashboard's HTTP listen port.
		// Without this, inherited env vars could point bd at the wrong port.
		ensureDoltPortEnv(townRoot)

		fetcher, fetchErr := web.NewLiveConvoyFetcher()
		if fetchErr != nil {
			return fmt.Errorf("creating convoy fetcher: %w", fetchErr)
		}

		// Load web timeouts config (nil-safe: NewDashboardMux applies defaults)
		if ts, loadErr := config.LoadOrCreateTownSettings(config.TownSettingsPath(townRoot)); loadErr == nil {
			if ts.WebTimeouts != nil {
				webCfg = ts.WebTimeouts
			}
		} else {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: loading town settings: %v (using defaults)\n", loadErr)
		}

		handler, err = web.NewDashboardMux(fetcher, webCfg)
		if err != nil {
			return fmt.Errorf("creating dashboard handler: %w", err)
		}
	}

	// Build the listen address and display URL
	listenAddr := fmt.Sprintf("%s:%d", dashboardBind, dashboardPort)
	displayHost := dashboardBind
	if displayHost == "0.0.0.0" {
		if hostname, err := os.Hostname(); err == nil {
			displayHost = hostname
		} else {
			displayHost = "localhost"
		}
	}
	url := fmt.Sprintf("http://%s:%d", displayHost, dashboardPort)

	// Open browser if requested
	if dashboardOpen {
		go openBrowser(url)
	}

	maxRunTimeout := config.ParseDurationOrDefault(webCfg.MaxRunTimeout, 120*time.Second)
	writeTimeout := maxRunTimeout + 15*time.Second
	if writeTimeout < 60*time.Second {
		writeTimeout = 60 * time.Second
	}

	// Start the server with timeouts
	// Only show the large banner if the terminal is wide enough (98 cols)
	width, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err == nil && width >= 98 {
		fmt.Print(`
 __       __  ________  __        ______    ______   __       __  ________
|  \  _  |  \|        \|  \      /      \  /      \ |  \     /  \|        \
| $$ / \ | $$| $$$$$$$$| $$     |  $$$$$$\|  $$$$$$\| $$\   /  $$| $$$$$$$$
| $$/  $\| $$| $$__    | $$     | $$   \$$| $$  | $$| $$$\ /  $$$| $$__
| $$  $$$\ $$| $$  \   | $$     | $$      | $$  | $$| $$$$\  $$$$| $$  \
| $$ $$\$$\$$| $$$$$   | $$     | $$   __ | $$  | $$| $$\$$ $$ $$| $$$$$
| $$$$  \$$$$| $$_____ | $$_____| $$__/  \| $$__/ $$| $$ \$$$| $$| $$_____
| $$$    \$$$| $$     \| $$     \\$$    $$ \$$    $$| $$  \$ | $$| $$     \
 \$$      \$$ \$$$$$$$$ \$$$$$$$$ \$$$$$$   \$$$$$$  \$$      \$$ \$$$$$$$$

 ________   ______          ______    ______    ______   ________   ______   __       __  __    __
|        \ /      \        /      \  /      \  /      \ |        \ /      \ |  \  _  |  \|  \  |  \
 \$$$$$$$$|  $$$$$$\      |  $$$$$$\|  $$$$$$\|  $$$$$$\ \$$$$$$$$|  $$$$$$\| $$ / \ | $$| $$\ | $$
   | $$   | $$  | $$      | $$ __\$$| $$__| $$| $$___\$$   | $$   | $$  | $$| $$/  $\| $$| $$$\| $$
   | $$   | $$  | $$      | $$|    \| $$    $$ \$$    \    | $$   | $$  | $$| $$  $$$\ $$| $$$$\ $$
   | $$   | $$  | $$      | $$ \$$$$| $$$$$$$$ _\$$$$$$\   | $$   | $$  | $$| $$ $$\$$\$$| $$\$$ $$
   | $$   | $$__/ $$      | $$__| $$| $$  | $$|  \__| $$   | $$   | $$__/ $$| $$$$  \$$$$| $$ \$$$$
   | $$    \$$    $$       \$$    $$| $$  | $$ \$$    $$   | $$    \$$    $$| $$$    \$$$| $$  \$$$
    \$$     \$$$$$$         \$$$$$$  \$$   \$$  \$$$$$$     \$$     \$$$$$$  \$$      \$$ \$$   \$$

`)
	} else {
		fmt.Print("\n  WELCOME TO GASTOWN\n\n")
	}
	fmt.Printf("  launching dashboard at %s  •  api: %s/api/  •  listening on %s  •  ctrl+c to stop\n", url, url, listenAddr)

	server := &http.Server{
		Addr:              listenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       120 * time.Second,
	}
	return server.ListenAndServe()
}

// ensureDoltPortEnv sets GT_DOLT_PORT, BEADS_DOLT_PORT, and BEADS_DOLT_SERVER_HOST
// to the actual Dolt server connection info. This prevents bd subprocesses from
// inheriting stale or incorrect values from the environment.
// Reads the running state from daemon/dolt-state.json; falls back to
// doltserver.DefaultConfig; otherwise uses the Dolt defaults.
func ensureDoltPortEnv(townRoot string) {
	var port int
	if state, err := doltserver.LoadState(townRoot); err == nil && state.Port > 0 {
		port = state.Port
	} else {
		port = doltserver.DefaultPort
	}
	portStr := strconv.Itoa(port)
	os.Setenv("GT_DOLT_PORT", portStr)
	os.Setenv("BEADS_DOLT_PORT", portStr)

	// Propagate host so bd doesn't fall back to 127.0.0.1.
	doltCfg := doltserver.DefaultConfig(townRoot)
	if doltCfg.Host != "" {
		os.Setenv("BEADS_DOLT_SERVER_HOST", doltCfg.Host)
	}
}

// openBrowser opens the specified URL in the default browser.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		return
	}
	_ = cmd.Start()
}
