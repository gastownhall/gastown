package cmd

import "testing"

func TestRoot_WastelandHiddenByDefault(t *testing.T) {
	// Registered first so it runs last (cleanups are LIFO), after t.Setenv
	// has restored the original env: re-derive visibility from the real env.
	t.Cleanup(applyUpstreamFeatureVisibility)

	t.Setenv("GTB_UPSTREAM_FEATURES", "")
	applyUpstreamFeatureVisibility()
	if upstreamFeaturesEnabled() {
		t.Errorf("upstream features should be off when GTB_UPSTREAM_FEATURES is unset")
	}
	if !wlCmd.Hidden {
		t.Errorf("wl should be hidden when GTB_UPSTREAM_FEATURES is unset")
	}
	if !proxySubcmdsCmd.Hidden {
		t.Errorf("proxy-subcmds should be hidden when GTB_UPSTREAM_FEATURES is unset")
	}

	t.Setenv("GTB_UPSTREAM_FEATURES", "1")
	applyUpstreamFeatureVisibility()
	if !upstreamFeaturesEnabled() {
		t.Errorf("GTB_UPSTREAM_FEATURES=1 should enable upstream features")
	}
	if wlCmd.Hidden {
		t.Errorf("wl should be visible when GTB_UPSTREAM_FEATURES=1")
	}
	// proxy-subcmds is internal plumbing for gt-proxy-server and was hidden
	// upstream; it stays hidden even with upstream features on.
	if !proxySubcmdsCmd.Hidden {
		t.Errorf("proxy-subcmds should stay hidden (internal plumbing)")
	}

	for _, v := range []string{"true", "yes", "0", " 1"} {
		t.Setenv("GTB_UPSTREAM_FEATURES", v)
		if upstreamFeaturesEnabled() {
			t.Errorf("GTB_UPSTREAM_FEATURES=%q should not enable upstream features; only \"1\" does", v)
		}
	}
}
