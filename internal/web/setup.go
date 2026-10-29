package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// SetupHandler handles the setup flow when no workspace exists.
type SetupHandler struct {
	csrfToken string
}

// NewSetupHandler creates a new setup handler with the given CSRF token.
func NewSetupHandler(csrfToken string) *SetupHandler {
	return &SetupHandler{csrfToken: csrfToken}
}

// ServeHTTP renders the setup page with the CSRF token injected.
func (h *SetupHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	html := strings.Replace(setupHTML, "<!--CSRF_TOKEN-->", h.csrfToken, 1)
	_, _ = w.Write([]byte(html))
}

// SetupAPIHandler handles API requests for setup operations.
type SetupAPIHandler struct {
	csrfToken string
}

// NewSetupAPIHandler creates a new setup API handler with the given CSRF token.
func NewSetupAPIHandler(csrfToken string) *SetupAPIHandler {
	if csrfToken == "" {
		log.Printf("WARNING: SetupAPIHandler created with empty CSRF token — POST requests will not be protected")
	}
	return &SetupAPIHandler{csrfToken: csrfToken}
}

// ServeHTTP routes setup API requests.
func (h *SetupAPIHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// No CORS headers — the setup page is served from the same origin.

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Validate CSRF token on all POST requests.
	if r.Method == http.MethodPost && h.csrfToken != "" {
		if r.Header.Get("X-Dashboard-Token") != h.csrfToken {
			h.sendError(w, "Invalid or missing dashboard token", http.StatusForbidden)
			return
		}
	}

	path := strings.TrimPrefix(r.URL.Path, "/api")
	switch {
	case path == "/install" && r.Method == http.MethodPost:
		h.handleInstall(w, r)
	case path == "/rig/add" && r.Method == http.MethodPost:
		h.handleRigAdd(w, r)
	case path == "/check-workspace" && r.Method == http.MethodPost:
		h.handleCheckWorkspace(w, r)
	case path == "/launch" && r.Method == http.MethodPost:
		h.handleLaunch(w, r)
	case path == "/status" && r.Method == http.MethodGet:
		h.handleStatus(w, r)
	default:
		http.Error(w, "Not found", http.StatusNotFound)
	}
}

// InstallRequest is the request body for installing a new workspace.
type InstallRequest struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Git  bool   `json:"git"`
}

// CheckWorkspaceRequest is the request body for checking a workspace path.
type CheckWorkspaceRequest struct {
	Path string `json:"path"`
}

// LaunchRequest is the request body for launching dashboard from a workspace.
type LaunchRequest struct {
	Path string `json:"path"`
	Port int    `json:"port"`
	Bind string `json:"bind"`
}

// CheckWorkspaceResponse is the response for workspace checks.
type CheckWorkspaceResponse struct {
	Valid   bool     `json:"valid"`
	Path    string   `json:"path"`
	Name    string   `json:"name,omitempty"`
	Rigs    []string `json:"rigs,omitempty"`
	Message string   `json:"message,omitempty"`
}

// RigAddRequest is the request body for adding a rig.
type RigAddRequest struct {
	Name   string `json:"name"`
	GitURL string `json:"gitUrl"`
}

// SetupResponse is the response for setup operations.
type SetupResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
	Output  string `json:"output,omitempty"`
}

func (h *SetupAPIHandler) handleInstall(w http.ResponseWriter, r *http.Request) {
	var req InstallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.sendError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Path == "" {
		h.sendError(w, "Path is required", http.StatusBadRequest)
		return
	}

	// Expand ~ to home directory (with path cleaning to prevent traversal).
	// Absolute paths (e.g., /opt/workspace) are intentionally allowed —
	// this is a localhost-only dashboard and users may install workspaces anywhere.
	expanded, err := expandHomePath(req.Path)
	if err != nil {
		log.Printf("handleInstall: expandHomePath(%q) failed: %v", req.Path, err)
		h.sendError(w, "Invalid path", http.StatusBadRequest)
		return
	}
	req.Path = expanded

	// Build gt install command. Flags go first, then -- to end flag parsing,
	// then the positional path (prevents paths like "--help" being parsed as flags).
	args := []string{"install"}
	if req.Name != "" {
		if !isValidID(req.Name) {
			h.sendError(w, "Invalid workspace name format", http.StatusBadRequest)
			return
		}
		args = append(args, "--name", req.Name)
	}
	if req.Git {
		args = append(args, "--git")
	}
	args = append(args, "--", req.Path)

	output, err := h.runGtCommand(r.Context(), 60*time.Second, args)
	if err != nil {
		h.sendJSON(w, SetupResponse{
			Success: false,
			Error:   err.Error(),
			Output:  output,
		})
		return
	}

	h.sendJSON(w, SetupResponse{
		Success: true,
		Message: fmt.Sprintf("Workspace created at %s", req.Path),
		Output:  output,
	})
}

