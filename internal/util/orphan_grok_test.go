package util

import "testing"

func TestIsAgentOrphanCommNameIncludesGrok(t *testing.T) {
	t.Parallel()
	if !isAgentOrphanCommName("grok") {
		t.Error("isAgentOrphanCommName(\"grok\") = false, want true so gt down / zombie cleanup tracks Grok CLI")
	}
}
