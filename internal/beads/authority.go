package beads

import (
	"path/filepath"

	beadsdk "github.com/steveyegge/beads"
)

// Authority is the Beads routing module. Callers ask for a Session for a
// bead ID; prefix routes, redirects, and fallbacks stay inside.
type Authority struct {
	townRoot    string
	fallbackDir string
}

// NewAuthority builds an Authority rooted at a town directory.
// The fallback Beads directory is <townRoot>/.beads.
func NewAuthority(townRoot string) *Authority {
	fallback := ""
	if townRoot != "" {
		fallback = filepath.Join(townRoot, ".beads")
	}
	return &Authority{townRoot: townRoot, fallbackDir: fallback}
}

// NewAuthorityFromBeadsDir builds an Authority from a caller's Beads directory.
// Town routes are discovered by walking up from that directory. If no town is
// found, ForBead falls back to beadsDir.
func NewAuthorityFromBeadsDir(beadsDir string) *Authority {
	townRoot := ""
	if beadsDir != "" {
		townRoot = FindTownRoot(filepath.Dir(beadsDir))
	}
	return &Authority{townRoot: townRoot, fallbackDir: beadsDir}
}

// ForBead returns a Session bound to the database that owns beadID.
func (a *Authority) ForBead(beadID string) Session {
	if a == nil {
		return Session{beadID: beadID}
	}
	fallback := a.fallbackDir
	s := Session{
		beadID:   beadID,
		townRoot: a.townRoot,
		beadsDir: fallback,
		workDir:  parentDir(fallback),
	}
	prefix := ExtractPrefix(beadID)
	if prefix == "" {
		return s
	}

	routesDir := fallback
	if a.townRoot != "" {
		routesDir = filepath.Join(a.townRoot, ".beads")
	}
	routes, err := LoadRoutes(routesDir)
	if len(routes) == 0 || err != nil {
		townRoot := a.townRoot
		if townRoot == "" && fallback != "" {
			townRoot = FindTownRoot(filepath.Dir(fallback))
		}
		if townRoot != "" {
			townBeads := filepath.Join(townRoot, ".beads")
			if townBeads != routesDir {
				routesDir = townBeads
				routes, err = LoadRoutes(routesDir)
			}
		}
	}
	if err != nil || len(routes) == 0 {
		return s
	}

	for _, r := range routes {
		if r.Prefix != prefix {
			continue
		}
		townRoot := a.townRoot
		if townRoot == "" {
			townRoot = filepath.Dir(routesDir)
		}
		beadsDir := ResolveBeadsDir(routesDir)
		if r.Path != "." {
			beadsDir = ResolveBeadsDir(filepath.Join(townRoot, r.Path))
		}
		return Session{
			beadID:   beadID,
			townRoot: townRoot,
			beadsDir: beadsDir,
			workDir:  parentDir(beadsDir),
			routed:   true,
		}
	}
	return s
}

// ForAgentBead returns a Session for an agent bead. Agent beads live in the
// town database even when their ID carries a rig prefix.
func (a *Authority) ForAgentBead(beadID string) Session {
	if a == nil {
		return Session{beadID: beadID}
	}
	townBeads := a.fallbackDir
	workDir := parentDir(townBeads)
	if a.townRoot != "" {
		townBeads = filepath.Join(a.townRoot, ".beads")
		workDir = a.townRoot
	}
	return Session{
		beadID:   beadID,
		townRoot: a.townRoot,
		beadsDir: townBeads,
		workDir:  workDir,
	}
}

// Session is a Beads database binding for one bead ID.
type Session struct {
	beadID   string
	townRoot string
	workDir  string
	beadsDir string
	routed   bool
	store    beadsdk.Storage
}

// BeadID returns the bead this Session was opened for.
func (s Session) BeadID() string { return s.beadID }

// BeadsDir returns the resolved .beads directory for this Session.
func (s Session) BeadsDir() string { return s.beadsDir }

// WorkDir returns the directory callers should use as bd's working directory.
// This is the parent of BeadsDir, after redirects.
func (s Session) WorkDir() string { return s.workDir }

// Routed reports whether prefix routing found an owning rig or town path.
func (s Session) Routed() bool { return s.routed }

// WithStore binds an in-process beads SDK adapter. Show, Update, Close, and
// Hook then use the store instead of the bd CLI.
func (s Session) WithStore(store beadsdk.Storage) Session {
	s.store = store
	return s
}

func (a *Authority) withFallback(dir string) *Authority {
	if a == nil {
		return &Authority{fallbackDir: dir}
	}
	cp := *a
	if dir != "" {
		cp.fallbackDir = dir
	}
	return &cp
}

// Client returns a Beads wrapper pinned to this Session's database.
// Routing is already applied, so the client does not re-route by prefix.
func (s Session) Client() *Beads {
	return &Beads{
		workDir:  s.workDir,
		beadsDir: s.beadsDir,
		townRoot: s.townRoot,
		noRoute:  true,
		store:    s.store,
	}
}

// Show returns the bead this Session was opened for.
func (s Session) Show() (*Issue, error) {
	if s.beadID == "" {
		return nil, ErrNotFound
	}
	return s.Client().Show(s.beadID)
}

// Update mutates the bead this Session was opened for.
func (s Session) Update(opts UpdateOptions) error {
	if s.beadID == "" {
		return ErrNotFound
	}
	return s.Client().Update(s.beadID, opts)
}

// Close closes the bead this Session was opened for.
func (s Session) Close(reason string) error {
	if s.beadID == "" {
		return ErrNotFound
	}
	if reason == "" {
		return s.Client().Close(s.beadID)
	}
	return s.Client().CloseWithReason(reason, s.beadID)
}

// Hook sets the bead status to hooked and assigns it to agent.
func (s Session) Hook(agent string) error {
	status := StatusHooked
	return s.Update(UpdateOptions{Status: &status, Assignee: &agent})
}

func parentDir(path string) string {
	if path == "" {
		return ""
	}
	return filepath.Dir(path)
}
