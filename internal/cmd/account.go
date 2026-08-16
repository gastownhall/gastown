package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/constants"
	"github.com/jonbaldie/gastown/internal/skills"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

// Account command flags
var (
	accountJSON        bool
	accountEmail       string
	accountDescription string
)

var accountCmd = &cobra.Command{
	Use:     "account",
	GroupID: GroupConfig,
	Short:   "Manage Claude Code accounts",
	RunE:    requireSubcommand,
	Long: `Manage multiple Claude Code accounts for Gas Town.

This enables switching between accounts (e.g., personal vs work) with
easy account selection per spawn or globally.

Commands:
  gt account list              List registered accounts
  gt account add <handle>      Add a new account
  gt account default <handle>  Set the default account
  gt account status            Show current account info`,
}

var accountListCmd = &cobra.Command{
	Use:   "list",
	Short: "List registered accounts",
	Long: `List all registered Claude Code accounts.

Shows account handles, emails, and which is the default.

Examples:
  gt account list           # Text output
  gt account list --json    # JSON output`,
	RunE: runAccountList,
}

var accountAddCmd = &cobra.Command{
	Use:   "add <handle>",
	Short: "Add a new account",
	Long: `Add a new Claude Code account.

Creates a config directory at ~/.claude-accounts/<handle> and registers
the account. You'll need to run 'claude' with CLAUDE_CONFIG_DIR set to
that directory to complete the login.

Examples:
  gt account add work
  gt account add work --email steve@company.com
  gt account add work --email steve@company.com --desc "Work account"`,
	Args: cobra.ExactArgs(1),
	RunE: runAccountAdd,
}

var accountDefaultCmd = &cobra.Command{
	Use:   "default <handle>",
	Short: "Set the default account",
	Long: `Set the default Claude Code account.

The default account is used when no --account flag or GT_ACCOUNT env var
is specified during spawn or attach.

Examples:
  gt account default work
  gt account default personal`,
	Args: cobra.ExactArgs(1),
	RunE: runAccountDefault,
}

// AccountListItem represents an account in list output.
type AccountListItem struct {
	Handle      string `json:"handle"`
	Email       string `json:"email"`
	Description string `json:"description,omitempty"`
	ConfigDir   string `json:"config_dir"`
	IsDefault   bool   `json:"is_default"`
}

func runAccountList(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("finding town root: %w", err)
	}

	accountsPath := constants.MayorAccountsPath(townRoot)
	cfg, err := config.LoadAccountsConfig(accountsPath)
	if err != nil {
		// If file doesn't exist, show empty message
		fmt.Println("No accounts configured.")
		fmt.Println("\nTo add an account:")
		fmt.Println("  gt account add <handle>")
		return nil
	}

	if len(cfg.Accounts) == 0 {
		fmt.Println("No accounts configured.")
		fmt.Println("\nTo add an account:")
		fmt.Println("  gt account add <handle>")
		return nil
	}

	// Build list items
	var items []AccountListItem
	for handle, acct := range cfg.Accounts {
		items = append(items, AccountListItem{
			Handle:      handle,
			Email:       acct.Email,
			Description: acct.Description,
			ConfigDir:   acct.ConfigDir,
			IsDefault:   handle == cfg.Default,
		})
	}

	// Sort by handle for consistent output
	sort.Slice(items, func(i, j int) bool {
		return items[i].Handle < items[j].Handle
	})

	if accountJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(items)
	}

	// Text output
	fmt.Printf("%s\n\n", style.Bold.Render("Claude Code Accounts"))
	for _, item := range items {
		marker := "  "
		if item.IsDefault {
			marker = "* "
		}

		fmt.Printf("%s%s", marker, style.Bold.Render(item.Handle))
		if item.Email != "" {
			fmt.Printf("  %s", item.Email)
		}
		if item.IsDefault {
			fmt.Printf("  %s", style.Dim.Render("(default)"))
		}
		fmt.Println()

		if item.Description != "" {
			fmt.Printf("    %s\n", style.Dim.Render(item.Description))
		}
	}

	return nil
}

func runAccountAdd(cmd *cobra.Command, args []string) error {
	handle := args[0]

	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("finding town root: %w", err)
	}

	accountsPath := constants.MayorAccountsPath(townRoot)

	// Load existing config or create new
	cfg, err := config.LoadAccountsConfig(accountsPath)
	if err != nil {
		cfg = config.NewAccountsConfig()
	}

	// Check if account already exists
	if _, exists := cfg.Accounts[handle]; exists {
		return fmt.Errorf("account '%s' already exists", handle)
	}

	// Build config directory path
	baseDir, err := config.DefaultAccountsConfigDir()
	if err != nil {
		return fmt.Errorf("determining accounts config directory: %w", err)
	}
	configDir := baseDir + "/" + handle

	// Create the config directory
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	// Symlink global commands (e.g., SuperClaude) so they're available
	// when CLAUDE_CONFIG_DIR points at this account directory.
	if err := ensureSharedCommandsSymlink(configDir); err != nil {
		style.PrintWarning("could not symlink global commands: %v", err)
	}
	if err := skills.ProvisionUserDir(configDir); err != nil {
		style.PrintWarning("could not provision mattpocock skills: %v", err)
	}

	// Add account
	cfg.Accounts[handle] = config.Account{
		Email:       accountEmail,
		Description: accountDescription,
		ConfigDir:   configDir,
	}

	// If this is the first account, make it default
	if cfg.Default == "" {
		cfg.Default = handle
	}

	// Save config
	if err := config.SaveAccountsConfig(accountsPath, cfg); err != nil {
		return fmt.Errorf("saving accounts config: %w", err)
	}

	fmt.Printf("Added account '%s'\n", handle)
	fmt.Printf("Config directory: %s\n", configDir)
	fmt.Println()
	fmt.Println("To complete login, run:")
	fmt.Printf("  CLAUDE_CONFIG_DIR=%s claude\n", configDir)
	fmt.Println("Then use /login to authenticate.")

	return nil
}

