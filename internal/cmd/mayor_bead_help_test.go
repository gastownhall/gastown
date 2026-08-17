package cmd

import (
	"strings"
	"testing"
)

// UAT Low #14: Mayor created town hq-* work beads and tried to sling them
// to rigs. Prime and sling help must show the create-in-rig recipe.
func TestSlingHelpShowsRigBeadCreatePath(t *testing.T) {
	assertRigBeadCreateRecipe(t, slingCmd.Long)
}

func TestMayorPrimeFallbackShowsRigBeadCreatePath(t *testing.T) {
	out := captureStdout(t, func() {
		outputMayorContext(RoleContext{
			Role:     RoleMayor,
			TownRoot: "/test/town",
		})
	})
	assertRigBeadCreateRecipe(t, out)
}

func assertRigBeadCreateRecipe(t *testing.T, text string) {
	t.Helper()
	for _, needle := range []string{
		"bd -C",
		"create --title=",
		"convoy create",
		"convoy add",
		"--merge=local",
		"hq-cv-",
		"hq-mayor",
	} {
		if !strings.Contains(text, needle) {
			t.Errorf("missing %q", needle)
		}
	}
}
