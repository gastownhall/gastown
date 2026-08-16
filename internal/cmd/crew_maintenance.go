package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/jonbaldie/gastown/internal/crew"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/tmux"
	"github.com/spf13/cobra"
)

func runCrewRename(cmd *cobra.Command, args []string) error {
	oldName := args[0]
	newName := args[1]
	// Parse rig/name format for oldName (e.g., "beads/emma" -> rig=beads, name=emma)
	if rig, crewName, ok := parseRigSlashName(oldName); ok {
		if crewRig == "" {
			crewRig = rig
		}
		oldName = crewName
	}
	// Note: newName is just the new name, no rig prefix expected

	crewMgr, r, err := getCrewManagerForMember(crewRig, oldName)
	if err != nil {
		return err
	}

	// Kill any running session for the old name.
	// Use KillSessionWithProcesses to ensure all descendant processes are killed.
	t := tmux.NewTmux()
	oldSessionID := crewSessionName(r.Name, oldName)
	if hasSession, _ := t.HasSession(oldSessionID); hasSession {
		if err := t.KillSessionWithProcesses(oldSessionID); err != nil {
			return fmt.Errorf("killing old session: %w", err)
		}
		fmt.Printf("Killed session %s\n", oldSessionID)
	}

	// Perform the rename
	if err := crewMgr.Rename(oldName, newName); err != nil {
		if errors.Is(err, crew.ErrInvalidCrewName) {
			return fmt.Errorf("invalid new name '%s': %w", newName, err)
		}
		if err == crew.ErrCrewNotFound {
			return fmt.Errorf("crew workspace '%s' not found", oldName)
		}
		if err == crew.ErrCrewExists {
			return fmt.Errorf("crew workspace '%s' already exists", newName)
		}
		return fmt.Errorf("renaming crew workspace: %w", err)
	}

	fmt.Printf("%s Renamed crew workspace: %s/%s → %s/%s\n",
		style.Bold.Render("✓"), r.Name, oldName, r.Name, newName)
	fmt.Printf("New session will be: %s\n", style.Dim.Render(crewSessionName(r.Name, newName)))

	return nil
}

func runCrewPristine(cmd *cobra.Command, args []string) error {
	crewMgr, r, err := getCrewManager(crewRig)
	if err != nil {
		return err
	}

	var workers []*crew.CrewWorker

	if len(args) > 0 {
		// Specific worker
		name := args[0]
		// Parse rig/name format (e.g., "beads/emma" -> rig=beads, name=emma)
		if _, crewName, ok := parseRigSlashName(name); ok {
			name = crewName
		}
		worker, err := crewMgr.Get(name)
		if err != nil {
			if err == crew.ErrCrewNotFound {
				return fmt.Errorf("crew workspace '%s' not found", name)
			}
			return fmt.Errorf("getting crew worker: %w", err)
		}
		workers = []*crew.CrewWorker{worker}
	} else {
		// All workers
		workers, err = crewMgr.List()
		if err != nil {
			return fmt.Errorf("listing crew workers: %w", err)
		}
	}

	if len(workers) == 0 {
		fmt.Println("No crew workspaces found.")
		return nil
	}

	var results []*crew.PristineResult

	for _, w := range workers {
		result, err := crewMgr.Pristine(w.Name)
		if err != nil {
			return fmt.Errorf("pristine %s: %w", w.Name, err)
		}
		results = append(results, result)
	}

	if crewJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(results)
	}

	// Text output
	for _, result := range results {
		fmt.Printf("%s %s/%s\n", style.Bold.Render("→"), r.Name, result.Name)

		if result.HadChanges {
			fmt.Printf("  %s\n", style.Bold.Render("⚠ Has uncommitted changes"))
		}

		if result.Pulled {
			fmt.Printf("  %s git pull\n", style.Dim.Render("✓"))
		} else if result.PullError != "" {
			fmt.Printf("  %s git pull: %s\n", style.Bold.Render("✗"), result.PullError)
		}
	}

	return nil
}
