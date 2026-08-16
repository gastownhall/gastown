package templates

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonbaldie/gastown/internal/constants"
	"github.com/jonbaldie/gastown/internal/instructions"
	"github.com/jonbaldie/gastown/internal/testutil/gitcmd"
)

func TestNew(t *testing.T) {
	tmpl, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if tmpl == nil {
		t.Fatal("New() returned nil")
	}
}

func TestRenderRole_Mayor(t *testing.T) {
	tmpl, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	data := RoleData{
		Role:          "mayor",
		TownRoot:      "/test/town",
		TownName:      "town",
		WorkDir:       "/test/town",
		DefaultBranch: "main",
		MayorSession:  "gt-town-mayor",
		DeaconSession: "gt-town-deacon",
	}

	output, err := tmpl.RenderRole("mayor", data)
	if err != nil {
		t.Fatalf("RenderRole() error = %v", err)
	}

	// Check for key content
	if !strings.Contains(output, "Mayor Context") {
		t.Error("output missing 'Mayor Context'")
	}
	if !strings.Contains(output, "/test/town") {
		t.Error("output missing town root")
	}
	if !strings.Contains(output, "global coordinator") {
		t.Error("output missing role description")
	}
}

func TestRenderRole_Polecat(t *testing.T) {
	tmpl, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	data := RoleData{
		Role:          "polecat",
		RigName:       "myrig",
		TownRoot:      "/test/town",
		TownName:      "town",
		WorkDir:       "/test/town/myrig/polecats/TestCat",
		DefaultBranch: "main",
		Polecat:       "TestCat",
		MayorSession:  "gt-town-mayor",
		DeaconSession: "gt-town-deacon",
	}

	output, err := tmpl.RenderRole("polecat", data)
	if err != nil {
		t.Fatalf("RenderRole() error = %v", err)
	}

	// Check for key content
	if !strings.Contains(output, "Polecat Context") {
		t.Error("output missing 'Polecat Context'")
	}
	if !strings.Contains(output, "TestCat") {
		t.Error("output missing polecat name")
	}
	if !strings.Contains(output, "myrig") {
		t.Error("output missing rig name")
	}
}

func TestRenderRole_PolecatForkRigUsesPRWorkflow(t *testing.T) {
	tmpl, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	output, err := tmpl.RenderRole("polecat", RoleData{
		Role:          "polecat",
		RigName:       "myrig",
		TownRoot:      "/test/town",
		TownName:      "town",
		WorkDir:       "/test/town/myrig/polecats/TestCat",
		DefaultBranch: "main",
		IsForkRig:     true,
		UpstreamURL:   "https://example.com/upstream/repo.git",
		Polecat:       "TestCat",
		MayorSession:  "gt-town-mayor",
		DeaconSession: "gt-town-deacon",
	})
	if err != nil {
		t.Fatalf("RenderRole() error = %v", err)
	}

	for _, want := range []string{"Fork-backed rig", "GitHub PR/no-merge workflow", "Do NOT submit upstream changes to the local Refinery/MQ"} {
		if !strings.Contains(output, want) {
			t.Fatalf("fork polecat output missing %q:\n%s", want, output)
		}
	}
	for _, forbidden := range []string{"Merge Queue Workflow (gastown, beads repos)", "Refinery merges to main", "Merges your work when complete"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("fork polecat output contains stale MQ guidance %q:\n%s", forbidden, output)
		}
	}
}

func TestRenderRole_CrewForkRigUsesPRWorkflow(t *testing.T) {
	tmpl, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	output, err := tmpl.RenderRole("crew", RoleData{
		Role:          "crew",
		RigName:       "myrig",
		TownRoot:      "/test/town",
		TownName:      "town",
		WorkDir:       "/test/town/myrig/crew/alex",
		DefaultBranch: "main",
		IsForkRig:     true,
		UpstreamURL:   "https://example.com/upstream/repo.git",
		Polecat:       "alex",
		MayorSession:  "gt-town-mayor",
		DeaconSession: "gt-town-deacon",
	})
	if err != nil {
		t.Fatalf("RenderRole() error = %v", err)
	}

	for _, want := range []string{"Fork-backed rig", "Fork-Backed PR Workflow", "git fetch upstream main", "gh pr create --base main"} {
		if !strings.Contains(output, want) {
			t.Fatalf("fork crew output missing %q:\n%s", want, output)
		}
	}
	for _, forbidden := range []string{"Crew workers push directly to main", "git push                    # Direct to main", "Refinery immediately", "origin/main", "commit directly to main"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("fork crew output contains stale direct-main guidance %q:\n%s", forbidden, output)
		}
	}
}

