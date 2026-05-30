package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/steveyegge/gastown/internal/doltserver"
)

// DoltConfigCheck verifies shared-server beads dirs have explicit bd config.
type DoltConfigCheck struct {
	FixableCheck
	targets []doltConfigTarget
}

type doltConfigTarget struct {
	beadsDir string
	host     string
	port     int
	issues   []string
}

type doltConfigMetadata struct {
	DoltMode       string `json:"dolt_mode"`
	DoltServerHost string `json:"dolt_server_host"`
	DoltServerPort int    `json:"dolt_server_port"`
}

// NewDoltConfigCheck creates a check for explicit shared Dolt bd config keys.
func NewDoltConfigCheck() *DoltConfigCheck {
	return &DoltConfigCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "dolt-config",
				CheckDescription: "Verify shared Dolt beads configs are explicit",
				CheckCategory:    CategoryConfig,
			},
		},
	}
}

// Run checks every .beads/config.yaml that belongs to a server-mode beads dir.
func (c *DoltConfigCheck) Run(ctx *CheckContext) *CheckResult {
	c.targets = nil
	if ctx == nil || ctx.TownRoot == "" {
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusWarning,
			Message:  "No town root configured",
			Category: c.CheckCategory,
		}
	}

	defaultCfg := doltserver.DefaultConfig(ctx.TownRoot)
	var details []string
	seen := make(map[string]bool)
	err := filepath.WalkDir(ctx.TownRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		switch d.Name() {
		case ".git", ".dolt-data", "node_modules":
			return filepath.SkipDir
		case ".beads":
			if seen[path] {
				return filepath.SkipDir
			}
			seen[path] = true
			c.checkBeadsDir(ctx, path, defaultCfg.Host, defaultCfg.Port, &details)
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusWarning,
			Message:  fmt.Sprintf("Could not scan beads configs: %v", err),
			Category: c.CheckCategory,
		}
	}

	if len(c.targets) == 0 {
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusOK,
			Message:  "All shared Dolt configs are explicit",
			Category: c.CheckCategory,
		}
	}

	return &CheckResult{
		Name:     c.Name(),
		Status:   StatusError,
		Message:  fmt.Sprintf("%d shared Dolt config(s) missing explicit bd keys", len(c.targets)),
		Details:  details,
		FixHint:  "Run 'gt doctor --fix' to set storage.backend, dolt.server, and dolt.port",
		Category: c.CheckCategory,
	}
}

func (c *DoltConfigCheck) checkBeadsDir(ctx *CheckContext, beadsDir, defaultHost string, defaultPort int, details *[]string) {
	configPath := filepath.Join(beadsDir, "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return
	}

	meta, ok := sharedDoltMetadata(beadsDir)
	if !ok {
		return
	}
	host := strings.TrimSpace(meta.DoltServerHost)
	if host == "" {
		host = defaultHost
	}
	port := meta.DoltServerPort
	if port == 0 {
		port = doltPortFile(beadsDir)
	}
	if port == 0 {
		port = defaultPort
	}

	issues := doltConfigIssues(string(data))
	if len(issues) == 0 {
		return
	}

	rel, err := filepath.Rel(ctx.TownRoot, beadsDir)
	if err != nil {
		rel = beadsDir
	}
	*details = append(*details, fmt.Sprintf("%s/config.yaml: %s", rel, strings.Join(issues, ", ")))
	c.targets = append(c.targets, doltConfigTarget{beadsDir: beadsDir, host: host, port: port, issues: issues})
}

func sharedDoltMetadata(beadsDir string) (doltConfigMetadata, bool) {
	if meta, ok := readDoltConfigMetadata(filepath.Join(beadsDir, "metadata.json")); ok {
		return meta, true
	}
	redirectData, err := os.ReadFile(filepath.Join(beadsDir, "redirect"))
	if err != nil {
		return doltConfigMetadata{}, false
	}
	target := strings.TrimSpace(string(redirectData))
	if target == "" {
		return doltConfigMetadata{}, false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Clean(filepath.Join(filepath.Dir(beadsDir), target))
	}
	return readDoltConfigMetadata(filepath.Join(target, "metadata.json"))
}

func readDoltConfigMetadata(path string) (doltConfigMetadata, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return doltConfigMetadata{}, false
	}
	var meta doltConfigMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return doltConfigMetadata{}, false
	}
	return meta, meta.DoltMode == "server"
}

func doltPortFile(beadsDir string) int {
	data, err := os.ReadFile(filepath.Join(beadsDir, "dolt-server.port"))
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return port
}

func doltConfigIssues(content string) []string {
	values := configYAMLValues(content)
	var issues []string
	if value := values["storage.backend"]; value == "" {
		issues = append(issues, "storage.backend unset")
	} else if !strings.EqualFold(value, "dolt") {
		issues = append(issues, "storage.backend must be dolt")
	}
	if values["dolt.server"] == "" {
		issues = append(issues, "dolt.server unset")
	}
	if values["dolt.port"] == "" {
		issues = append(issues, "dolt.port unset")
	}
	return issues
}

func configYAMLValues(content string) map[string]string {
	values := make(map[string]string)
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		values[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return values
}

// Fix writes only the shared Dolt connection keys, preserving existing config.
func (c *DoltConfigCheck) Fix(ctx *CheckContext) error {
	if len(c.targets) == 0 {
		return nil
	}
	for _, target := range c.targets {
		if err := ensureDoltConfigKeys(target.beadsDir, target.host, target.port); err != nil {
			return err
		}
	}
	return nil
}

func ensureDoltConfigKeys(beadsDir, host string, port int) error {
	configPath := filepath.Join(beadsDir, "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", configPath, err)
	}
	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(content, "\n")
	wanted := map[string]string{
		"storage.backend": "storage.backend: dolt",
		"dolt.server":     fmt.Sprintf("dolt.server: %q", host),
		"dolt.port":       fmt.Sprintf("dolt.port: %d", port),
	}
	found := make(map[string]bool, len(wanted))

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, _, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if replacement, ok := wanted[key]; ok {
			lines[i] = replacement
			found[key] = true
		}
	}

	for _, key := range []string{"storage.backend", "dolt.server", "dolt.port"} {
		if !found[key] {
			lines = append(lines, wanted[key])
		}
	}

	newContent := strings.Join(lines, "\n")
	if !strings.HasSuffix(newContent, "\n") {
		newContent += "\n"
	}
	if newContent == content {
		return nil
	}
	return os.WriteFile(configPath, []byte(newContent), 0644)
}
