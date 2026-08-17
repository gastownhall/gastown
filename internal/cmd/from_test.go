package cmd

import (
	"bytes"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/doltserver"
	"github.com/jonbaldie/gastown/internal/from"
	"github.com/jonbaldie/gastown/internal/testutil"
	"github.com/jonbaldie/gastown/internal/workspace"
)

func TestFromEmptyParentFailsWithNoWrites(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "demo")
	if err := os.Mkdir(parent, 0755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(parent, "README.md"), []byte("not a repo\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	err := runFromRequest(fromRequest{Parent: parent})
	if err == nil {
		t.Fatal("expected error for a parent with no Git repositories")
	}
	if !strings.Contains(err.Error(), "no Git repositories") {
		t.Fatalf("error = %v, want it to mention no Git repositories", err)
	}

	townPath := filepath.Join(root, "demo.gt")
	if _, err := os.Stat(townPath); !os.IsNotExist(err) {
		t.Fatalf("empty parent must not create Town HQ at %s", townPath)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("read parent: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "README.md" {
		t.Fatalf("parent tree changed: %v", namesOf(entries))
	}
}

func TestFromHelpDistinguishesParentFromHQ(t *testing.T) {
	help := fromCmd.Short + "\n" + fromCmd.Long + "\n" + fromCmd.Use
	for _, want := range []string{
		"parent",
		"Town HQ",
		"gt install",
		"--dry-run",
	} {
		if !strings.Contains(help, want) {
			t.Fatalf("gt from help should mention %q, got:\n%s", want, help)
		}
	}
	if !strings.Contains(help, "first path is the parent") && !strings.Contains(help, "The first path is the parent") {
		t.Fatalf("gt from help should say the first path is the parent of repositories, got:\n%s", help)
	}
}

func TestFromCommandRegisteredAndExempt(t *testing.T) {
	found := false
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == "from" {
			found = true
			if cmd.GroupID != GroupWorkspace {
				t.Fatalf("from GroupID = %q, want %q", cmd.GroupID, GroupWorkspace)
			}
			break
		}
	}
	if !found {
		t.Fatal("from command not registered with rootCmd")
	}
	if !beadsExemptCommands["from"] {
		t.Fatal("from should be in beadsExemptCommands")
	}
	if !branchCheckExemptCommands["from"] {
		t.Fatal("from should be in branchCheckExemptCommands")
	}
}

func TestLocalRigBootstrapDocsRecommendFrom(t *testing.T) {
	root := findModuleRoot(t)
	body := readRepoFile(t, filepath.Join(root, "docs", "guides", "local-rig-bootstrap.md"))
	if !strings.Contains(body, "gt from") {
		t.Fatal("docs/guides/local-rig-bootstrap.md should recommend gt from for a parent folder of project repositories")
	}
	if !strings.Contains(body, "adopt") {
		t.Fatal("docs/guides/local-rig-bootstrap.md should still explain when to use adopt")
	}
	if !strings.Contains(body, "never uses `--adopt`") && !strings.Contains(body, "never uses --adopt") {
		t.Fatal("docs/guides/local-rig-bootstrap.md should say gt from never uses adopt")
	}
}

func TestFromDryRunMixedChildrenWritesNothing(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "demo")
	if err := os.Mkdir(parent, 0755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}

	httpsRepo := initFromChildRepo(t, parent, "auth")
	setOrigin(t, httpsRepo, "https://github.com/acme/auth.git")
	httpsHEAD := gitHEAD(t, httpsRepo)
	httpsOrigin := gitOrigin(t, httpsRepo)

	sshRepo := initFromChildRepo(t, parent, "billing")
	setOrigin(t, sshRepo, "git@github.com:acme/billing.git")
	sshOrigin := gitOrigin(t, sshRepo)

	localRepo := initFromChildRepo(t, parent, "my-app")
	localHEAD := gitHEAD(t, localRepo)
	localOrigin := gitOrigin(t, localRepo)

	if err := os.WriteFile(filepath.Join(parent, "compose.yaml"), []byte("services: {}\n"), 0644); err != nil {
		t.Fatalf("write compose.yaml: %v", err)
	}
	if err := os.Mkdir(filepath.Join(parent, "docs"), 0755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	if err := os.Mkdir(filepath.Join(parent, ".cache"), 0755); err != nil {
		t.Fatalf("mkdir .cache: %v", err)
	}
	nested := filepath.Join(httpsRepo, "vendor", "leftpad")
	initFromChildRepo(t, filepath.Dir(nested), "leftpad")

	var out bytes.Buffer
	err := runFromRequest(fromRequest{Parent: parent, DryRun: true, Stdout: &out})
	if err != nil {
		t.Fatalf("dry-run: %v\n%s", err, out.String())
	}

	got := out.String()
	townPath := filepath.Join(root, "demo.gt")
	if !strings.Contains(got, townPath) {
		t.Fatalf("dry-run should print default Town path %s, got:\n%s", townPath, got)
	}
	if !strings.Contains(got, "create") {
		t.Fatalf("dry-run should say the Town would be created, got:\n%s", got)
	}
	if !strings.Contains(got, "auth") || !strings.Contains(got, "https://github.com/acme/auth.git") {
		t.Fatalf("dry-run should plan HTTPS origin for auth, got:\n%s", got)
	}
	if !strings.Contains(got, "billing") || !strings.Contains(got, "git@github.com:acme/billing.git") {
		t.Fatalf("dry-run should plan SSH origin for billing, got:\n%s", got)
	}
	if !strings.Contains(got, "my_app") {
		t.Fatalf("hyphenated folder my-app should become Rig my_app, got:\n%s", got)
	}
	wantFileURL := (&url.URL{Scheme: "file", Path: localRepo}).String()
	if !strings.Contains(got, wantFileURL) {
		t.Fatalf("dry-run should plan file URL %s for local-only child, got:\n%s", wantFileURL, got)
	}
	if !strings.Contains(got, "compose.yaml") {
		t.Fatalf("dry-run should report leftover compose.yaml, got:\n%s", got)
	}
	if strings.Contains(got, "docs") && strings.Contains(got, "[add]") && strings.Contains(got, filepath.Join(parent, "docs")) {
		t.Fatalf("non-Git docs folder must not become a Rig, got:\n%s", got)
	}
	if strings.Contains(got, "leftpad") {
		t.Fatalf("nested Git repositories must not be imported, got:\n%s", got)
	}
	if strings.Contains(got, ".cache") {
		t.Fatalf("hidden directories must not be scanned as children, got:\n%s", got)
	}
	if _, err := os.Stat(townPath); !os.IsNotExist(err) {
		t.Fatalf("dry-run must not create Town HQ at %s", townPath)
	}
	if gitHEAD(t, httpsRepo) != httpsHEAD {
		t.Fatal("dry-run must not change source HEAD")
	}
	if gitHEAD(t, localRepo) != localHEAD {
		t.Fatal("dry-run must not change local-only source HEAD")
	}
	if gitOrigin(t, httpsRepo) != httpsOrigin {
		t.Fatal("dry-run must not change HTTPS origin")
	}
	if gitOrigin(t, sshRepo) != sshOrigin {
		t.Fatal("dry-run must not change SSH origin")
	}
	if gitOrigin(t, localRepo) != localOrigin {
		t.Fatal("dry-run must not add an origin to a local-only child")
	}
}

func TestFromTownPathInsideParentFailsBeforeWrites(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "demo")
	initFromChildRepo(t, parent, "auth")
	inside := filepath.Join(parent, "gt")

	err := runFromRequest(fromRequest{Parent: parent, Town: inside, DryRun: true})
	if err == nil {
		t.Fatal("expected error when Town path is inside the parent folder")
	}
	if !strings.Contains(err.Error(), "inside") {
		t.Fatalf("error = %v, want it to mention inside", err)
	}
	if _, statErr := os.Stat(inside); !os.IsNotExist(statErr) {
		t.Fatalf("must not write Town inside parent: %s", inside)
	}
}