func TestRenderRole_Deacon(t *testing.T) {
	tmpl, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	data := RoleData{
		Role:          "deacon",
		TownRoot:      "/test/town",
		TownName:      "town",
		WorkDir:       "/test/town",
		DefaultBranch: "main",
		MayorSession:  "gt-town-mayor",
		DeaconSession: "gt-town-deacon",
	}

	output, err := tmpl.RenderRole("deacon", data)
	if err != nil {
		t.Fatalf("RenderRole() error = %v", err)
	}

	// Check for key content
	if !strings.Contains(output, "Deacon Context") {
		t.Error("output missing 'Deacon Context'")
	}
	if !strings.Contains(output, "/test/town") {
		t.Error("output missing town root")
	}
	if !strings.Contains(output, "Patrol Executor") {
		t.Error("output missing role description")
	}
	if !strings.Contains(output, "Startup Protocol: Propulsion") {
		t.Error("output missing startup protocol section")
	}
	if !strings.Contains(output, constants.MolDeaconPatrol) {
		t.Error("output missing patrol molecule reference")
	}
}

func TestRenderRole_Refinery_DefaultBranch(t *testing.T) {
	tmpl, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// Test with custom default branch (e.g., "develop")
	data := RoleData{
		Role:          "refinery",
		RigName:       "myrig",
		TownRoot:      "/test/town",
		TownName:      "town",
		WorkDir:       "/test/town/myrig/refinery/rig",
		DefaultBranch: "develop",
		MayorSession:  "gt-town-mayor",
		DeaconSession: "gt-town-deacon",
	}

	output, err := tmpl.RenderRole("refinery", data)
	if err != nil {
		t.Fatalf("RenderRole() error = %v", err)
	}

	// Check that the custom default branch is used in target-resolution guidance.
	// The refinery template intentionally uses placeholders
	// (<rebase-target>/<merge-target>) instead of literal branch commands, so this
	// test verifies the rendered rule text + placeholders.
	fallback := fmt.Sprintf("fallback `%s`", data.DefaultBranch)
	alwaysUse := fmt.Sprintf("always use `%s`", data.DefaultBranch)
	if !strings.Contains(output, "Target Resolution Rule (single source):") {
		t.Error("output missing target resolution rule heading")
	}
	if !strings.Contains(output, fallback) {
		t.Errorf("output missing %q - DefaultBranch not being used in target fallback guidance", fallback)
	}
	if !strings.Contains(output, alwaysUse) {
		t.Errorf("output missing %q - DefaultBranch not being used in integration-disabled guidance", alwaysUse)
	}
	if !strings.Contains(output, "git rebase origin/<rebase-target>") {
		t.Error("output missing placeholder rebase command")
	}
	if !strings.Contains(output, "git checkout <merge-target>") {
		t.Error("output missing placeholder checkout command")
	}
	if !strings.Contains(output, "git push origin <merge-target>") {
		t.Error("output missing placeholder push command")
	}

	// Verify it does NOT contain hardcoded "main" in git commands
	// (main may appear in other contexts like "main branch" descriptions, so we check specific patterns)
	if strings.Contains(output, "git rebase origin/main") {
		t.Error("output still contains hardcoded 'git rebase origin/main' - should use DefaultBranch")
	}
	if strings.Contains(output, "git checkout main") {
		t.Error("output still contains hardcoded 'git checkout main' - should use DefaultBranch")
	}
	if strings.Contains(output, "git push origin main") {
		t.Error("output still contains hardcoded 'git push origin main' - should use DefaultBranch")
	}
}