func runAccountDefault(cmd *cobra.Command, args []string) error {
	handle := args[0]

	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("finding town root: %w", err)
	}

	accountsPath := constants.MayorAccountsPath(townRoot)
	cfg, err := config.LoadAccountsConfig(accountsPath)
	if err != nil {
		return fmt.Errorf("loading accounts config: %w", err)
	}

	// Check if account exists
	if _, exists := cfg.Accounts[handle]; !exists {
		return fmt.Errorf("account '%s' not found", handle)
	}

	// Update default
	cfg.Default = handle

	// Save config
	if err := config.SaveAccountsConfig(accountsPath, cfg); err != nil {
		return fmt.Errorf("saving accounts config: %w", err)
	}

	fmt.Printf("Default account set to '%s'\n", handle)
	return nil
}

var accountStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show current account info",
	Long: `Show which Claude Code account would be used for new sessions.

Displays the currently resolved account based on:
1. GT_ACCOUNT environment variable (highest priority)
2. Default account from config

Examples:
  gt account status           # Show current account
  GT_ACCOUNT=work gt account status  # Show with env override`,
	RunE: runAccountStatus,
}

var accountSwitchCmd = &cobra.Command{
	Use:   "switch <handle>",
	Short: "Switch to a different account",
	Long: `Switch the active Claude Code account.

This command:
1. Backs up ~/.claude to the current account's config_dir (if needed)
2. Creates a symlink from ~/.claude to the target account's config_dir
3. Updates the default account in accounts.json

After switching, you must restart Claude Code for the change to take effect.

Examples:
  gt account switch work       # Switch to work account
  gt account switch personal   # Switch to personal account`,
	Args: cobra.ExactArgs(1),
	RunE: runAccountSwitch,
}

func runAccountStatus(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("finding town root: %w", err)
	}

	accountsPath := constants.MayorAccountsPath(townRoot)

	// Resolve account (empty flag since we want to show default resolution)
	configDir, handle, err := config.ResolveAccountConfigDir(accountsPath, "")
	if err != nil {
		return fmt.Errorf("resolving account: %w", err)
	}

	if handle == "" {
		fmt.Println("No account configured.")
		fmt.Println("\nTo add an account:")
		fmt.Println("  gt account add <handle>")
		return nil
	}

	// Check if GT_ACCOUNT is overriding
	envAccount := os.Getenv("GT_ACCOUNT")

	// Load config to get full account info
	cfg, err := config.LoadAccountsConfig(accountsPath)
	if err != nil {
		return fmt.Errorf("loading accounts config: %w", err)
	}

	acct := cfg.GetAccount(handle)
	if acct == nil {
		return fmt.Errorf("account '%s' not found", handle)
	}

	fmt.Printf("%s\n\n", style.Bold.Render("Current Account"))
	fmt.Printf("Handle:     %s\n", style.Bold.Render(handle))
	if acct.Email != "" {
		fmt.Printf("Email:      %s\n", acct.Email)
	}
	if acct.Description != "" {
		fmt.Printf("Description: %s\n", acct.Description)
	}
	fmt.Printf("Config Dir: %s\n", configDir)

	if envAccount != "" {
		fmt.Printf("\n%s\n", style.Dim.Render("(set via GT_ACCOUNT environment variable)"))
	} else if handle == cfg.Default {
		fmt.Printf("\n%s\n", style.Dim.Render("(default account)"))
	}

	return nil
}