func TestFromNameCollisionFailsBeforeWrites(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "demo")
	initFromChildRepo(t, parent, "my-app")
	initFromChildRepo(t, parent, "my.app")

	err := runFromRequest(fromRequest{Parent: parent, DryRun: true})
	if err == nil {
		t.Fatal("expected error when two folders sanitize to the same Rig name")
	}
	if !strings.Contains(err.Error(), "my_app") {
		t.Fatalf("error = %v, want the colliding sanitized name", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "demo.gt")); !os.IsNotExist(statErr) {
		t.Fatal("name collision must not create a Town")
	}
}

func TestFromReservedHQNameFailsBeforeWrites(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "demo")
	initFromChildRepo(t, parent, "hq")

	err := runFromRequest(fromRequest{Parent: parent, DryRun: true})
	if err == nil {
		t.Fatal("expected error when a folder sanitizes to reserved name hq")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "hq") {
		t.Fatalf("error = %v, want reserved name hq", err)
	}
}

func TestFromParentAsOnlyRepoPlansOneRig(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "solo")
	initGitRepoAt(t, parent)
	setOrigin(t, parent, "https://github.com/acme/solo.git")

	plan, err := from.Prepare(parent, "")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if len(plan.Rigs) != 1 {
		t.Fatalf("got %d rigs, want 1 from parent-as-only-repo", len(plan.Rigs))
	}
	if plan.Rigs[0].Name != "solo" {
		t.Fatalf("rig name = %q, want solo", plan.Rigs[0].Name)
	}
	if plan.Rigs[0].GitURL != "https://github.com/acme/solo.git" {
		t.Fatalf("git URL = %q, want origin", plan.Rigs[0].GitURL)
	}
}