func TestRenderMessage_Spawn(t *testing.T) {
	tmpl, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	data := SpawnData{
		Issue:       "gt-123",
		Title:       "Test Issue",
		Priority:    1,
		Description: "Test description",
		Branch:      "feature/test",
		RigName:     "myrig",
		Polecat:     "TestCat",
	}

	output, err := tmpl.RenderMessage("spawn", data)
	if err != nil {
		t.Fatalf("RenderMessage() error = %v", err)
	}

	// Check for key content
	if !strings.Contains(output, "gt-123") {
		t.Error("output missing issue ID")
	}
	if !strings.Contains(output, "Test Issue") {
		t.Error("output missing issue title")
	}
}

func TestRenderMessage_Nudge(t *testing.T) {
	tmpl, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	data := NudgeData{
		Polecat:    "TestCat",
		Reason:     "No progress for 30 minutes",
		NudgeCount: 2,
		MaxNudges:  3,
		Issue:      "gt-123",
		Status:     "in_progress",
	}

	output, err := tmpl.RenderMessage("nudge", data)
	if err != nil {
		t.Fatalf("RenderMessage() error = %v", err)
	}

	// Check for key content
	if !strings.Contains(output, "TestCat") {
		t.Error("output missing polecat name")
	}
	if !strings.Contains(output, "2/3") {
		t.Error("output missing nudge count")
	}
}

func TestRenderRole_Dog(t *testing.T) {
	tmpl, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	data := RoleData{
		Role:          "dog",
		DogName:       "Fido",
		TownRoot:      "/test/town",
		TownName:      "town",
		WorkDir:       "/test/town/deacon/dogs/Fido",
		DefaultBranch: "main",
		MayorSession:  "gt-town-mayor",
		DeaconSession: "gt-town-deacon",
	}

	output, err := tmpl.RenderRole("dog", data)
	if err != nil {
		t.Fatalf("RenderRole() error = %v", err)
	}

	// Check for key content
	if !strings.Contains(output, "Dog Context") {
		t.Error("output missing 'Dog Context'")
	}
	if !strings.Contains(output, "Fido") {
		t.Error("output missing dog name")
	}
	if !strings.Contains(output, "/test/town") {
		t.Error("output missing town root")
	}
}

// TestRenderRole_Dog_NoHardcodedGtPath verifies the dog template uses {{ .TownRoot }}
// and does not contain hardcoded ~/gt paths.
func TestRenderRole_Dog_NoHardcodedGtPath(t *testing.T) {
	tmpl, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	const customTownRoot = "/custom/test/instance"

	data := RoleData{
		Role:          "dog",
		DogName:       "Rover",
		TownRoot:      customTownRoot,
		TownName:      "instance",
		WorkDir:       customTownRoot + "/deacon/dogs/Rover",
		DefaultBranch: "main",
		MayorSession:  "gt-instance-mayor",
		DeaconSession: "gt-instance-deacon",
	}

	output, err := tmpl.RenderRole("dog", data)
	if err != nil {
		t.Fatalf("RenderRole() error = %v", err)
	}

	if strings.Contains(output, "~/gt") {
		var offending []string
		for i, line := range strings.Split(output, "\n") {
			if strings.Contains(line, "~/gt") {
				offending = append(offending, fmt.Sprintf("  line %d: %s", i+1, strings.TrimSpace(line)))
			}
		}
		t.Errorf("rendered dog template still contains hardcoded ~/gt (TownRoot=%q):\n%s",
			customTownRoot, strings.Join(offending, "\n"))
	}

	if !strings.Contains(output, customTownRoot) {
		t.Errorf("rendered dog template does not contain TownRoot %q — paths may be hardcoded", customTownRoot)
	}
}

