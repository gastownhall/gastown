package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var tapGuardBroadSearchCmd = &cobra.Command{
	Use:   "broad-search",
	Short: "Block a repo-root recursive search that would read a denied .env",
	Long: `Block unbounded repo-root searches that walk into a denied .env file.

WHY THIS EXISTS. A polecat doing a "find all consumers" sweep writes a
recursive grep rooted at the repo root. The sweep reaches .env, the deny rule
in the repo's .claude/settings.json correctly stops it, and Claude Code raises
an owner-only permission prompt. The agent then blocks INDEFINITELY on a
question only the human can answer, holding a capacity slot, while every
status surface still reports it as working.

Measured on liveop, 2026-09-02/03: nuka held 7 hours, brahmin held 2.5 hours
with 8 files of uncommitted work, on the identical prompt. Two of the last
three polecats to attempt a repo-wide search hit the same wall.

The deny rule is RIGHT and this guard does not weaken it. The search is too
broad. Failing closed here converts an indefinite silent block into a clean
refusal the agent can read and adapt to, which is the whole difference between
a lost afternoon and a retried command.

WHEN IT BLOCKS. All of these must hold, so ordinary searches are unaffected:
  1. the command is a recursive grep/find-exec-grep, or an rg that has been
     told to ignore .gitignore or to include hidden files
  2. its search root is the repository root ("." or omitted), not a subtree
  3. a .env file actually exists at that root -- no .env, no block
  4. the command does not already exclude .env

Exit codes:
  0 - Search allowed
  2 - Search BLOCKED (with a scoped rewrite suggested on stderr)`,
	// The refusal text IS the product here -- it is the only thing the blocked
	// agent reads before deciding what to do next. Cobra's usage dump and its
	// "Error: exit 2" line would bury it, so silence both. The other guards
	// print them; matching that is not worth a worse message.
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runTapGuardBroadSearch,
}

func init() {
	tapGuardCmd.AddCommand(tapGuardBroadSearchCmd)
}

// hookInputCwd extracts the working directory Claude Code reports for the
// tool call, falling back to the guard's own cwd. The .env existence check
// has to run against the AGENT's directory, not the guard process's, or the
// guard answers a question about the wrong repository.
func hookInputCwd(input []byte) string {
	var hookInput struct {
		Cwd string `json:"cwd"`
	}
	if err := json.Unmarshal(input, &hookInput); err == nil && hookInput.Cwd != "" {
		return hookInput.Cwd
	}
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

// rootHasEnvFile reports whether a dotenv file the deny rules cover exists at
// dir. The guard blocks ONLY when the prompt it prevents could actually fire,
// so a repository with no .env is never slowed down by this rule.
func rootHasEnvFile(dir string) bool {
	if dir == "" {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if e.Name() == ".env" || strings.HasPrefix(e.Name(), ".env.") {
			return true
		}
	}
	return false
}

// splitCommand tokenises a shell command for the matchers below. Word
// splitting is enough here: every predicate either looks for a whole token or
// trims the quotes itself, and a real shell parser would buy nothing a guard
// that fails open can use.
func splitCommand(command string) []string {
	return strings.Fields(command)
}

// excludesEnv reports whether the command already keeps the sweep away from
// .env, by any of the spellings the tools accept.
func excludesEnv(command string) bool {
	markers := []string{
		"--exclude=.env",
		"--exclude='.env",
		"--exclude=\".env",
		"--exclude-dir=.env",
		"!.env",
		"--exclude=*.env",
	}
	for _, m := range markers {
		if strings.Contains(command, m) {
			return true
		}
	}
	return false
}

// searchRootIsRepoRoot reports whether the recursive search starts at the
// current directory rather than a subtree. A sweep of "src/" cannot reach a
// root .env, so it is none of this guard's business.
//
// A bare "." must appear, and no other argument may name a directory that
// actually exists under dir. Testing for existence rather than for a "/" is
// what keeps `grep -rn 'foo/bar' .` blocked: the pattern looks like a path
// but is not one, and treating it as a scope would let the exact command
// this guard exists to stop through unexamined.
func searchRootIsRepoRoot(fields []string, dir string) bool {
	sawDotRoot := false
	for i, f := range fields {
		if i == 0 || strings.HasPrefix(f, "-") {
			continue
		}
		f = strings.Trim(f, "\"'")
		if f == "." || f == "./" {
			sawDotRoot = true
			continue
		}
		if dir == "" {
			continue
		}
		candidate := f
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(dir, f)
		}
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return false
		}
	}
	return sawDotRoot
}