func (h *SetupAPIHandler) handleRigAdd(w http.ResponseWriter, r *http.Request) {
	var req RigAddRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.sendError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Name == "" || req.GitURL == "" {
		h.sendError(w, "Name and gitUrl are required", http.StatusBadRequest)
		return
	}
	if !isValidRigName(req.Name) {
		h.sendError(w, "Invalid rig name format (alphanumeric and underscores only, no hyphens or dots)", http.StatusBadRequest)
		return
	}
	if !isValidGitURL(req.GitURL) {
		h.sendError(w, "Git URL must be https://, http://, ssh://, git://, or git@host:path format", http.StatusBadRequest)
		return
	}

	// Flags before --, positional args after (consistent with handleInstall/handleIssueCreate).
	args := []string{"rig", "add", "--", req.Name, req.GitURL}

	output, err := h.runGtCommand(r.Context(), 120*time.Second, args)
	if err != nil {
		h.sendJSON(w, SetupResponse{
			Success: false,
			Error:   err.Error(),
			Output:  output,
		})
		return
	}

	h.sendJSON(w, SetupResponse{
		Success: true,
		Message: fmt.Sprintf("Rig '%s' added", req.Name),
		Output:  output,
	})
}

func (h *SetupAPIHandler) handleLaunch(w http.ResponseWriter, r *http.Request) {
	var req LaunchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.sendError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Path == "" {
		h.sendError(w, "Path is required", http.StatusBadRequest)
		return
	}

	// Expand ~ to home directory (with path cleaning to prevent traversal)
	path, err := expandHomePath(req.Path)
	if err != nil {
		log.Printf("handleLaunch: expandHomePath(%q) failed: %v", req.Path, err)
		h.sendError(w, "Invalid path", http.StatusBadRequest)
		return
	}

	port := req.Port
	if port == 0 {
		port = 8080
	}
	// Upper bound is 65534 (not 65535) to reserve room for newPort = port + 1
	if port < 1 || port > 65534 {
		h.sendError(w, "Port must be between 1 and 65534", http.StatusBadRequest)
		return
	}

	// Use PATH lookup for gt binary. Do NOT use os.Executable() here - during
	// tests it returns the test binary, causing fork bombs when executed.

	// Start new dashboard on a DIFFERENT port first, then we'll tell the browser to go there
	newPort := port + 1

	// Propagate bind address so the child dashboard listens on the same interface.
	bind := req.Bind
	if bind == "" {
		bind = "127.0.0.1"
	}

	// Start new dashboard process from the workspace directory
	cmd := exec.Command("gt", "dashboard", "--port", fmt.Sprintf("%d", newPort), "--bind", bind)
	cmd.Dir = path
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		h.sendError(w, "Failed to start dashboard: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Wait for the new server to be ready.
	// Use the bind address for the health check; if binding to all interfaces,
	// probe via localhost since 0.0.0.0 is not directly connectable.
	probeHost := bind
	if probeHost == "0.0.0.0" {
		probeHost = "127.0.0.1"
	}
	ready := false
	for i := 0; i < 30; i++ { // Try for 3 seconds
		resp, err := http.Get(fmt.Sprintf("http://%s:%d/api/commands", probeHost, newPort))
		if err == nil {
			_ = resp.Body.Close()
			ready = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !ready {
		h.sendError(w, "New dashboard failed to start", http.StatusInternalServerError)
		return
	}

	// Build redirect URL using the Host header from the incoming request so the
	// browser reaches the child dashboard via the same hostname it used for the
	// parent (works for both localhost and remote/IP access).
	redirectHost := r.Host
	if host, _, err := net.SplitHostPort(redirectHost); err == nil {
		redirectHost = host
	}
	// Send success response with the new port to redirect to
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success":  true,
		"message":  fmt.Sprintf("Dashboard launching from %s", path),
		"redirect": fmt.Sprintf("http://%s:%d", redirectHost, newPort),
	})
}

func (h *SetupAPIHandler) handleCheckWorkspace(w http.ResponseWriter, r *http.Request) {
	var req CheckWorkspaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.sendError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Path == "" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(CheckWorkspaceResponse{Valid: false, Message: "Path is required"})
		return
	}

	// Expand ~ to home directory (with path cleaning to prevent traversal)
	path, err := expandHomePath(req.Path)
	if err != nil {
		// Return 200 with Valid:false (not 400) because this is a "check" endpoint
		// that reports validity status, unlike mutating endpoints that return 400 on bad input.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(CheckWorkspaceResponse{Valid: false, Message: "Invalid path format"})
		return
	}

	// Check if mayor/ directory exists (indicates a Gas Town HQ)
	mayorDir := filepath.Join(path, "mayor")
	if _, err := os.Stat(mayorDir); os.IsNotExist(err) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(CheckWorkspaceResponse{
			Valid:   false,
			Path:    path,
			Message: "Not a Gas Town workspace (no mayor/ directory)",
		})
		return
	}

	// Try to get rig list from this workspace
	var rigs []string
	cmd := exec.CommandContext(r.Context(), "gt", "rig", "list", "--json")
	configureWebCommand(cmd)
	cmd.Dir = path
	if output, err := cmd.Output(); err == nil {
		// Parse JSON output for rig names
		var rigList []struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(output, &rigList) == nil {
			for _, rig := range rigList {
				rigs = append(rigs, rig.Name)
			}
		}
	}

	// Get workspace name from directory
	name := filepath.Base(path)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(CheckWorkspaceResponse{
		Valid:   true,
		Path:    path,
		Name:    name,
		Rigs:    rigs,
		Message: fmt.Sprintf("Valid workspace with %d rigs", len(rigs)),
	})
}

