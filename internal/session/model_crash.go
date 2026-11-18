package session

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// ModelCrashFatalSignature is the exact fatal line captured from an OpenCode
// session after the local model process crashed. Keep this intentionally
// narrow: connection, transport, and generic model errors belong to the
// infrastructure watchdog or ordinary agent error handling.
const ModelCrashFatalSignature = "The model has crashed without additional information. (Exit code: null)"

var (
	ansiEscapePattern          = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07]*(?:\x07|\x1b\\))`)
	openCodeModeModelPattern   = regexp.MustCompile(`^(?:[[:^alnum:]]+\s*)?(?:Build|Plan)\s*·\s*.+\s+\(local(?:,\s*[^)]+)?\)(?:\s+.+)?$`)
	openCodeLocalSourcePattern = regexp.MustCompile(`^[[:alnum:]][[:alnum:] ._+/-]*\s+\(local(?:,\s*[^)]+)?\)$`)
	openCodeWorkspacePattern   = regexp.MustCompile(`^(?:~[/\\]|/|[[:alpha:]]:[/\\])`)
	ModelCrashFatalFingerprint = fingerprintModelCrash(ModelCrashFatalSignature)
)

// DetectModelCrash reports the stable fingerprint of the one captured fatal
// signature. ANSI escapes and surrounding UI decoration are allowed, but the
// semantic message itself is not broadened.
func DetectModelCrash(output string) (string, bool) {
	clean := ansiEscapePattern.ReplaceAllString(output, "")
	index := strings.LastIndex(clean, ModelCrashFatalSignature)
	if index < 0 {
		return "", false
	}
	// A fatal line retained in scrollback must not override newer successful
	// model output. Ignore only the captured OpenCode footer chrome; any other
	// meaningful line is newer progress.
	suffix := clean[index+len(ModelCrashFatalSignature):]
	for _, line := range strings.Split(suffix, "\n") {
		if !isOpenCodeModelCrashChrome(line) {
			return "", false
		}
	}
	return ModelCrashFatalFingerprint, true
}

func isOpenCodeModelCrashChrome(line string) bool {
	trimmed := strings.TrimSpace(strings.Trim(line, "│┃┆┊┌┐└┘├┤┬┴┼─━╭╮╰╯╹▀▄▁▂▃▅▆▇█"))
	if trimmed == "" {
		return true
	}
	// OpenCode may render the fatal message as a quoted block. After slicing
	// at the signature the closing quote remains on its own decorated line.
	if trimmed == `"` || trimmed == `'` {
		return true
	}
	// OpenCode renders the active mode/model and local provider as structured
	// footer rows. Match that structure rather than any particular model or
	// provider name.
	if openCodeModeModelPattern.MatchString(trimmed) ||
		openCodeLocalSourcePattern.MatchString(trimmed) {
		return true
	}
	// The final row contains an absolute/home-relative workspace path and may
	// include context usage. This covers macOS, Linux, containers, and Windows
	// without treating ordinary assistant/tool prose as footer chrome.
	if openCodeWorkspacePattern.MatchString(trimmed) {
		parts := strings.SplitN(trimmed, " · ", 2)
		if strings.TrimSpace(parts[0]) == "" {
			return false
		}
		return len(parts) == 1 || strings.Contains(strings.ToLower(parts[1]), "context")
	}
	return false
}

func fingerprintModelCrash(signature string) string {
	sum := sha256.Sum256([]byte(signature))
	return "sha256:" + hex.EncodeToString(sum[:])
}
