// ABOUTME: `gtb server` runs the gtb server (agent API, dashboard auth,
// ABOUTME: browser API and embedded UI); `gtb server admin-token` manages login.

package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/server"
)

var (
	serverListen    string
	serverData      string
	serverPublicURL string
	serverRotate    bool
	serverTrusted   []string

	// serverReady, when set (tests), is called with the bound address.
	serverReady func(addr string)
)

var serverCmd = &cobra.Command{
	Use:     "server",
	GroupID: GroupServices,
	Short:   "Run the gtb server (machine link, dashboard and browser API)",
	Long: `Run the gtb server: the /agent/v1/ API that enrolled machines call, the
dashboard login and /api/v1/ browser API, and the embedded dashboard UI.

The store lives at <data>/gtb.db.

Dashboard login uses an admin token. On first start, if GTB_ADMIN_TOKEN is
not set, the server generates one and prints it once to stdout. If
GTB_ADMIN_TOKEN is set (at least 32 characters), its hash is stored and
nothing is printed; a different GTB_ADMIN_TOKEN on a later start replaces
the stored token. Use 'gtb server admin-token --rotate' to replace it.

Behind a reverse proxy, pass its address range with --trusted-proxy (repeatable)
or GTB_TRUSTED_PROXIES (comma-separated CIDRs) so login and enrollment rate
limits apply per client (from X-Forwarded-For) rather than to the proxy.

The server stops gracefully on SIGTERM or SIGINT.`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, os.Interrupt)
		defer stop()
		return runServerCtx(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

var serverAdminTokenCmd = &cobra.Command{
	Use:   "admin-token",
	Short: "Show or rotate the dashboard admin token",
	Long: `Without --rotate, report whether an admin token is set. With --rotate,
generate a new admin token and CSRF key, revoke every dashboard session, and
print the new token once.

Run it against the server's --data directory. A running server keeps working;
its dashboard sessions end and the old token stops logging in.`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runServerAdminToken,
}

func init() {
	serverCmd.Flags().StringVar(&serverListen, "listen", ":8080", "Address to listen on")
	serverCmd.PersistentFlags().StringVar(&serverData, "data", "/data", "Data directory (the store is <data>/gtb.db)")
	serverCmd.PersistentFlags().StringVar(&serverPublicURL, "public-url", "http://localhost:8080", "Public base URL of the dashboard (https makes cookies Secure)")
	serverCmd.Flags().StringArrayVar(&serverTrusted, "trusted-proxy", nil, "CIDR of a reverse proxy whose X-Forwarded-For is trusted (repeatable; also GTB_TRUSTED_PROXIES)")
	serverAdminTokenCmd.Flags().BoolVar(&serverRotate, "rotate", false, "Generate and print a new admin token, revoking all sessions")
	serverCmd.AddCommand(serverAdminTokenCmd)
	rootCmd.AddCommand(serverCmd)
}

func openServerStore() (*server.Store, error) {
	if serverData == "" {
		return nil, errors.New("--data is required")
	}
	return server.OpenStore(filepath.Join(serverData, "gtb.db"))
}

// runServerCtx runs the server until ctx is done. The admin token frame (if
// any) goes to out; logs go to errOut.
func runServerCtx(ctx context.Context, out, errOut io.Writer) error {
	logger := log.New(errOut, "gtb server: ", log.LstdFlags)
	envToken := os.Getenv("GTB_ADMIN_TOKEN")
	if envToken != "" && len(envToken) < server.MinAdminTokenLen {
		return fmt.Errorf("GTB_ADMIN_TOKEN must be at least %d characters; refusing to start", server.MinAdminTokenLen)
	}
	trusted, err := trustedProxies()
	if err != nil {
		return err
	}
	st, err := openServerStore()
	if err != nil {
		return err
	}
	defer st.Close()
	if err := server.EnsureAdminToken(st, envToken, serverPublicURL, out, logger.Printf); err != nil {
		return err
	}
	return server.Serve(ctx, st, server.ServeOptions{
		Listen:         serverListen,
		PublicURL:      serverPublicURL,
		TrustedProxies: trusted,
		Logf:           logger.Printf,
		Ready:          serverReady,
	})
}

// trustedProxies parses --trusted-proxy and GTB_TRUSTED_PROXIES together.
func trustedProxies() ([]netip.Prefix, error) {
	all := append([]string{}, serverTrusted...)
	if env := os.Getenv("GTB_TRUSTED_PROXIES"); env != "" {
		all = append(all, strings.Split(env, ",")...)
	}
	ps, err := server.ParseTrustedProxies(all)
	if err != nil {
		return nil, fmt.Errorf("%w; refusing to start", err)
	}
	return ps, nil
}

func runServerAdminToken(cmd *cobra.Command, _ []string) error {
	st, err := openServerStore()
	if err != nil {
		return err
	}
	defer st.Close()
	out := cmd.OutOrStdout()
	if serverRotate {
		return server.RotateAdminToken(st, serverPublicURL, out)
	}
	a, err := st.Admin()
	if errors.Is(err, server.ErrNotFound) {
		fmt.Fprintln(out, "no admin token is set; it is created on the next 'gtb server' start (or use --rotate)")
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "an admin token is set (created %s); use --rotate to replace it\n", a.CreatedAt.Local().Format(time.RFC3339))
	return nil
}
