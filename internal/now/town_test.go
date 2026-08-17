package now

import "testing"

func TestSanitizeRigNameHyphensBecomeUnderscores(t *testing.T) {
	if got := SanitizeRigName("my-proj"); got != "my_proj" {
		t.Fatalf("SanitizeRigName(my-proj) = %q, want my_proj", got)
	}
}