// TestRenderRole_NoHardcodedGtPath verifies that no role template renders
// a literal "~/gt" path — all path references must use {{ .TownRoot }}.
// This is a regression test for instances running outside ~/gt
// (e.g., test instances at a custom path).
func TestRenderRole_NoHardcodedGtPath(t *testing.T) {
	tmpl, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	const customTownRoot2 = "/custom/test/instance"

	roles := []struct {
		role string
		data RoleData
	}{
		{
			role: "polecat",
			data: RoleData{
				Role: "polecat", RigName: "myrig", Polecat: "TestCat",
				TownRoot: customTownRoot2, TownName: "instance",
				WorkDir:       customTownRoot2 + "/myrig/polecats/TestCat",
				DefaultBranch: "main",
				MayorSession:  "gt-instance-mayor", DeaconSession: "gt-instance-deacon",
			},
		},
		{
			role: "mayor",
			data: RoleData{
				Role: "mayor", TownRoot: customTownRoot2, TownName: "instance",
				WorkDir:       customTownRoot2,
				DefaultBranch: "main",
				MayorSession:  "gt-instance-mayor", DeaconSession: "gt-instance-deacon",
			},
		},
		{
			role: "witness",
			data: RoleData{
				Role: "witness", RigName: "myrig",
				TownRoot: customTownRoot2, TownName: "instance",
				WorkDir:       customTownRoot2 + "/myrig/witness",
				DefaultBranch: "main",
				Polecats:      []string{"Cat1", "Cat2"},
				MayorSession:  "gt-instance-mayor", DeaconSession: "gt-instance-deacon",
			},
		},
		{
			role: "crew",
			data: RoleData{
				Role: "crew", RigName: "myrig", Polecat: "TestCrew",
				TownRoot: customTownRoot2, TownName: "instance",
				WorkDir:       customTownRoot2 + "/myrig/crew/TestCrew",
				DefaultBranch: "main",
				MayorSession:  "gt-instance-mayor", DeaconSession: "gt-instance-deacon",
			},
		},
		{
			role: "deacon",
			data: RoleData{
				Role: "deacon", TownRoot: customTownRoot2, TownName: "instance",
				WorkDir:       customTownRoot2,
				DefaultBranch: "main",
				MayorSession:  "gt-instance-mayor", DeaconSession: "gt-instance-deacon",
			},
		},
		// dog tested separately in TestRenderRole_Dog_NoHardcodedGtPath
		// (requires DogName field)
	}

	for _, tc := range roles {
		t.Run(tc.role, func(t *testing.T) {
			output, err := tmpl.RenderRole(tc.role, tc.data)
			if err != nil {
				t.Fatalf("RenderRole(%q) error = %v", tc.role, err)
			}
			if strings.Contains(output, "~/gt") {
				var offending []string
				for i, line := range strings.Split(output, "\n") {
					if strings.Contains(line, "~/gt") {
						offending = append(offending, fmt.Sprintf("  line %d: %s", i+1, strings.TrimSpace(line)))
					}
				}
				t.Errorf("rendered %q template still contains hardcoded ~/gt (TownRoot=%q):\n%s",
					tc.role, customTownRoot2, strings.Join(offending, "\n"))
			}
		})
	}
}

// TestRenderRole_TownRootInOutput verifies that the actual TownRoot value
// appears in the rendered output for roles that reference it in path instructions.
func TestRenderRole_TownRootInOutput(t *testing.T) {
	tmpl, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	const customRoot = "/Users/pa/dev/gastown-tests/my-instance"

	roles := []struct {
		role string
		data RoleData
	}{
		{
			role: "polecat",
			data: RoleData{
				Role: "polecat", RigName: "myrig", Polecat: "Sparky",
				TownRoot: customRoot, TownName: "my-instance",
				WorkDir: customRoot + "/myrig/polecats/Sparky", DefaultBranch: "main",
				MayorSession: "gt-my-instance-mayor", DeaconSession: "gt-my-instance-deacon",
			},
		},
		{
			role: "mayor",
			data: RoleData{
				Role: "mayor", TownRoot: customRoot, TownName: "my-instance",
				WorkDir: customRoot, DefaultBranch: "main",
				MayorSession: "gt-my-instance-mayor", DeaconSession: "gt-my-instance-deacon",
			},
		},
		{
			role: "witness",
			data: RoleData{
				Role: "witness", RigName: "myrig",
				TownRoot: customRoot, TownName: "my-instance",
				WorkDir: customRoot + "/myrig/witness", DefaultBranch: "main",
				MayorSession: "gt-my-instance-mayor", DeaconSession: "gt-my-instance-deacon",
			},
		},
		{
			role: "crew",
			data: RoleData{
				Role: "crew", RigName: "myrig", Polecat: "Sparky",
				TownRoot: customRoot, TownName: "my-instance",
				WorkDir: customRoot + "/myrig/crew/Sparky", DefaultBranch: "main",
				MayorSession: "gt-my-instance-mayor", DeaconSession: "gt-my-instance-deacon",
			},
		},
		{
			role: "deacon",
			data: RoleData{
				Role: "deacon", TownRoot: customRoot, TownName: "my-instance",
				WorkDir: customRoot, DefaultBranch: "main",
				MayorSession: "gt-my-instance-mayor", DeaconSession: "gt-my-instance-deacon",
			},
		},
	}

	for _, tc := range roles {
		t.Run(tc.role, func(t *testing.T) {
			output, err := tmpl.RenderRole(tc.role, tc.data)
			if err != nil {
				t.Fatalf("RenderRole(%q) error = %v", tc.role, err)
			}
			if !strings.Contains(output, customRoot) {
				t.Errorf("rendered %q template does not contain TownRoot %q — paths may be hardcoded", tc.role, customRoot)
			}
		})
	}
}

