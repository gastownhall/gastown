package polecat

import "testing"

func TestDecideSlotReuse(t *testing.T) {
	base := SlotReuseInput{State: StateIdle, CleanupStatus: CleanupClean}
	tests := []struct {
		name   string
		mutate func(*SlotReuseInput)
		want   string
	}{
		{name: "clean idle", want: "reusable"},
		{name: "working", mutate: func(in *SlotReuseInput) { in.State = StateWorking }, want: "not-idle"},
		{name: "hook", mutate: func(in *SlotReuseInput) { in.HookBead = "gt-work" }, want: "hook-still-set"},
		{name: "push failed", mutate: func(in *SlotReuseInput) { in.PushFailed = true }, want: "push-failed"},
		{name: "mr failed", mutate: func(in *SlotReuseInput) { in.MRFailed = true }, want: "mr-failed"},
		{name: "cleanup dirty", mutate: func(in *SlotReuseInput) { in.CleanupStatus = CleanupUnpushed }, want: "cleanup-has_unpushed"},
		{name: "stale cleanup safe", mutate: func(in *SlotReuseInput) { in.CleanupStatus = CleanupUnpushed; in.IgnoreCleanupStatus = true }, want: "reusable"},
		{name: "cleanup unknown", mutate: func(in *SlotReuseInput) { in.CleanupStatus = CleanupUnknown }, want: "cleanup-unknown"},
		{name: "git dirty", mutate: func(in *SlotReuseInput) { in.GitDirty = true }, want: "git-dirty"},
		{name: "stash", mutate: func(in *SlotReuseInput) { in.StashCount = 1 }, want: "git-stash"},
		{name: "unpushed", mutate: func(in *SlotReuseInput) { in.UnpushedCommits = 1 }, want: "git-unpushed"},
		{name: "git failed", mutate: func(in *SlotReuseInput) { in.GitCheckFailed = true }, want: "git-check-failed"},
		{name: "active MR pending", mutate: func(in *SlotReuseInput) { in.ActiveMR = "gt-mr"; in.ActiveMRBlocker = "active_mr=gt-mr status=open" }, want: "active-mr-open"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := base
			if tt.mutate != nil {
				tt.mutate(&in)
			}
			got := DecideSlotReuse(in)
			if got.Reason != tt.want {
				t.Fatalf("Reason = %q, want %q", got.Reason, tt.want)
			}
			if got.Reusable != (tt.want == "reusable") {
				t.Fatalf("Reusable = %v for reason %q", got.Reusable, got.Reason)
			}
		})
	}
}