func TestFromParentGitWithChildrenUsesChildrenOnly(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "mono")
	initGitRepoAt(t, parent)
	initFromChildRepo(t, parent, "auth")
	initFromChildRepo(t, parent, "billing")

	plan, err := from.Prepare(parent, "")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if len(plan.Rigs) != 2 {
		t.Fatalf("got %d rigs, want 2 children only", len(plan.Rigs))
	}
	for _, r := range plan.Rigs {
		if r.Name == "mono" {
			t.Fatal("parent container repository must not become a Rig when it has Git children")
		}
	}
}

func TestFromTownHQParentIsRefused(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "demo")
	if err := os.MkdirAll(filepath.Join(parent, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir town: %v", err)
	}
	if err := os.WriteFile(filepath.Join(parent, "mayor", "town.json"), []byte(`{"type":"town","version":2,"name":"demo"}`), 0644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}
	initFromChildRepo(t, parent, "auth")

	err := runFromRequest(fromRequest{Parent: parent, DryRun: true})
	if err == nil {
		t.Fatal("expected error when parent is a Town HQ")
	}
	if !strings.Contains(err.Error(), "Gas Town HQ") {
		t.Fatalf("error = %v, want Gas Town HQ", err)
	}
}

func TestFromSkipsSubmoduleGitFiles(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "demo")
	initFromChildRepo(t, parent, "auth")
	sub := filepath.Join(parent, "vendorlib")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatalf("mkdir submodule: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, ".git"), []byte("gitdir: ../auth/.git/modules/vendorlib\n"), 0644); err != nil {
		t.Fatalf("write git file: %v", err)
	}

	var out bytes.Buffer
	if err := runFromRequest(fromRequest{Parent: parent, DryRun: true, Stdout: &out}); err != nil {
		t.Fatalf("dry-run: %v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "vendorlib") {
		t.Fatalf("submodule git files must not become Rigs, got:\n%s", out.String())
	}
}

func TestFromExistingNonTownPathFails(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "demo")
	initFromChildRepo(t, parent, "auth")
	notTown := filepath.Join(root, "random")
	if err := os.Mkdir(notTown, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(notTown, "notes.txt"), []byte("hi\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	err := runFromRequest(fromRequest{Parent: parent, Town: notTown, DryRun: true})
	if err == nil {
		t.Fatal("expected error when Town path exists but is not a Town")
	}
	if !strings.Contains(err.Error(), "not a Gas Town") {
		t.Fatalf("error = %v, want not a Gas Town HQ", err)
	}
}

func TestFromSkipsAssembledRigDirectories(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "demo")
	initFromChildRepo(t, parent, "auth")
	assembled := filepath.Join(parent, "old_rig")
	if err := os.MkdirAll(filepath.Join(assembled, ".repo.git"), 0755); err != nil {
		t.Fatalf("mkdir assembled: %v", err)
	}
	if err := os.WriteFile(filepath.Join(assembled, "config.json"), []byte(`{"type":"rig","name":"old_rig"}`), 0644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}

	var out bytes.Buffer
	if err := runFromRequest(fromRequest{Parent: parent, DryRun: true, Stdout: &out}); err != nil {
		t.Fatalf("dry-run: %v\n%s", err, out.String())
	}
	got := out.String()
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "old_rig") && strings.Contains(line, "[add]") {
			t.Fatalf("assembled Rig directory must not be added, got:\n%s", got)
		}
	}
	if !strings.Contains(got, "assembled Rig") {
		t.Fatalf("report should say assembled Rig was skipped, got:\n%s", got)
	}
}