// TestRenderRole_Polecat_CwdInstruction verifies the critical cwd instruction
// uses the actual town root, not a hardcoded ~/gt path.
// Regression test: agents were following hardcoded ~/gt even in test instances.
func TestRenderRole_Polecat_CwdInstruction(t *testing.T) {
	tmpl, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	const customRoot = "/srv/gastown-ci"

	data := RoleData{
		Role: "polecat", RigName: "rig1", Polecat: "Worker",
		TownRoot: customRoot, TownName: "gastown-ci",
		WorkDir: customRoot + "/rig1/polecats/Worker", DefaultBranch: "main",
		MayorSession: "gt-gastown-ci-mayor", DeaconSession: "gt-gastown-ci-deacon",
	}

	output, err := tmpl.RenderRole("polecat", data)
	if err != nil {
		t.Fatalf("RenderRole() error = %v", err)
	}

	wantCwd := customRoot + "/rig1/polecats/Worker/"
	if !strings.Contains(output, wantCwd) {
		t.Errorf("cwd instruction missing %q\n(agent would use wrong path for non-default instance)", wantCwd)
	}

	wantNeverEdit := customRoot + "/rig1/"
	if !strings.Contains(output, wantNeverEdit) {
		t.Errorf("NEVER edit instruction missing %q", wantNeverEdit)
	}
}

func TestRoleNames(t *testing.T) {
	tmpl, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	names := tmpl.RoleNames()
	expected := []string{"mayor", "witness", "refinery", "polecat", "crew", "deacon", "boot"}

	if len(names) != len(expected) {
		t.Errorf("RoleNames() = %v, want %v", names, expected)
	}

	for i, name := range names {
		if name != expected[i] {
			t.Errorf("RoleNames()[%d] = %q, want %q", i, name, expected[i])
		}
	}
}

func TestRenderRole_BootUsesNudgeNotRawTmux(t *testing.T) {
	tmpl, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	output, err := tmpl.RenderRole("boot", RoleData{
		Role:          "boot",
		TownRoot:      "/test/town",
		TownName:      "town",
		WorkDir:       "/test/town/deacon/dogs/boot",
		DefaultBranch: "main",
		MayorSession:  "gt-town-mayor",
		DeaconSession: "gt-town-deacon",
	})
	if err != nil {
		t.Fatalf("RenderRole() error = %v", err)
	}

	if !strings.Contains(output, `gt nudge --mode=immediate deacon "Boot wake: check your inbox"`) {
		t.Fatalf("boot template missing immediate nudge wake guidance:\n%s", output)
	}
	if !strings.Contains(output, "Boot hooks block it") {
		t.Fatalf("boot template missing raw tmux block rationale:\n%s", output)
	}
	for _, forbidden := range []string{"Escape +", "tmux send-keys -t"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("boot template contains forbidden raw tmux guidance %q:\n%s", forbidden, output)
		}
	}
}