func runAccountSwitch(cmd *cobra.Command, args []string) error {
	targetHandle := args[0]

	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("finding town root: %w", err)
	}

	accountsPath := constants.MayorAccountsPath(townRoot)
	cfg, err := config.LoadAccountsConfig(accountsPath)
	if err != nil {
		return fmt.Errorf("loading accounts config: %w", err)
	}

	// Check if target account exists
	targetAcct := cfg.GetAccount(targetHandle)
	if targetAcct == nil {
		// List available accounts
		var handles []string
		for h := range cfg.Accounts {
			handles = append(handles, h)
		}
		sort.Strings(handles)
		return fmt.Errorf("account '%s' not found. Available accounts: %v", targetHandle, handles)
	}

	// Get ~/.claude path
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("getting home directory: %w", err)
	}
	claudeDir := home + "/.claude"

	// Check current state of ~/.claude
	fileInfo, err := os.Lstat(claudeDir)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("checking ~/.claude: %w", err)
	}

	// Determine current account (if any) by checking symlink target
	var currentHandle string
	if err == nil && fileInfo.Mode()&os.ModeSymlink != 0 {
		// It's a symlink - find which account it points to
		linkTarget, err := os.Readlink(claudeDir)
		if err != nil {
			return fmt.Errorf("reading symlink: %w", err)
		}
		for h, acct := range cfg.Accounts {
			if acct.ConfigDir == linkTarget {
				currentHandle = h
				break
			}
		}
	}

	// Check if already on target account
	if currentHandle == targetHandle {
		fmt.Printf("Already on account '%s'\n", targetHandle)
		return nil
	}

	// Handle the case where ~/.claude is a real directory (not a symlink)
	if err == nil && fileInfo.Mode()&os.ModeSymlink == 0 && fileInfo.IsDir() {
		// It's a real directory - need to move it
		// Try to find which account it belongs to based on default
		if currentHandle == "" && cfg.Default != "" {
			currentHandle = cfg.Default
		}

		if currentHandle != "" {
			currentAcct := cfg.GetAccount(currentHandle)
			if currentAcct != nil {
				// Move ~/.claude to the current account's config_dir
				fmt.Printf("Moving ~/.claude to %s...\n", currentAcct.ConfigDir)

				// Remove the target config dir if it exists (it might be empty from account add)
				if _, err := os.Stat(currentAcct.ConfigDir); err == nil {
					if err := os.RemoveAll(currentAcct.ConfigDir); err != nil {
						return fmt.Errorf("removing existing config dir: %w", err)
					}
				}

				if err := os.Rename(claudeDir, currentAcct.ConfigDir); err != nil {
					return fmt.Errorf("moving ~/.claude to %s: %w", currentAcct.ConfigDir, err)
				}
			}
		} else {
			return fmt.Errorf("~/.claude is a directory but no default account is set. Please set a default account first with 'gt account default <handle>'")
		}
	} else if err == nil && fileInfo.Mode()&os.ModeSymlink != 0 {
		// It's a symlink - remove it so we can create a new one
		if err := os.Remove(claudeDir); err != nil {
			return fmt.Errorf("removing existing symlink: %w", err)
		}
	}
	// If ~/.claude doesn't exist, that's fine - we'll create the symlink

	// Create symlink to target account
	if err := os.Symlink(targetAcct.ConfigDir, claudeDir); err != nil {
		return fmt.Errorf("creating symlink to %s: %w", targetAcct.ConfigDir, err)
	}

	// Update default account
	cfg.Default = targetHandle
	if err := config.SaveAccountsConfig(accountsPath, cfg); err != nil {
		return fmt.Errorf("saving accounts config: %w", err)
	}

	fmt.Printf("Switched to account '%s'\n", targetHandle)
	fmt.Printf("~/.claude -> %s\n", targetAcct.ConfigDir)
	fmt.Println()
	fmt.Println(style.Warning.Render("⚠️  Restart Claude Code for the change to take effect"))

	return nil
}

// ensureSharedCommandsSymlink creates a symlink from configDir/commands to the
// global commands directory (~/.claude/commands) so that custom commands (e.g.,
// SuperClaude) are available regardless of which account is active via CLAUDE_CONFIG_DIR.
func ensureSharedCommandsSymlink(configDir string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	// Resolve the real path to the global commands directory.
	// This follows any symlinks (e.g., if ~/.claude is itself a symlink).
	globalCmds := filepath.Join(home, ".claude", "commands")
	realCmds, err := filepath.EvalSymlinks(globalCmds)
	if err != nil {
		// No global commands directory — nothing to share.
		return nil
	}

	acctCmds := filepath.Join(configDir, "commands")

	// If account already resolves to the same real directory, skip.
	if acctReal, err := filepath.EvalSymlinks(acctCmds); err == nil && acctReal == realCmds {
		return nil
	}

	// If something exists at acctCmds, handle it.
	if info, err := os.Lstat(acctCmds); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			// Stale symlink — remove and recreate.
			_ = os.Remove(acctCmds)
		} else {
			// Real directory — don't overwrite user's custom commands.
			return nil
		}
	}

	return os.Symlink(realCmds, acctCmds)
}

func init() {
	// Add flags
	accountListCmd.Flags().BoolVar(&accountJSON, "json", false, "Output as JSON")

	accountAddCmd.Flags().StringVar(&accountEmail, "email", "", "Account email address")
	accountAddCmd.Flags().StringVar(&accountDescription, "desc", "", "Account description")

	// Add subcommands
	accountCmd.AddCommand(accountListCmd)
	accountCmd.AddCommand(accountAddCmd)
	accountCmd.AddCommand(accountDefaultCmd)
	accountCmd.AddCommand(accountStatusCmd)
	accountCmd.AddCommand(accountSwitchCmd)

	rootCmd.AddCommand(accountCmd)
}