func TestFromParentGitWithOnlyAssembledChildrenUsesParent(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "mono")
	initGitRepoAt(t, parent)
	assembled := filepath.Join(parent, "old_rig")
	if err := os.MkdirAll(filepath.Join(assembled, ".repo.git"), 0755); err != nil {
		t.Fatalf("mkdir assembled: %v", err)
	}
	if err := os.WriteFile(filepath.Join(assembled, "config.json"), []byte(`{"type":"rig","name":"old_rig"}`), 0644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}

	plan, err := from.Prepare(parent, "")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if len(plan.Rigs) != 1 || plan.Rigs[0].Name != "mono" {
		t.Fatalf("got rigs %+v, want one Rig from the parent", plan.Rigs)
	}
	foundAssembled := false
	for _, skipped := range plan.Skipped {
		if strings.Contains(skipped, "assembled Rig") {
			foundAssembled = true
		}
	}
	if !foundAssembled {
		t.Fatalf("assembled child should be skipped, got %v", plan.Skipped)
	}
}

func TestFromPrefixCollisionFailsBeforeWrites(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "demo")
	initFromChildRepo(t, parent, "auth")
	initFromChildRepo(t, parent, "authorization")

	err := runFromRequest(fromRequest{Parent: parent, DryRun: true})
	if err == nil {
		t.Fatal("expected prefix collision between auth and authorization")
	}
	if !strings.Contains(err.Error(), "prefix") {
		t.Fatalf("error = %v, want prefix collision", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "demo.gt")); !os.IsNotExist(statErr) {
		t.Fatal("prefix collision must not create a Town")
	}
}

func TestFromConflictsWhenRigNamePointsAtDifferentPath(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "demo")
	initFromChildRepo(t, parent, "auth")
	town := filepath.Join(root, "town")
	if err := os.MkdirAll(filepath.Join(town, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir town: %v", err)
	}
	if err := os.WriteFile(filepath.Join(town, "mayor", "town.json"), []byte(`{"type":"town","version":2,"name":"town"}`), 0644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}
	rigsJSON := `{"version":1,"rigs":{"auth":{"git_url":"https://example.com/other.git","local_repo":"/other/auth"}}}`
	if err := os.WriteFile(filepath.Join(town, "mayor", "rigs.json"), []byte(rigsJSON), 0644); err != nil {
		t.Fatalf("write rigs.json: %v", err)
	}

	err := runFromRequest(fromRequest{Parent: parent, Town: town, DryRun: true})
	if err == nil {
		t.Fatal("expected conflict when Rig name is taken by a different local path")
	}
	if !strings.Contains(err.Error(), "different source") {
		t.Fatalf("error = %v, want different source conflict", err)
	}
}

func TestFromConflictsWhenRigNameMatchesURLButNotPath(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "demo")
	auth := initFromChildRepo(t, parent, "auth")
	setOrigin(t, auth, "https://github.com/acme/auth.git")
	town := filepath.Join(root, "town")
	if err := os.MkdirAll(filepath.Join(town, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir town: %v", err)
	}
	if err := os.WriteFile(filepath.Join(town, "mayor", "town.json"), []byte(`{"type":"town","version":2,"name":"town"}`), 0644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}
	rigsJSON := `{"version":1,"rigs":{"auth":{"git_url":"https://github.com/acme/auth.git","local_repo":"/other/auth"}}}`
	if err := os.WriteFile(filepath.Join(town, "mayor", "rigs.json"), []byte(rigsJSON), 0644); err != nil {
		t.Fatalf("write rigs.json: %v", err)
	}

	err := runFromRequest(fromRequest{Parent: parent, Town: town, DryRun: true})
	if err == nil {
		t.Fatal("expected conflict when Rig name is taken by a different local path")
	}
	if !strings.Contains(err.Error(), "different source") {
		t.Fatalf("error = %v, want different source conflict", err)
	}
}