func TestCreatePolecatAgentsMD(t *testing.T) {
	dir := t.TempDir()

	created, err := CreatePolecatAgentsMD(dir, "greenplace", "furiosa")
	if err != nil {
		t.Fatalf("CreatePolecatAgentsMD() error = %v", err)
	}
	if !created {
		t.Fatal("CreatePolecatAgentsMD() created = false, want true")
	}

	content := readCanonicalOverlay(t, dir)

	if strings.Contains(content, "{{rig}}") {
		t.Error("canonical file still contains {{rig}} placeholder")
	}
	if strings.Contains(content, "{{name}}") {
		t.Error("canonical file still contains {{name}} placeholder")
	}
	if !strings.Contains(content, "greenplace") {
		t.Error("canonical file does not contain rig name 'greenplace'")
	}
	if !strings.Contains(content, "furiosa") {
		t.Error("canonical file does not contain polecat name 'furiosa'")
	}
	if !strings.Contains(content, "gt done") {
		t.Fatal("canonical file does not contain 'gt done' — polecats will not know to call it")
	}
	if !strings.Contains(content, "IDLE POLECAT HERESY") {
		t.Error("canonical file missing 'IDLE POLECAT HERESY' warning section")
	}
	if !strings.Contains(content, "MANDATORY FINAL STEP") {
		t.Error("canonical file missing completion protocol with MANDATORY FINAL STEP")
	}
	assertSymlinkTo(t, filepath.Join(dir, "CLAUDE.md"), "AGENTS.md")
}

func TestCreatePolecatAgentsMD_WritesToLocalWhenTrackedExists(t *testing.T) {
	dir := t.TempDir()
	gitcmd.InitRepo(t, dir)

	existing := TownRootAgentsMD()
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(existing), 0644); err != nil {
		t.Fatalf("writing existing CLAUDE.md: %v", err)
	}
	gitcmd.CommitFile(t, dir, "CLAUDE.md", "track constitution")

	created, err := CreatePolecatAgentsMD(dir, "greenplace", "furiosa")
	if err != nil {
		t.Fatalf("CreatePolecatAgentsMD() error = %v", err)
	}
	if !created {
		t.Fatal("CreatePolecatAgentsMD() created = false, want true (should write local pair)")
	}

	data, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("reading CLAUDE.md: %v", err)
	}
	if string(data) != existing {
		t.Error("CLAUDE.md was modified — tracked file must not be touched")
	}
	if strings.Contains(string(data), PolecatLifecycleMarker) {
		t.Error("polecat lifecycle marker written to tracked CLAUDE.md")
	}
	assertRegularFile(t, filepath.Join(dir, "CLAUDE.md"))

	localData, err := os.ReadFile(filepath.Join(dir, "AGENTS.local.md"))
	if err != nil {
		t.Fatalf("reading AGENTS.local.md: %v", err)
	}
	localContent := string(localData)
	if !strings.Contains(localContent, "IDLE POLECAT HERESY") {
		t.Error("polecat lifecycle instructions not written to AGENTS.local.md")
	}
	if !strings.Contains(localContent, "gt done") {
		t.Fatal("gt done instructions not in AGENTS.local.md")
	}
	assertSymlinkTo(t, filepath.Join(dir, "CLAUDE.local.md"), "AGENTS.local.md")
}

