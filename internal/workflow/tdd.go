package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Runner executes argv in dir and reports its exit code and a short tail of
// its combined output. A non-zero exit is reported through exit with a nil
// err; err is reserved for failures to start the command or I/O errors.
// Implementations must exec argv directly, never through a shell.
type Runner interface {
	Run(ctx context.Context, dir string, argv []string) (exit int, tail string, err error)
}

// Evidence provenance values and TDD phases recorded by gtb.
const (
	sourceGTB          = "gtb"
	provenanceVerified = "verified"
	phaseRed           = "red"
	phaseGreen         = "green"
	phaseRefused       = "refused"

	tailPassedBeforeImpl = "test passed before implementation"
	tailTestFilesChanged = "test files changed since red"

	// exitNotRun is recorded when gtb refuses without running the command.
	exitNotRun = -1
	tailBytes  = TailBytes
)

// TailBytes caps every stored output tail (TDD and check runs). A tail of
// (nearly) this length was cut at its start.
const TailBytes = 1000

// ErrRefused is returned (wrapped) when Red or Green records a refused run.
var ErrRefused = errors.New("workflow: run refused")

// ErrInvalidItemID is returned (wrapped) when an item ID or plan step is not
// a safe identifier.
var ErrInvalidItemID = errors.New("workflow: invalid item id")

var idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// validateID rejects identifiers that could escape a path or confuse a
// lookup: id must match ^[A-Za-z0-9][A-Za-z0-9._-]*$ and contain no "..".
// Callers validate before any file or exec access.
func validateID(id string) error {
	if !idRe.MatchString(id) || strings.Contains(id, "..") {
		return fmt.Errorf("%w: %q", ErrInvalidItemID, id)
	}
	return nil
}

// ValidateID reports whether id is a safe item identifier (see validateID).
// It returns an error wrapping ErrInvalidItemID otherwise.
func ValidateID(id string) error { return validateID(id) }

// validateIDs validates each id with validateID.
func validateIDs(ids ...string) error {
	for _, id := range ids {
		if err := validateID(id); err != nil {
			return err
		}
	}
	return nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// cleanTestFiles validates testFiles as local paths relative to the run
// directory and returns them sorted and de-duplicated.
func cleanTestFiles(testFiles []string) ([]string, error) {
	if len(testFiles) == 0 {
		return nil, errors.New("workflow: at least one test file is required")
	}
	seen := make(map[string]bool, len(testFiles))
	var out []string
	for _, f := range testFiles {
		c := filepath.ToSlash(filepath.Clean(f))
		if f == "" || !filepath.IsLocal(c) {
			return nil, fmt.Errorf("workflow: test file %q must be a path inside the run directory", f)
		}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out, nil
}

// testFilesSHA returns the sha256 over the sorted "path\x00sha256(file)\n"
// entries of testFiles (relative to dir). A missing file is an error.
func testFilesSHA(dir string, testFiles []string) (string, error) {
	files, err := cleanTestFiles(testFiles)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, f := range files {
		content, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(f)))
		if err != nil {
			return "", fmt.Errorf("workflow: reading test file: %w", err)
		}
		b.WriteString(f)
		b.WriteByte(0)
		b.WriteString(sha256Hex(content))
		b.WriteByte('\n')
	}
	return sha256Hex([]byte(b.String())), nil
}

// Red runs argv (no shell) in dir for planStep and records a verified red
// when it fails. A passing command is recorded as refused with the tail
// "test passed before implementation" and ErrRefused is returned. Missing
// test files, an empty argv or a runner error record nothing.
func Red(env GateEnv, r Runner, itemID, planStep, dir string, argv []string, testFiles []string) (TDDRun, error) {
	if err := validateIDs(itemID, planStep); err != nil {
		return TDDRun{}, err
	}
	if len(argv) == 0 || argv[0] == "" {
		return TDDRun{}, errors.New("workflow: red needs a command")
	}
	files, err := cleanTestFiles(testFiles)
	if err != nil {
		return TDDRun{}, err
	}
	sha, err := testFilesSHA(dir, files)
	if err != nil {
		return TDDRun{}, err
	}
	cmd, err := json.Marshal(argv)
	if err != nil {
		return TDDRun{}, fmt.Errorf("workflow: encoding argv: %w", err)
	}
	exit, tail, err := r.Run(context.Background(), dir, argv)
	if err != nil {
		return TDDRun{}, fmt.Errorf("workflow: running red: %w", err)
	}
	run := TDDRun{
		ItemID: itemID, PlanStep: planStep, Phase: phaseRed, Cmd: string(cmd), Exit: exit,
		TestFilesSHA: sha, TestFiles: files, Tail: tail,
		Source: sourceGTB, Provenance: provenanceVerified, At: env.now(),
	}
	var refused error
	if exit == 0 {
		run.Phase, run.Tail = phaseRefused, tailPassedBeforeImpl
		refused = fmt.Errorf("%w: %s", ErrRefused, tailPassedBeforeImpl)
	}
	if err := env.Store.AddTDD(run); err != nil {
		return TDDRun{}, err
	}
	env.emit(EventEvidence, itemID, run)
	return run, refused
}