func TestFromExistingTownPrefixCollisionOmitsPrefixFlag(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "demo")
	initFromChildRepo(t, parent, "auth")
	town := filepath.Join(root, "town")
	if err := os.MkdirAll(filepath.Join(town, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir town: %v", err)
	}
	if err := os.WriteFile(filepath.Join(town, "mayor", "town.json"), []byte(`{"type":"town","version":2,"name":"town"}`), 0644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(town, "mayor", "rigs.json"), []byte(`{"version":1,"rigs":{}}`), 0644); err != nil {
		t.Fatalf("write rigs.json: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(town, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(town, ".beads", "routes.jsonl"), []byte("{\"prefix\":\"au-\",\"path\":\"other\"}\n"), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	err := runFromRequest(fromRequest{Parent: parent, Town: town, DryRun: true})
	if err == nil {
		t.Fatal("expected prefix collision against the existing Town")
	}
	if !strings.Contains(err.Error(), "prefix") {
		t.Fatalf("error = %v, want prefix collision", err)
	}
	if strings.Contains(err.Error(), "--prefix") {
		t.Fatalf("gt from has no --prefix flag, got %v", err)
	}
}

func TestFromRelativeParentResolvesFromCwd(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "services")
	initFromChildRepo(t, parent, "auth")

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	plan, err := from.Prepare("services", "")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if plan.ParentAbs != parent {
		t.Fatalf("ParentAbs = %q, want %q", plan.ParentAbs, parent)
	}
	if plan.TownAbs != filepath.Join(root, "services.gt") {
		t.Fatalf("TownAbs = %q, want sibling services.gt", plan.TownAbs)
	}
}

func TestFromCreatesTownAndRigsFromMixedChildren(t *testing.T) {
	setupFromApply(t)
	root := t.TempDir()
	parent := filepath.Join(root, "demo")
	httpsRepo := initFromChildRepo(t, parent, "auth")
	setOrigin(t, httpsRepo, "https://github.com/acme/auth.git")
	httpsHEAD := gitHEAD(t, httpsRepo)
	httpsOrigin := gitOrigin(t, httpsRepo)
	sshRepo := initFromChildRepo(t, parent, "billing")
	setOrigin(t, sshRepo, "git@github.com:acme/billing.git")
	sshOrigin := gitOrigin(t, sshRepo)
	localRepo := initFromChildRepo(t, parent, "my-app")
	localHEAD := gitHEAD(t, localRepo)
	localOrigin := gitOrigin(t, localRepo)
	if err := os.WriteFile(filepath.Join(parent, "compose.yaml"), []byte("services: {}\n"), 0644); err != nil {
		t.Fatalf("write compose.yaml: %v", err)
	}
	rewriteCloneURL(t, "https://github.com/acme/auth.git", httpsRepo)
	rewriteCloneURL(t, "git@github.com:acme/billing.git", sshRepo)

	townPath := filepath.Join(root, "demo.gt")
	testutil.ReapOwnedDoltOnCleanup(t, townPath)

	var out bytes.Buffer
	if err := runFromRequest(fromRequest{Parent: parent, Stdout: &out}); err != nil {
		t.Fatalf("gt from: %v\n%s", err, out.String())
	}

	if ok, err := workspace.IsWorkspace(townPath); err != nil || !ok {
		t.Fatalf("expected Town HQ at %s, isWorkspace=%v err=%v\n%s", townPath, ok, err, out.String())
	}
	if _, err := os.Stat(filepath.Join(townPath, ".git")); err != nil {
		t.Fatalf("new Town should have Git history: %v", err)
	}
	rigs := loadFromRigs(t, townPath)
	if _, ok := rigs["docs"]; ok {
		t.Fatal("compose.yaml / non-Git folders must not become Rigs")
	}
	if _, ok := rigs["compose"]; ok {
		t.Fatal("compose.yaml must not become a Rig")
	}
	assertRig(t, rigs, "auth", "https://github.com/acme/auth.git", httpsRepo, "au")
	assertRig(t, rigs, "billing", "git@github.com:acme/billing.git", sshRepo, "bi")
	assertRig(t, rigs, "my_app", (&url.URL{Scheme: "file", Path: localRepo}).String(), localRepo, "ma")
	if gitHEAD(t, httpsRepo) != httpsHEAD {
		t.Fatal("source HEAD changed for HTTPS child")
	}
	if gitHEAD(t, localRepo) != localHEAD {
		t.Fatal("source HEAD changed for local-only child")
	}
	if gitOrigin(t, httpsRepo) != httpsOrigin {
		t.Fatal("source origin changed for HTTPS child")
	}
	if gitOrigin(t, sshRepo) != sshOrigin {
		t.Fatal("source origin changed for SSH child")
	}
	if gitOrigin(t, localRepo) != localOrigin {
		t.Fatal("source origin changed for local-only child")
	}
	if _, err := os.Stat(filepath.Join(httpsRepo, "README.md")); err != nil {
		t.Fatal("source working tree was changed")
	}
	if strings.Contains(out.String(), "compose.yaml") == false {
		t.Fatalf("report should mention leftover compose.yaml, got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), composeReminder()) {
		t.Fatalf("report should remind that Compose stays in the parent folder, got:\n%s", out.String())
	}
	assertNoCrewMembers(t, townPath)
	assertNotMachineEnabled(t)

	var second bytes.Buffer
	if err := runFromRequest(fromRequest{Parent: parent, Stdout: &second}); err != nil {
		t.Fatalf("second gt from: %v\n%s", err, second.String())
	}
	if len(loadFromRigs(t, townPath)) != 3 {
		t.Fatalf("second run duplicated Rigs: %+v", loadFromRigs(t, townPath))
	}
	if !strings.Contains(second.String(), "reused") && !strings.Contains(second.String(), "Skipped") {
		t.Fatalf("second run should reuse Town and skip existing Rigs, got:\n%s", second.String())
	}

	initFromChildRepo(t, parent, "payments")
	var third bytes.Buffer
	if err := runFromRequest(fromRequest{Parent: parent, Stdout: &third}); err != nil {
		t.Fatalf("third gt from: %v\n%s", err, third.String())
	}
	rigs = loadFromRigs(t, townPath)
	if _, ok := rigs["payments"]; !ok {
		t.Fatalf("second new child should be added, rigs=%v\n%s", keysOf(rigs), third.String())
	}
	if len(rigs) != 4 {
		t.Fatalf("got %d rigs, want 4 after adding payments: %v", len(rigs), keysOf(rigs))
	}
}

func TestFromReusesExistingTown(t *testing.T) {
	setupFromApply(t)
	root := t.TempDir()
	parent := filepath.Join(root, "demo")
	initFromChildRepo(t, parent, "auth")
	townPath := filepath.Join(root, "existing-town")
	testutil.ReapOwnedDoltOnCleanup(t, townPath)

	if err := runFromRequest(fromRequest{Parent: parent, Town: townPath}); err != nil {
		t.Fatalf("first from: %v", err)
	}
	if _, err := os.Stat(filepath.Join(townPath, "mayor", "town.json")); err != nil {
		t.Fatalf("town.json: %v", err)
	}

	initFromChildRepo(t, parent, "billing")
	var out bytes.Buffer
	if err := runFromRequest(fromRequest{Parent: parent, Town: townPath, Stdout: &out}); err != nil {
		t.Fatalf("reuse from: %v\n%s", err, out.String())
	}
	second, err := os.Stat(filepath.Join(townPath, "mayor", "town.json"))
	if err != nil {
		t.Fatalf("town.json after reuse: %v", err)
	}
	if second.Size() == 0 {
		t.Fatal("reused Town lost town.json")
	}
	rigs := loadFromRigs(t, townPath)
	if _, ok := rigs["auth"]; !ok {
		t.Fatal("existing auth Rig missing after reuse")
	}
	if _, ok := rigs["billing"]; !ok {
		t.Fatal("new billing Rig was not added to existing Town")
	}
}

func TestFromReuseStartsDoltWhenStopped(t *testing.T) {
	setupFromApply(t)
	root := t.TempDir()
	parent := filepath.Join(root, "demo")
	initFromChildRepo(t, parent, "auth")
	townPath := filepath.Join(root, "existing-town")
	testutil.ReapOwnedDoltOnCleanup(t, townPath)

	if err := runFromRequest(fromRequest{Parent: parent, Town: townPath}); err != nil {
		t.Fatalf("first from: %v", err)
	}
	if err := doltserver.Stop(townPath); err != nil {
		t.Fatalf("stop Dolt: %v", err)
	}

	initFromChildRepo(t, parent, "billing")
	if err := runFromRequest(fromRequest{Parent: parent, Town: townPath}); err != nil {
		t.Fatalf("reuse from with Dolt stopped: %v", err)
	}
	rigs := loadFromRigs(t, townPath)
	if _, ok := rigs["billing"]; !ok {
		t.Fatal("new billing Rig was not added after Dolt restart")
	}
}

func TestFromParentAsOnlyRepoCreatesOneRig(t *testing.T) {
	setupFromApply(t)
	root := t.TempDir()
	parent := filepath.Join(root, "solo")
	initGitRepoAt(t, parent)
	head := gitHEAD(t, parent)
	townPath := filepath.Join(root, "solo.gt")
	testutil.ReapOwnedDoltOnCleanup(t, townPath)

	if err := runFromRequest(fromRequest{Parent: parent}); err != nil {
		t.Fatalf("gt from: %v", err)
	}
	rigs := loadFromRigs(t, townPath)
	if len(rigs) != 1 {
		t.Fatalf("got %d rigs, want 1", len(rigs))
	}
	assertRig(t, rigs, "solo", (&url.URL{Scheme: "file", Path: parent}).String(), parent, "so")
	if gitHEAD(t, parent) != head {
		t.Fatal("parent-as-only-repo HEAD changed")
	}
	if gitOrigin(t, parent) != "" {
		t.Fatal("parent-as-only-repo origin was rewritten")
	}
}

func TestFromApplyContinuesAfterOneRigFails(t *testing.T) {
	setupFromApply(t)
	root := t.TempDir()
	parent := filepath.Join(root, "demo")
	keep := initFromChildRepo(t, parent, "alpha")
	keepHEAD := gitHEAD(t, keep)
	fail := initFromChildRepo(t, parent, "broken")
	dead := "file:///no-such-from-clone-target"
	setOrigin(t, fail, dead)
	failOrigin := gitOrigin(t, fail)
	townPath := filepath.Join(root, "demo.gt")
	testutil.ReapOwnedDoltOnCleanup(t, townPath)

	var out bytes.Buffer
	err := runFromRequest(fromRequest{Parent: parent, Stdout: &out})
	if err == nil {
		t.Fatalf("expected a failure for the unreachable origin, got:\n%s", out.String())
	}
	if !strings.Contains(err.Error(), "broken") {
		t.Fatalf("error = %v, want the failed Rig named", err)
	}

	rigs := loadFromRigs(t, townPath)
	assertRig(t, rigs, "alpha", (&url.URL{Scheme: "file", Path: keep}).String(), keep, "al")
	if _, ok := rigs["broken"]; ok {
		t.Fatalf("failed Rig must not be registered, have %v", keysOf(rigs))
	}
	if gitHEAD(t, keep) != keepHEAD {
		t.Fatal("successful source HEAD changed")
	}
	if gitOrigin(t, fail) != failOrigin {
		t.Fatal("failed source origin changed")
	}
}

func namesOf(entries []os.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func initFromChildRepo(t *testing.T, parent, name string) string {
	t.Helper()
	if err := os.MkdirAll(parent, 0755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	repo := filepath.Join(parent, name)
	initGitRepoAt(t, repo)
	return repo
}

func initGitRepoAt(t *testing.T, repo string) {
	t.Helper()
	isolateGitConfig(t)
	if err := os.MkdirAll(repo, 0755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	runGitAt(t, repo, "init", "--initial-branch=main")
	runGitAt(t, repo, "config", "user.email", "test@test.com")
	runGitAt(t, repo, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# "+filepath.Base(repo)+"\n"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGitAt(t, repo, "add", ".")
	runGitAt(t, repo, "commit", "-m", "initial")
}

func setOrigin(t *testing.T, repo, origin string) {
	t.Helper()
	runGitAt(t, repo, "remote", "add", "origin", origin)
}

func gitHEAD(t *testing.T, repo string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func gitOrigin(t *testing.T, repo string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "config", "--get", "remote.origin.url")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func isolateGitConfig(t *testing.T) {
	t.Helper()
	if os.Getenv("GIT_CONFIG_GLOBAL") != "" && strings.Contains(os.Getenv("GIT_CONFIG_GLOBAL"), "from-test-gitconfig") {
		return
	}
	cfg := filepath.Join(t.TempDir(), "from-test-gitconfig")
	content := "[user]\n\tname = Test User\n\temail = test@test.com\n[init]\n\tdefaultBranch = main\n"
	if err := os.WriteFile(cfg, []byte(content), 0644); err != nil {
		t.Fatalf("write gitconfig: %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func runGitAt(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

func setupFromApply(t *testing.T) {
	t.Helper()
	origHome, err := os.UserHomeDir()
	if err == nil && origHome != "" {
		t.Setenv("PATH", filepath.Join(origHome, "go", "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("dolt not installed")
	}
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not installed")
	}
	isolateGitConfig(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "xdg-state"))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate Dolt port: %v", err)
	}
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	_ = ln.Close()
	t.Setenv("GT_DOLT_PORT", port)
	t.Setenv("BEADS_DOLT_PORT", port)
	t.Setenv("BEADS_DOLT_AUTO_START", "0")
}

func rewriteCloneURL(t *testing.T, remoteURL, localPath string) {
	t.Helper()
	cfg := os.Getenv("GIT_CONFIG_GLOBAL")
	if cfg == "" {
		t.Fatal("GIT_CONFIG_GLOBAL is not set")
	}
	fileURL := (&url.URL{Scheme: "file", Path: localPath}).String()
	f, err := os.OpenFile(cfg, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open gitconfig: %v", err)
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "\n[url %q]\n\tinsteadOf = %s\n", fileURL, remoteURL); err != nil {
		t.Fatalf("write insteadOf: %v", err)
	}
}

func loadFromRigs(t *testing.T, townPath string) map[string]config.RigEntry {
	t.Helper()
	cfg, err := config.LoadRigsConfig(filepath.Join(townPath, "mayor", "rigs.json"))
	if err != nil {
		t.Fatalf("load rigs.json: %v", err)
	}
	if cfg.Rigs == nil {
		return map[string]config.RigEntry{}
	}
	return cfg.Rigs
}

func assertRig(t *testing.T, rigs map[string]config.RigEntry, name, gitURL, localRepo, prefix string) {
	t.Helper()
	entry, ok := rigs[name]
	if !ok {
		t.Fatalf("missing Rig %q, have %v", name, keysOf(rigs))
	}
	if entry.GitURL != gitURL {
		t.Fatalf("rig %s git_url = %q, want %q", name, entry.GitURL, gitURL)
	}
	if filepath.Clean(entry.LocalRepo) != filepath.Clean(localRepo) {
		t.Fatalf("rig %s local_repo = %q, want %q", name, entry.LocalRepo, localRepo)
	}
	if entry.BeadsConfig == nil {
		t.Fatalf("rig %s missing beads config", name)
	}
	gotPrefix := strings.TrimSuffix(entry.BeadsConfig.Prefix, "-")
	if gotPrefix != prefix {
		t.Fatalf("rig %s prefix = %q, want %q", name, entry.BeadsConfig.Prefix, prefix)
	}
}

func assertNoCrewMembers(t *testing.T, townPath string) {
	t.Helper()
	rigs := loadFromRigs(t, townPath)
	for name := range rigs {
		crewDir := filepath.Join(townPath, name, "crew")
		entries, err := os.ReadDir(crewDir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("read crew dir: %v", err)
		}
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				t.Fatalf("gt from must not create Crew members, found %s", filepath.Join(crewDir, e.Name()))
			}
		}
	}
}

func assertNotMachineEnabled(t *testing.T) {
	t.Helper()
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		return
	}
	statePath := filepath.Join(stateHome, "gastown", "state.json")
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("gt from must not enable Gas Town on the machine; wrote %s", statePath)
	}
}

func keysOf(rigs map[string]config.RigEntry) []string {
	keys := make([]string, 0, len(rigs))
	for k := range rigs {
		keys = append(keys, k)
	}
	return keys
}