func TestCreatePolecatAgentsMD_WritesToLocalWhenAgentsExists(t *testing.T) {
	dir := t.TempDir()
	constitution := "# Project agents\nKeep this file.\n"
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(constitution), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := CreatePolecatAgentsMD(dir, "greenplace", "furiosa"); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != constitution {
		t.Fatal("constitution AGENTS.md was changed")
	}
	assertRegularFile(t, filepath.Join(dir, "AGENTS.md"))
	local, err := os.ReadFile(filepath.Join(dir, "AGENTS.local.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(local), PolecatLifecycleMarker) {
		t.Fatal("lifecycle overlay missing from AGENTS.local.md")
	}
	assertSymlinkTo(t, filepath.Join(dir, "CLAUDE.local.md"), "AGENTS.local.md")
}

func TestCreatePolecatAgentsMD_WritesToLocalWhenBothExist(t *testing.T) {
	dir := t.TempDir()
	claude := "# Claude constitution\n"
	agents := "# Agents constitution\n"
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(claude), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(agents), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := CreatePolecatAgentsMD(dir, "greenplace", "furiosa"); err != nil {
		t.Fatal(err)
	}

	gotClaude, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	gotAgents, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if string(gotClaude) != claude || string(gotAgents) != agents {
		t.Fatal("constitution files were changed")
	}
	assertRegularFile(t, filepath.Join(dir, "CLAUDE.md"))
	assertRegularFile(t, filepath.Join(dir, "AGENTS.md"))
	assertSymlinkTo(t, filepath.Join(dir, "CLAUDE.local.md"), "AGENTS.local.md")
}

func TestCreatePolecatAgentsMD_SkipsWhenAlreadyProvisioned(t *testing.T) {
	dir := t.TempDir()

	created, err := CreatePolecatAgentsMD(dir, "greenplace", "furiosa")
	if err != nil {
		t.Fatalf("first CreatePolecatAgentsMD() error = %v", err)
	}
	if !created {
		t.Fatal("first call should create")
	}

	data1, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))

	created, err = CreatePolecatAgentsMD(dir, "greenplace", "furiosa")
	if err != nil {
		t.Fatalf("second CreatePolecatAgentsMD() error = %v", err)
	}
	if created {
		t.Fatal("second call should skip (lifecycle instructions already present)")
	}

	data2, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if string(data1) != string(data2) {
		t.Fatal("file was modified on second call — should be idempotent")
	}
}

func TestCreatePolecatAgentsMD_ReusePath(t *testing.T) {
	dir := t.TempDir()
	gitcmd.InitRepo(t, dir)
	claudePath := filepath.Join(dir, "CLAUDE.md")

	townRoot := TownRootAgentsMD()
	if err := os.WriteFile(claudePath, []byte(townRoot), 0644); err != nil {
		t.Fatalf("writing tracked CLAUDE.md: %v", err)
	}
	gitcmd.CommitFile(t, dir, "CLAUDE.md", "track constitution")

	created, err := CreatePolecatAgentsMD(dir, "greenplace", "furiosa")
	if err != nil {
		t.Fatalf("first CreatePolecatAgentsMD() error = %v", err)
	}
	if !created {
		t.Fatal("first call should create local pair")
	}

	localData, _ := os.ReadFile(filepath.Join(dir, "AGENTS.local.md"))
	if !strings.Contains(string(localData), PolecatLifecycleMarker) {
		t.Fatal("lifecycle marker not found in AGENTS.local.md after first provision")
	}
	claudeData, _ := os.ReadFile(claudePath)
	if strings.Contains(string(claudeData), PolecatLifecycleMarker) {
		t.Fatal("lifecycle marker written to tracked CLAUDE.md")
	}

	if err := os.WriteFile(claudePath, []byte(townRoot), 0644); err != nil {
		t.Fatalf("simulating git reset --hard: %v", err)
	}

	localData, _ = os.ReadFile(filepath.Join(dir, "AGENTS.local.md"))
	if !strings.Contains(string(localData), PolecatLifecycleMarker) {
		t.Fatal("AGENTS.local.md lifecycle marker lost — should survive git reset --hard")
	}

	created, err = CreatePolecatAgentsMD(dir, "greenplace", "furiosa")
	if err != nil {
		t.Fatalf("second CreatePolecatAgentsMD() error = %v", err)
	}
	if created {
		t.Fatal("second call should be a no-op (lifecycle instructions still in local pair)")
	}

	claudeData, _ = os.ReadFile(claudePath)
	if !strings.Contains(string(claudeData), "Dolt Server") {
		t.Error("town-root content in CLAUDE.md was lost")
	}
	localData, _ = os.ReadFile(filepath.Join(dir, "AGENTS.local.md"))
	if !strings.Contains(string(localData), "gt done") {
		t.Fatal("gt done instructions not found in AGENTS.local.md")
	}
}