func (h *SetupAPIHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	// Check if we can find a workspace now
	output, err := h.runGtCommand(r.Context(), 5*time.Second, []string{"status"})
	if err != nil {
		h.sendJSON(w, SetupResponse{
			Success: false,
			Error:   "No workspace configured",
		})
		return
	}

	h.sendJSON(w, SetupResponse{
		Success: true,
		Message: "Workspace found",
		Output:  output,
	})
}

func (h *SetupAPIHandler) runGtCommand(ctx context.Context, timeout time.Duration, args []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "gt", args...)
	configureWebCommand(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	output := stdout.String()
	if stderr.Len() > 0 {
		if output != "" {
			output += "\n"
		}
		output += stderr.String()
	}

	return output, err
}

func (h *SetupAPIHandler) sendError(w http.ResponseWriter, msg string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(SetupResponse{Success: false, Error: msg})
}

func (h *SetupAPIHandler) sendJSON(w http.ResponseWriter, resp SetupResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// NewSetupMux creates the HTTP handler for setup mode.
func NewSetupMux() (http.Handler, error) {
	csrfToken := generateCSRFToken()
	setupHandler := NewSetupHandler(csrfToken)
	apiHandler := NewSetupAPIHandler(csrfToken)

	mux := http.NewServeMux()
	mux.Handle("/api/", apiHandler)
	mux.Handle("/", setupHandler)

	return mux, nil
}

const setupHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <meta name="dashboard-token" content="<!--CSRF_TOKEN-->">
    <title>Gas Town Setup</title>
    <style>
        :root {
            --bg-dark: #0d1117;
            --bg-card: #161b22;
            --bg-card-hover: #1c2128;
            --border: #30363d;
            --text-primary: #e6edf3;
            --text-secondary: #8b949e;
            --text-muted: #6e7681;
            --green: #3fb950;
            --blue: #58a6ff;
            --yellow: #d29922;
            --red: #f85149;
            --orange: #db6d28;
            --font-mono: 'SF Mono', 'Monaco', 'Inconsolata', 'Roboto Mono', monospace;
        }

        * { box-sizing: border-box; margin: 0; padding: 0; }

        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Helvetica, Arial, sans-serif;
            background: var(--bg-dark);
            color: var(--text-primary);
            min-height: 100vh;
            display: flex;
            flex-direction: column;
            align-items: center;
            justify-content: center;
            padding: 20px;
        }

        .setup-container {
            max-width: 600px;
            width: 100%;
        }

        .setup-header {
            text-align: center;
            margin-bottom: 40px;
        }

        .setup-header h1 {
            font-size: 2.5rem;
            margin-bottom: 8px;
        }

        .setup-header p {
            color: var(--text-secondary);
            font-size: 1.1rem;
        }

        .setup-card {
            background: var(--bg-card);
            border: 1px solid var(--border);
            border-radius: 8px;
            padding: 24px;
            margin-bottom: 20px;
        }

        .setup-card h2 {
            font-size: 1.2rem;
            margin-bottom: 16px;
            display: flex;
            align-items: center;
            gap: 8px;
        }

        .step-number {
            background: var(--blue);
            color: var(--bg-dark);
            width: 28px;
            height: 28px;
            border-radius: 50%;
            display: flex;
            align-items: center;
            justify-content: center;
            font-weight: 600;
            font-size: 0.9rem;
        }

        .step-number.done {
            background: var(--green);
        }

        .form-group {
            margin-bottom: 16px;
        }

        .form-group label {
            display: block;
            font-size: 0.85rem;
            color: var(--text-secondary);
            margin-bottom: 6px;
        }

        .form-group input {
            width: 100%;
            padding: 10px 12px;
            background: var(--bg-dark);
            border: 1px solid var(--border);
            border-radius: 4px;
            color: var(--text-primary);
            font-family: var(--font-mono);
            font-size: 0.9rem;
        }

        .form-group input:focus {
            outline: none;
            border-color: var(--blue);
        }

        .form-group .hint {
            font-size: 0.8rem;
            color: var(--text-muted);
            margin-top: 4px;
        }

        .checkbox-group {
            display: flex;
            align-items: center;
            gap: 8px;
        }

        .checkbox-group input[type="checkbox"] {
            width: 18px;
            height: 18px;
        }

        .btn {
            padding: 10px 20px;
            border-radius: 6px;
            font-size: 0.9rem;
            font-weight: 500;
            cursor: pointer;
            border: 1px solid var(--border);
            transition: all 0.15s ease;
            margin-right: 8px;
        }

        .btn-primary {
            background: var(--green);
            color: var(--bg-dark);
            border-color: var(--green);
        }

        .btn-primary:hover {
            opacity: 0.9;
        }

        .btn-primary:disabled {
            opacity: 0.5;
            cursor: not-allowed;
        }

        .btn-secondary {
            background: transparent;
            color: var(--text-secondary);
        }

        .btn-secondary:hover {
            background: var(--bg-card-hover);
        }

        .output-box {
            background: var(--bg-dark);
            border: 1px solid var(--border);
            border-radius: 4px;
            padding: 12px;
            font-family: var(--font-mono);
            font-size: 0.8rem;
            white-space: pre-wrap;
            max-height: 200px;
            overflow-y: auto;
            margin-top: 12px;
            display: none;
        }

        .output-box.visible {
            display: block;
        }

        .output-box.success {
            border-color: var(--green);
        }

        .output-box.error {
            border-color: var(--red);
            color: var(--red);
        }

        .status-message {
            padding: 12px;
            border-radius: 4px;
            margin-top: 12px;
            font-size: 0.9rem;
        }

        .status-message.success {
            background: rgba(63, 185, 80, 0.1);
            border: 1px solid var(--green);
            color: var(--green);
        }

        .status-message.error {
            background: rgba(248, 81, 73, 0.1);
            border: 1px solid var(--red);
            color: var(--red);
        }

        .hidden { display: none !important; }

        .loading {
            display: inline-block;
            width: 16px;
            height: 16px;
            border: 2px solid var(--border);
            border-top-color: var(--blue);
            border-radius: 50%;
            animation: spin 1s linear infinite;
            margin-right: 8px;
        }

        @keyframes spin {
            to { transform: rotate(360deg); }
        }

        .mode-tabs {
            display: flex;
            gap: 0;
            margin-bottom: 20px;
            border: 1px solid var(--border);
            border-radius: 6px;
            overflow: hidden;
        }

        .mode-tab {
            flex: 1;
            padding: 12px 16px;
            background: var(--bg-dark);
            border: none;
            color: var(--text-secondary);
            cursor: pointer;
            font-size: 0.9rem;
            font-weight: 500;
            transition: all 0.15s ease;
        }

        .mode-tab:not(:last-child) {
            border-right: 1px solid var(--border);
        }

        .mode-tab.active {
            background: var(--bg-card);
            color: var(--text-primary);
        }

        .mode-tab:hover:not(.active) {
            background: var(--bg-card-hover);
        }

        .workspace-info {
            background: var(--bg-dark);
            border: 1px solid var(--green);
            border-radius: 6px;
            padding: 16px;
            margin-top: 12px;
        }

        .workspace-info .name {
            font-weight: 600;
            color: var(--green);
            margin-bottom: 4px;
        }

        .workspace-info .path {
            font-family: var(--font-mono);
            font-size: 0.85rem;
            color: var(--text-secondary);
            margin-bottom: 8px;
        }

        .workspace-info .rigs {
            font-size: 0.85rem;
            color: var(--text-muted);
        }

        .workspace-info .rigs span {
            background: var(--bg-card);
            padding: 2px 8px;
            border-radius: 4px;
            margin-right: 6px;
            font-family: var(--font-mono);
        }
    </style>