// Green re-runs the argv of the latest (last appended) gtb-verified red for
// planStep in dir. It
// records refused (and returns ErrRefused) without running the command when
// the red's test files changed or disappeared, and refused when the command
// still fails; otherwise it records a verified green.
func Green(env GateEnv, r Runner, itemID, planStep, dir string) (TDDRun, error) {
	if err := validateIDs(itemID, planStep); err != nil {
		return TDDRun{}, err
	}
	runs, err := env.Store.TDD(itemID)
	if err != nil {
		return TDDRun{}, err
	}
	// The latest red is the last matching row in store (append) order, not
	// the greatest At.
	var red *TDDRun
	for i := len(runs) - 1; i >= 0; i-- {
		if x := runs[i]; x.PlanStep == planStep && x.Phase == phaseRed && isVerifiedTDD(x) {
			red = &runs[i]
			break
		}
	}
	if red == nil {
		return TDDRun{}, fmt.Errorf("workflow: no verified red for plan step %s", planStep)
	}
	var argv []string
	if err := json.Unmarshal([]byte(red.Cmd), &argv); err != nil || len(argv) == 0 {
		return TDDRun{}, fmt.Errorf("workflow: red for plan step %s has no usable command", planStep)
	}
	run := TDDRun{
		ItemID: itemID, PlanStep: planStep, Phase: phaseGreen, Cmd: red.Cmd,
		TestFilesSHA: red.TestFilesSHA, TestFiles: red.TestFiles,
		Source: sourceGTB, Provenance: provenanceVerified,
	}
	var refused error
	if sha, err := testFilesSHA(dir, red.TestFiles); err != nil || sha != red.TestFilesSHA {
		run.Phase, run.Exit, run.Tail = phaseRefused, exitNotRun, tailTestFilesChanged
		refused = fmt.Errorf("%w: %s", ErrRefused, tailTestFilesChanged)
	} else {
		exit, tail, err := r.Run(context.Background(), dir, argv)
		if err != nil {
			return TDDRun{}, fmt.Errorf("workflow: running green: %w", err)
		}
		run.Exit, run.Tail = exit, tail
		if exit != 0 {
			run.Phase = phaseRefused
			refused = fmt.Errorf("%w: test still fails (exit %d)", ErrRefused, exit)
		}
	}
	run.At = env.now()
	if err := env.Store.AddTDD(run); err != nil {
		return TDDRun{}, err
	}
	env.emit(EventEvidence, itemID, run)
	return run, refused
}

// ExecRunner is the production Runner. It execs argv directly (no shell),
// writes the full combined output to <OutputDir>/<UTC timestamp>.log and
// returns the last 1000 bytes as the tail. Use OutputDir(townRoot, rig, id).
type ExecRunner struct{ OutputDir string }

// Run implements Runner.
func (e ExecRunner) Run(ctx context.Context, dir string, argv []string) (int, string, error) {
	if len(argv) == 0 {
		return 0, "", errors.New("workflow: empty argv")
	}
	if err := os.MkdirAll(e.OutputDir, 0o700); err != nil {
		return 0, "", fmt.Errorf("workflow: creating output dir: %w", err)
	}
	log, err := createLog(e.OutputDir)
	if err != nil {
		return 0, "", err
	}
	defer log.Close()
	tail := &tailWriter{max: 4 * tailBytes}
	out := io.MultiWriter(log, tail)

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = out, out
	runErr := cmd.Run()
	if err := log.Close(); err != nil {
		return 0, "", fmt.Errorf("workflow: writing output log: %w", err)
	}
	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
		return 0, tailOf(tail.buf), nil
	case errors.As(runErr, &exitErr):
		return exitErr.ExitCode(), tailOf(tail.buf), nil
	default:
		return 0, "", fmt.Errorf("workflow: running %s: %w", argv[0], runErr)
	}
}

// createLog creates a new log file named by the current UTC time in RFC 3339
// basic format (no colons), never overwriting an existing log.
func createLog(dir string) (*os.File, error) {
	base := time.Now().UTC().Format("20060102T150405.000000000Z")
	for i := 0; i < 100; i++ {
		name := base
		if i > 0 {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		f, err := os.OpenFile(filepath.Join(dir, name+".log"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("workflow: creating output log: %w", err)
		}
		return f, nil
	}
	return nil, fmt.Errorf("workflow: creating output log in %s: too many logs at %s", dir, base)
}

// tailWriter keeps the last max bytes written to it.
type tailWriter struct {
	buf []byte
	max int
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return len(p), nil
}

// tailOf returns the last tailBytes bytes of b, advanced to the next rune
// start so a multi-byte UTF-8 rune is never split.
func tailOf(b []byte) string { return lastBytes(string(b), tailBytes) }

// lastBytes returns the longest suffix of s of at most n bytes that starts
// on a rune boundary.
func lastBytes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	start := len(s) - n
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

// FirstBytes returns the longest prefix of s of at most n bytes that ends
// on a rune boundary.
func FirstBytes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	end := n
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}