func TestCreatePolecatAgentsMD_GitCleanRemovesLocal(t *testing.T) {
	dir := t.TempDir()
	gitcmd.InitRepo(t, dir)
	claudePath := filepath.Join(dir, "CLAUDE.md")

	townRoot := TownRootAgentsMD()
	if err := os.WriteFile(claudePath, []byte(townRoot), 0644); err != nil {
		t.Fatalf("writing tracked CLAUDE.md: %v", err)
	}
	gitcmd.CommitFile(t, dir, "CLAUDE.md", "track constitution")

	if _, err := CreatePolecatAgentsMD(dir, "greenplace", "nux"); err != nil {
		t.Fatalf("first provision: %v", err)
	}

	if err := os.Remove(filepath.Join(dir, "AGENTS.local.md")); err != nil {
		t.Fatalf("simulating git clean -f: %v", err)
	}
	_ = os.Remove(filepath.Join(dir, "CLAUDE.local.md"))

	created, err := CreatePolecatAgentsMD(dir, "greenplace", "nux")
	if err != nil {
		t.Fatalf("second provision: %v", err)
	}
	if !created {
		t.Fatal("should recreate local pair after git clean removed it")
	}

	localData, _ := os.ReadFile(filepath.Join(dir, "AGENTS.local.md"))
	if !strings.Contains(string(localData), PolecatLifecycleMarker) {
		t.Fatal("lifecycle marker not in recreated AGENTS.local.md")
	}
	claudeData, _ := os.ReadFile(claudePath)
	if string(claudeData) != townRoot {
		t.Error("tracked CLAUDE.md was modified")
	}
}

func TestCreatePolecatAgentsMD_GitCleanScenario(t *testing.T) {
	dir := t.TempDir()

	created, err := CreatePolecatAgentsMD(dir, "greenplace", "nux")
	if err != nil {
		t.Fatalf("first CreatePolecatAgentsMD() error = %v", err)
	}
	if !created {
		t.Fatal("first call should create file")
	}

	if err := os.Remove(filepath.Join(dir, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(dir, "CLAUDE.md"))
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatal("git clean simulation should have removed AGENTS.md")
	}

	created, err = CreatePolecatAgentsMD(dir, "greenplace", "nux")
	if err != nil {
		t.Fatalf("second CreatePolecatAgentsMD() error = %v", err)
	}
	if !created {
		t.Fatal("second call should re-create file after git clean")
	}

	data, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if !strings.Contains(string(data), "gt done") {
		t.Fatal("gt done instructions not found after re-creation")
	}
	assertSymlinkTo(t, filepath.Join(dir, "CLAUDE.md"), "AGENTS.md")
}

func TestCreatePolecatAgentsMD_GeminiAliasPointsAtCanonical(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink("CLAUDE.md", filepath.Join(dir, "GEMINI.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := CreatePolecatAgentsMD(dir, "greenplace", "nux"); err != nil {
		t.Fatal(err)
	}
	assertSymlinkTo(t, filepath.Join(dir, "GEMINI.md"), "AGENTS.md")
}

func TestCreatePolecatAgentsMD_LeavesRegularGeminiUnchanged(t *testing.T) {
	dir := t.TempDir()
	gemini := "# Gemini project file\n"
	if err := os.WriteFile(filepath.Join(dir, "GEMINI.md"), []byte(gemini), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := CreatePolecatAgentsMD(dir, "greenplace", "nux"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "GEMINI.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != gemini {
		t.Fatalf("regular GEMINI.md changed: %q", got)
	}
	assertRegularFile(t, filepath.Join(dir, "GEMINI.md"))
}

func readCanonicalOverlay(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, instructions.CanonicalFile)
	assertRegularFile(t, path)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

func assertSymlinkTo(t *testing.T, path, want string) {
	t.Helper()
	target, err := os.Readlink(path)
	if err != nil {
		t.Fatalf("readlink %s: %v", path, err)
	}
	if target != want {
		t.Fatalf("%s symlink target = %q, want %q", path, target, want)
	}
}

func assertRegularFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("%s is a symlink, want a regular file", path)
	}
}