</head>
<body>
    <div class="setup-container">
        <div class="setup-header">
            <pre style="font-size:10px;line-height:1.1;color:#58a6ff;margin:0 0 16px 0;white-space:pre;font-family:monospace;"> __       __  ________  __        ______    ______   __       __  ________                        
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
    \$$     \$$$$$$         \$$$$$$  \$$   \$$  \$$$$$$     \$$     \$$$$$$  \$$      \$$ \$$   \$$</pre>
            <p>Let's set up your workspace</p>
        </div>

        <!-- Mode selection tabs -->
        <div class="mode-tabs">
            <button class="mode-tab active" id="tab-existing" onclick="showMode('existing')">Use Existing</button>
            <button class="mode-tab" id="tab-create" onclick="showMode('create')">Create New</button>
        </div>

        <!-- Existing Workspace Mode -->
        <div class="setup-card" id="mode-existing">
            <h2>Open Existing Workspace</h2>
            <p style="color: var(--text-secondary); margin-bottom: 16px; font-size: 0.9rem;">
                Enter the path to an existing Gas Town workspace.
            </p>
            <div class="form-group">
                <label>Workspace Path</label>
                <input type="text" id="existing-path" placeholder="~/gt" value="~/gt">
                <div class="hint">Path to your Gas Town HQ directory</div>
            </div>
            <button class="btn btn-primary" id="check-btn" onclick="checkWorkspace()">Check Workspace</button>
            <div id="workspace-result"></div>
        </div>

        <!-- Create New Workspace Mode -->
        <div class="setup-card hidden" id="mode-create">
            <h2><span class="step-number" id="step1-num">1</span> Create Workspace (HQ)</h2>
            <div class="form-group">
                <label>Workspace Path</label>
                <input type="text" id="install-path" placeholder="~/gt" value="~/gt">
                <div class="hint">Where to create your Gas Town headquarters</div>
            </div>
            <div class="form-group">
                <label>Workspace Name (optional)</label>
                <input type="text" id="install-name" placeholder="my-workspace">
            </div>
            <div class="form-group checkbox-group">
                <input type="checkbox" id="install-git" checked>
                <label for="install-git">Initialize git repository</label>
            </div>
            <button class="btn btn-primary" id="install-btn" onclick="installWorkspace()">Create Workspace</button>
            <div class="output-box" id="install-output"></div>
        </div>

        <!-- Step 2: Add Rig (shown after create) -->
        <div class="setup-card hidden" id="step2">
            <h2><span class="step-number" id="step2-num">2</span> Add a Rig</h2>
            <p style="color: var(--text-secondary); margin-bottom: 16px; font-size: 0.9rem;">
                A rig is a project container. Add your first repository to get started.
            </p>
            <div class="form-group">
                <label>Rig Name</label>
                <input type="text" id="rig-name" placeholder="my-project">
                <div class="hint">Short name for this rig (no spaces)</div>
            </div>
            <div class="form-group">
                <label>Git URL</label>
                <input type="text" id="rig-url" placeholder="https://github.com/user/repo.git">
                <div class="hint">HTTPS or SSH URL of the repository</div>
            </div>
            <button class="btn btn-primary" id="rig-btn" onclick="addRig()">Add Rig</button>
            <button class="btn btn-secondary" onclick="skipRig()">Skip for now</button>
            <div class="output-box" id="rig-output"></div>
        </div>

        <!-- Step 3: Done -->
        <div class="setup-card hidden" id="step3">
            <h2><span class="step-number done">✓</span> Ready to Launch!</h2>
            <p style="color: var(--text-secondary); margin-bottom: 16px;">
                Your workspace is ready at <code id="workspace-path" style="background: var(--bg-dark); padding: 2px 6px; border-radius: 4px;">~/gt</code>
            </p>
            <button class="btn btn-primary" id="step3-launch-btn" onclick="launchFromStep3()">Launch Dashboard</button>
        </div>
    </div>

    <script>
        // CSRF protection: inject token into all POST requests
        (function() {
            var orig = window.fetch;
            var meta = document.querySelector('meta[name="dashboard-token"]');
            var token = meta ? meta.getAttribute('content') : '';
            window.fetch = function(url, opts) {
                opts = opts || {};
                if (opts.method && opts.method.toUpperCase() === 'POST' && token) {
                    opts.headers = opts.headers || {};
                    opts.headers['X-Dashboard-Token'] = token;
                }
                return orig.call(this, url, opts);
            };
        })();

        var workspacePath = '';

        function showMode(mode) {
            document.getElementById('tab-existing').className = mode === 'existing' ? 'mode-tab active' : 'mode-tab';
            document.getElementById('tab-create').className = mode === 'create' ? 'mode-tab active' : 'mode-tab';
            document.getElementById('mode-existing').className = mode === 'existing' ? 'setup-card' : 'setup-card hidden';
            document.getElementById('mode-create').className = mode === 'create' ? 'setup-card' : 'setup-card hidden';
            // Hide step2 and step3 when switching modes
            document.getElementById('step2').classList.add('hidden');
            document.getElementById('step3').classList.add('hidden');
        }

        function checkWorkspace() {
            var path = document.getElementById('existing-path').value.trim();
            var btn = document.getElementById('check-btn');
            var result = document.getElementById('workspace-result');

            if (!path) {
                alert('Please enter a workspace path');
                return;
            }

            btn.disabled = true;
            btn.innerHTML = '<span class="loading"></span>Checking...';

            fetch('/api/check-workspace', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ path: path })
            })
            .then(function(r) { return r.json(); })
            .then(function(data) {
                btn.disabled = false;
                btn.textContent = 'Check Workspace';

                if (data.valid) {
                    var rigsHtml = '';
                    if (data.rigs && data.rigs.length > 0) {
                        rigsHtml = '<div class="rigs">Rigs: ' + data.rigs.map(function(r) { return '<span>' + r + '</span>'; }).join('') + '</div>';
                    } else {
                        rigsHtml = '<div class="rigs">No rigs configured yet</div>';
                    }
                    result.innerHTML = '<div class="workspace-info">' +
                        '<div class="name">✓ ' + (data.name || 'Workspace') + '</div>' +
                        '<div class="path">' + data.path + '</div>' +
                        rigsHtml +
                        '</div>' +
                        '<div style="margin-top: 16px;">' +
                        '<button class="btn btn-primary" id="launch-btn" onclick="launchDashboard(\'' + data.path.replace(/'/g, "\\'") + '\')">Launch Dashboard</button>' +
                        '</div>';
                    workspacePath = data.path;
                } else {
                    result.innerHTML = '<div class="status-message error">' + (data.message || 'Not a valid workspace') + '</div>';
                }
            })
            .catch(function(err) {
                btn.disabled = false;
                btn.textContent = 'Check Workspace';
                result.innerHTML = '<div class="status-message error">Error: ' + err.message + '</div>';
            });
        }

        function launchDashboard(path) {
            var btn = document.getElementById('launch-btn');
            if (btn) {
                btn.disabled = true;
                btn.innerHTML = '<span class="loading"></span>Launching...';
            }

            fetch('/api/launch', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ path: path, port: parseInt(window.location.port) || 8080, bind: window.location.hostname === 'localhost' || window.location.hostname === '127.0.0.1' ? '127.0.0.1' : '0.0.0.0' })
            })
            .then(function(r) { return r.json(); })
            .then(function(data) {
                if (data.success) {
                    // Show loading message
                    document.body.innerHTML = '<div style="display:flex;flex-direction:column;align-items:center;justify-content:center;height:100vh;color:#e6edf3;font-family:monospace;background:#0d1117;">' +
                        '<div style="font-size:1.5rem;color:#58a6ff;margin-bottom:16px;">🚚</div>' +
                        '<div style="font-size:1rem;color:#8b949e;">loading control center...</div>' +
                        '</div>';
                    // Redirect to the new dashboard
                    if (data.redirect) {
                        window.location.href = data.redirect;
                    } else {
                        window.location.reload();
                    }
                } else {
                    if (btn) {
                        btn.disabled = false;
                        btn.textContent = 'Launch Dashboard';
                    }
                    alert('Failed to launch: ' + (data.error || 'Unknown error'));
                }
            })
            .catch(function(err) {
                if (btn) {
                    btn.disabled = false;
                    btn.textContent = 'Launch Dashboard';
                }
                alert('Error: ' + err.message);
            });
        }

        function installWorkspace() {
            var path = document.getElementById('install-path').value.trim();
            var name = document.getElementById('install-name').value.trim();
            var git = document.getElementById('install-git').checked;
            var btn = document.getElementById('install-btn');
            var output = document.getElementById('install-output');

            if (!path) {
                alert('Please enter a workspace path');
                return;
            }

            btn.disabled = true;
            btn.innerHTML = '<span class="loading"></span>Creating...';
            output.className = 'output-box visible';
            output.textContent = 'Running gt install...';

            fetch('/api/install', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ path: path, name: name, git: git })
            })
            .then(function(r) { return r.json(); })
            .then(function(data) {
                btn.disabled = false;
                btn.textContent = 'Create Workspace';
                output.textContent = data.output || data.message || data.error;

                if (data.success) {
                    output.className = 'output-box visible success';
                    workspacePath = path;
                    document.getElementById('step1-num').className = 'step-number done';
                    document.getElementById('step1-num').textContent = '✓';
                    document.getElementById('step2').classList.remove('hidden');
                    document.getElementById('workspace-path').textContent = path;
                } else {
                    output.className = 'output-box visible error';
                }
            })
            .catch(function(err) {
                btn.disabled = false;
                btn.textContent = 'Create Workspace';
                output.className = 'output-box visible error';
                output.textContent = 'Error: ' + err.message;
            });
        }

        function addRig() {
            var name = document.getElementById('rig-name').value.trim();
            var url = document.getElementById('rig-url').value.trim();
            var btn = document.getElementById('rig-btn');
            var output = document.getElementById('rig-output');

            if (!name || !url) {
                alert('Please enter both rig name and git URL');
                return;
            }

            btn.disabled = true;
            btn.innerHTML = '<span class="loading"></span>Adding rig...';
            output.className = 'output-box visible';
            output.textContent = 'Cloning repository and setting up rig...';

            fetch('/api/rig/add', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ name: name, gitUrl: url })
            })
            .then(function(r) { return r.json(); })
            .then(function(data) {
                btn.disabled = false;
                btn.textContent = 'Add Rig';
                output.textContent = data.output || data.message || data.error;

                if (data.success) {
                    output.className = 'output-box visible success';
                    document.getElementById('step2-num').className = 'step-number done';
                    document.getElementById('step2-num').textContent = '✓';
                    document.getElementById('step3').classList.remove('hidden');
                } else {
                    output.className = 'output-box visible error';
                }
            })
            .catch(function(err) {
                btn.disabled = false;
                btn.textContent = 'Add Rig';
                output.className = 'output-box visible error';
                output.textContent = 'Error: ' + err.message;
            });
        }

        function skipRig() {
            document.getElementById('step2-num').className = 'step-number done';
            document.getElementById('step2-num').textContent = '✓';
            document.getElementById('step3').classList.remove('hidden');
        }

        function launchFromStep3() {
            var path = workspacePath || document.getElementById('workspace-path').textContent;
            launchDashboard(path);
        }
    </script>
</body>
</html>
`