// isRecursiveGrep reports whether the command is a grep walking a tree.
func isRecursiveGrep(fields []string) bool {
	sawGrep := false
	recursive := false
	for _, f := range fields {
		base := filepath.Base(f)
		if base == "grep" || base == "egrep" || base == "fgrep" {
			sawGrep = true
			continue
		}
		if strings.HasPrefix(f, "-") && !strings.HasPrefix(f, "--") &&
			(strings.Contains(f, "r") || strings.Contains(f, "R")) {
			recursive = true
		}
		if f == "--recursive" || f == "--dereference-recursive" {
			recursive = true
		}
	}
	return sawGrep && recursive
}

// isUnrestrictedRipgrep reports whether an rg invocation has been told to
// look at files it would otherwise skip. Plain rg honours .gitignore and
// skips hidden files, so it never reaches .env on its own -- only the
// overrides do.
func isUnrestrictedRipgrep(fields []string) bool {
	sawRg := false
	unrestricted := false
	for _, f := range fields {
		if filepath.Base(f) == "rg" {
			sawRg = true
			continue
		}
		switch f {
		case "--no-ignore", "--no-ignore-vcs", "--hidden", "-.", "--unrestricted":
			unrestricted = true
		}
		// -u, -uu, -uuu and combined short flags such as -ul are the short
		// spellings of --unrestricted.
		if strings.HasPrefix(f, "-") && !strings.HasPrefix(f, "--") && strings.Contains(f, "u") {
			unrestricted = true
		}
	}
	return sawRg && unrestricted
}

// isFindExecGrep reports whether the command is the find-pipes-into-grep
// spelling of the same sweep.
func isFindExecGrep(fields []string) bool {
	sawFind := false
	sawGrep := false
	for _, f := range fields {
		switch filepath.Base(f) {
		case "find":
			sawFind = true
		case "grep", "egrep", "fgrep", "xargs":
			sawGrep = true
		}
	}
	return sawFind && sawGrep
}

func runTapGuardBroadSearch(cmd *cobra.Command, args []string) error {
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return nil // fail open
	}

	command := extractCommand(input)
	if command == "" {
		return nil
	}

	if excludesEnv(command) {
		return nil
	}

	fields := splitCommand(command)
	if !isRecursiveGrep(fields) && !isUnrestrictedRipgrep(fields) && !isFindExecGrep(fields) {
		return nil
	}
	cwd := hookInputCwd(input)
	if !searchRootIsRepoRoot(fields, cwd) {
		return nil
	}
	if !rootHasEnvFile(cwd) {
		return nil
	}

	printBroadSearchBlock(command)
	return NewSilentExit(2)
}

func printBroadSearchBlock(originalCommand string) {
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "╔══════════════════════════════════════════════════════════════════╗")
	fmt.Fprintln(os.Stderr, "║  ❌ REPO-ROOT SEARCH BLOCKED — it would stall on a .env prompt    ║")
	fmt.Fprintln(os.Stderr, "╠══════════════════════════════════════════════════════════════════╣")
	fmt.Fprintf(os.Stderr, "║  Command: %-53s ║\n", truncateStr(originalCommand, 53))
	fmt.Fprintln(os.Stderr, "╚══════════════════════════════════════════════════════════════════╝")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "This search walks the repository root, so it reaches .env, which the")
	fmt.Fprintln(os.Stderr, "deny rule in .claude/settings.json covers. That raises a permission")
	fmt.Fprintln(os.Stderr, "prompt only the human owner can answer, and you would block on it")
	fmt.Fprintln(os.Stderr, "indefinitely — two polecats lost 9.5 hours between them this way.")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Do not ask for the search to be approved. Narrow it instead:")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  name the subtrees you actually mean")
	fmt.Fprintln(os.Stderr, "    grep -rn 'pattern' src/ components/ services/")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  or exclude the files you never wanted")
	fmt.Fprintln(os.Stderr, "    grep -rn 'pattern' . --exclude='.env*' --exclude-dir=node_modules")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  or use the Grep tool, which honours the deny rules without prompting")
	fmt.Fprintln(os.Stderr, "")
}
