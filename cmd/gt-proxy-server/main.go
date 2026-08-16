// gt-proxy-server is the mTLS proxy server for sandboxed polecat execution.
// It runs on the host and allows containers to call gt/bd and access git repos
// via authenticated, authorized HTTP endpoints.
package main

import (
	"context"
	"flag"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/jonbaldie/gastown/internal/proxy"
	"github.com/jonbaldie/gastown/internal/util"
)

// defaultAllowedSubcmds lists the safe subcommands for gt and bd.
// Dangerous subcommands (e.g. gt polecat, gt rig, gt admin, gt nuke) are excluded.
const defaultAllowedSubcmds = "" +
	"gt:prime,hook,done,mail,nudge,mol,status,handoff,version,convoy,sling;" +
	"bd:create,update,close,show,list,ready,dep,export,prime,stats,blocked,doctor"

func main() {
	var (
		configFile     = flag.String("config", "", "path to config file (default: ~/gt/.runtime/proxy/config.json)")
		listen         = flag.String("listen", "0.0.0.0:9876", "address to listen on")
		adminListen    = flag.String("admin-listen", "127.0.0.1:9877", "address for local admin HTTP server (use empty string to disable)")
		caDir          = flag.String("ca-dir", "", "directory for CA cert/key (default: ~/gt/.runtime/ca)")
		allowedCmds    = flag.String("allowed-cmds", "gt,bd", "comma-separated list of allowed commands")
		allowedSubcmds = flag.String("allowed-subcmds", discoverAllowedSubcmds(),
			`semicolon-separated list of "cmd:sub1,sub2,..." subcommand allowlists`)
		townRoot = flag.String("town-root", "", "Gas Town root directory (default: $GT_TOWN or ~/gt)")
	)
	flag.Parse()

	home, err := os.UserHomeDir()
	if err != nil {
		slog.Error("cannot determine home dir", "err", err)
		os.Exit(1)
	}

	// Determine config file path and load it.
	cfgPath := *configFile
	if cfgPath == "" {
		cfgPath = filepath.Join(home, "gt", ".runtime", "proxy", "config.json")
	}
	fileCfg, err := loadConfig(cfgPath)
	if err != nil {
		slog.Error("failed to load config file", "path", cfgPath, "err", err)
		os.Exit(1)
	}

	// Merge: flag values override config file values. We use flag.Visit to detect
	// which flags were explicitly set by the user, so we only override those fields.
	explicitFlags := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) { explicitFlags[f.Name] = true })

	if !explicitFlags["listen"] && fileCfg.ListenAddr != "" {
		*listen = fileCfg.ListenAddr
	}
	if !explicitFlags["admin-listen"] && fileCfg.AdminListenAddr != "" {
		*adminListen = fileCfg.AdminListenAddr
	}
	if !explicitFlags["ca-dir"] && fileCfg.CADir != "" {
		*caDir = fileCfg.CADir
	}
	if !explicitFlags["town-root"] && fileCfg.TownRoot != "" {
		*townRoot = fileCfg.TownRoot
	}
	if !explicitFlags["allowed-cmds"] && len(fileCfg.AllowedCommands) > 0 {
		*allowedCmds = strings.Join(fileCfg.AllowedCommands, ",")
	}
	if !explicitFlags["allowed-subcmds"] && len(fileCfg.AllowedSubcommands) > 0 {
		*allowedSubcmds = buildAllowedSubcmds(fileCfg.AllowedSubcommands)
	}

	if *caDir == "" {
		*caDir = filepath.Join(home, "gt", ".runtime", "ca")
	}

	if *townRoot == "" {
		if v := os.Getenv("GT_TOWN"); v != "" {
			*townRoot = v
		} else {
			*townRoot = filepath.Join(home, "gt")
		}
	}

	ca, err := proxy.LoadOrGenerateCA(*caDir)
	if err != nil {
		slog.Error("CA setup failed", "err", err)
		os.Exit(1)
	}
	slog.Info("CA loaded", "dir", *caDir)

	cmds := strings.Split(*allowedCmds, ",")
	for i := range cmds {
		cmds[i] = strings.TrimSpace(cmds[i])
	}

	// Parse extra_san_ips: convert strings to net.IP, skip invalid entries with a warning.
	var extraSANIPs []net.IP
	for _, s := range fileCfg.ExtraSANIPs {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		ip := net.ParseIP(s)
		if ip == nil {
			slog.Warn("extra_san_ips: invalid IP address — skipping", "entry", s)
			continue
		}
		extraSANIPs = append(extraSANIPs, ip)
	}

	// Parse extra_san_hosts: filter empty strings.
	var extraSANHosts []string
	for _, h := range fileCfg.ExtraSANHosts {
		h = strings.TrimSpace(h)
		if h != "" {
			extraSANHosts = append(extraSANHosts, h)
		}
	}

	cfg := proxy.Config{
		ListenAddr:         *listen,
		AdminListenAddr:    *adminListen,
		AllowedCommands:    cmds,
		AllowedSubcommands: parseAllowedSubcmds(*allowedSubcmds),
		TownRoot:           *townRoot,
		ExtraSANIPs:        extraSANIPs,
		ExtraSANHosts:      extraSANHosts,
	}

	srv, err := proxy.New(cfg, ca)
	if err != nil {
		slog.Error("invalid server config", "err", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := srv.Start(ctx); err != nil {
		slog.Error("server error", "err", err)
		os.Exit(1)
	}
}

// discoverAllowedSubcmds calls "gt proxy-subcmds" to auto-discover the allowed
// subcommand list. Falls back to defaultAllowedSubcmds if the command is
// unavailable or returns empty output.
func discoverAllowedSubcmds() string {
	cmd := exec.Command("gt", "proxy-subcmds")
	util.SetDetachedProcessGroup(cmd)
	out, err := cmd.Output()
	if err != nil {
		slog.Debug("gt proxy-subcmds discovery failed, using built-in default", "err", err)
		return defaultAllowedSubcmds
	}
	result := strings.TrimSpace(string(out))
	if result == "" {
		return defaultAllowedSubcmds
	}
	return result
}

// buildAllowedSubcmds serializes a map[string][]string back into the semicolon-separated
// "cmd:sub1,sub2,..." format expected by parseAllowedSubcmds.
func buildAllowedSubcmds(m map[string][]string) string {
	parts := make([]string, 0, len(m))
	for cmd, subs := range m {
		parts = append(parts, cmd+":"+strings.Join(subs, ","))
	}
	return strings.Join(parts, ";")
}

// parseAllowedSubcmds parses a string of the form
// "gt:prime,hook,done;bd:create,update,close" into a map of command → subcommand set.
func parseAllowedSubcmds(s string) map[string][]string {
	if s == "" {
		return nil
	}
	result := make(map[string][]string)
	for _, part := range strings.Split(s, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		idx := strings.Index(part, ":")
		if idx < 0 {
			continue
		}
		cmd := strings.TrimSpace(part[:idx])
		subsStr := strings.TrimSpace(part[idx+1:])
		var subs []string
		for _, sub := range strings.Split(subsStr, ",") {
			sub = strings.TrimSpace(sub)
			if sub != "" {
				subs = append(subs, sub)
			}
		}
		if cmd != "" && len(subs) > 0 {
			result[cmd] = subs
		}
	}
	return result
}
