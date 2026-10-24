// ABOUTME: `gtb join` enrolls this town's machine with a gtb server using a
// ABOUTME: one-time code and stores the returned machine token (0600).

package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/agentlink"
	"github.com/steveyegge/gastown/internal/beads"
)

var (
	joinCode  string
	joinForce bool
	joinJSON  bool
)

// joinHTTPClient is the client used to call the enroll endpoint. It never
// follows redirects, so the enrollment code is only sent to the URL the user
// gave. Tests may replace it.
var joinHTTPClient = &http.Client{
	Timeout: 15 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

const joinMaxResponse = 64 << 10

var joinCmd = &cobra.Command{
	Use:     "join <server-url> --code <code>",
	GroupID: GroupConfig,
	Short:   "Enroll this machine with a gtb server",
	Long: `Enroll this town's machine with a gtb server using a one-time code.

The server URL must be https://, or http:// to 127.0.0.1, localhost or [::1].
On success the machine token is saved to <town>/.runtime/agentlink/machine-token
(mode 0600) and the server URL and machine id to
<town>/.runtime/agentlink/machine.json. The token is never printed.

If this town already has a machine token, join refuses unless --force is given.`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runJoin,
}

func init() {
	joinCmd.Flags().StringVar(&joinCode, "code", "", "One-time enrollment code from the server (required)")
	joinCmd.Flags().BoolVar(&joinForce, "force", false, "Replace an existing machine token")
	joinCmd.Flags().BoolVar(&joinJSON, "json", false, "Output as JSON")
	_ = joinCmd.MarkFlagRequired("code")
	rootCmd.AddCommand(joinCmd)
}

type joinResult struct {
	ServerURL string `json:"server_url"`
	MachineID string `json:"machine_id"`
}

func runJoin(cmd *cobra.Command, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: gtb join <server-url> --code <code>")
	}
	server, err := agentlink.NormalizeServerURL(args[0])
	if err != nil {
		return err
	}
	if joinCode == "" {
		return errors.New("--code is required")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}
	townRoot := beads.FindTownRoot(cwd)
	if townRoot == "" {
		return errors.New("not in a Gas Town town (no mayor/town.json found); run gtb join from inside your town")
	}
	if agentlink.HasToken(townRoot) && !joinForce {
		return fmt.Errorf("this town already has a machine token (%s); use --force to replace it", agentlink.TokenPath(townRoot))
	}

	host, _ := os.Hostname()
	body, err := json.Marshal(map[string]any{
		"code": joinCode,
		"hello": agentlink.Hello{
			Hostname:   host,
			OS:         runtime.GOOS,
			Arch:       runtime.GOARCH,
			GtbVersion: Version,
			Protocol:   agentlink.ProtocolVersion,
		},
	})
	if err != nil {
		return fmt.Errorf("encoding enroll request: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, server+"/agent/v1/enroll", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building enroll request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := joinHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("contacting %s: %w", server, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, joinMaxResponse))
	if err != nil {
		return fmt.Errorf("reading enroll response from %s: %w", server, err)
	}

	// Never include the response body in errors: on success it holds the token.
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return errors.New("enrollment code is invalid, used or expired")
	case http.StatusTooManyRequests:
		return errors.New("too many attempts; wait and retry")
	case http.StatusBadRequest:
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error == "protocol_mismatch" {
			return fmt.Errorf("server %s does not support agentlink protocol %s; upgrade gtb", server, agentlink.ProtocolVersion)
		}
		return fmt.Errorf("server %s rejected the enroll request (HTTP 400)", server)
	default:
		return fmt.Errorf("server %s returned HTTP %d", server, resp.StatusCode)
	}

	var out struct {
		MachineID string `json:"machine_id"`
		Token     string `json:"token"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.MachineID == "" || out.Token == "" {
		return fmt.Errorf("server %s returned an invalid enroll response", server)
	}
	if err := agentlink.SaveToken(townRoot, server, out.MachineID, out.Token); err != nil {
		return err
	}

	if joinJSON {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(joinResult{ServerURL: server, MachineID: out.MachineID})
	}
	fmt.Fprintf(cmd.OutOrStdout(), "joined %s as machine %s\n", server, out.MachineID)
	return nil
}
