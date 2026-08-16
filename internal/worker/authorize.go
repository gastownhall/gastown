package worker

import "strings"

// DecideAuthorize is fail-closed. Unknown state never allows. A deny
// always includes a reason. A missing town answer is a deny.
func DecideAuthorize(state State, req AuthorizeRequest) AuthorizeDecision {
	if !state.known() {
		return AuthorizeDecision{
			Allowed: false,
			Reason:  "unknown worker state: town will not allow a tool",
		}
	}
	if state == StateStopped || state == StateStopping {
		return AuthorizeDecision{
			Allowed: false,
			Reason:  "session is stopping: town will not allow a tool",
		}
	}
	command := commandFromInput(req.Input)
	if reason := dangerousReason(command); reason != "" {
		return AuthorizeDecision{Allowed: false, Reason: reason}
	}
	return AuthorizeDecision{Allowed: true}
}

func commandFromInput(input map[string]any) string {
	if input == nil {
		return ""
	}
	if v, ok := input["command"].(string); ok {
		return v
	}
	return ""
}

func dangerousReason(command string) string {
	if command == "" {
		return ""
	}
	lower := strings.ToLower(command)
	if reason := matchSudo(lower); reason != "" {
		return reason
	}
	if reason := matchForcePush(lower); reason != "" {
		return reason
	}
	if strings.Contains(lower, "git") && strings.Contains(lower, "reset") && strings.Contains(lower, "--hard") {
		return "hard reset discards uncommitted changes"
	}
	if strings.Contains(lower, "git") && strings.Contains(lower, "clean") && strings.Contains(lower, "-f") {
		return "git clean -f deletes untracked files"
	}
	return ""
}

func matchSudo(command string) string {
	for _, f := range strings.Fields(command) {
		if f == "sudo" {
			return "agents must never use sudo"
		}
	}
	return ""
}

func matchForcePush(command string) string {
	if !strings.Contains(command, "git") || !strings.Contains(command, "push") {
		return ""
	}
	fields := strings.Fields(command)
	hasPush := false
	for i, f := range fields {
		if f == "push" && i > 0 && fields[i-1] == "git" {
			hasPush = true
			continue
		}
		if !hasPush {
			continue
		}
		if f == "--force-with-lease" || f == "--force-if-includes" {
			continue
		}
		if f == "--force" || f == "-f" {
			return "force push rewrites remote history"
		}
	}
	return ""
}
