package workflow

import (
	"os"
	"path/filepath"
	"testing"
)

const sampleDraft = "---\ntitle: Spec\nstatus: draft\nbump: minor\n---\n# Body\n\nSome text.\n"

func TestArtifactHash_StatusLineIgnored(t *testing.T) {
	approved := "---\ntitle: Spec\nstatus: approved\nbump: minor\n---\n# Body\n\nSome text.\n"
	if a, b := ArtifactHash([]byte(sampleDraft)), ArtifactHash([]byte(approved)); a != b {
		t.Fatalf("hashes differ: %s vs %s", a, b)
	}
}

func TestArtifactHash_BodyEditChanges(t *testing.T) {
	edited := "---\ntitle: Spec\nstatus: draft\nbump: minor\n---\n# Body\n\nSome text!\n"
	if ArtifactHash([]byte(sampleDraft)) == ArtifactHash([]byte(edited)) {
		t.Fatal("body edit did not change hash")
	}
}

func TestArtifactHash_CRLFEqualsLF(t *testing.T) {
	crlf := "---\r\ntitle: Spec\r\nstatus: draft\r\nbump: minor\r\n---\r\n# Body\r\n\r\nSome text.\r\n"
	if ArtifactHash([]byte(sampleDraft)) != ArtifactHash([]byte(crlf)) {
		t.Fatal("CRLF and LF hashes differ")
	}
}

func TestArtifactHash_TrailingSpaceChanges(t *testing.T) {
	edited := "---\ntitle: Spec\nstatus: draft\nbump: minor\n---\n# Body\n\nSome text. \n"
	if ArtifactHash([]byte(sampleDraft)) == ArtifactHash([]byte(edited)) {
		t.Fatal("trailing-space edit did not change hash")
	}
}

func TestArtifactHash_BodyStatusLineCounts(t *testing.T) {
	a := "---\ntitle: Spec\n---\nstatus: draft\n"
	b := "---\ntitle: Spec\n---\nstatus: approved\n"
	if ArtifactHash([]byte(a)) == ArtifactHash([]byte(b)) {
		t.Fatal("status line in body was ignored")
	}
	noFM := "status: draft\nbody\n"
	noFM2 := "status: approved\nbody\n"
	if ArtifactHash([]byte(noFM)) == ArtifactHash([]byte(noFM2)) {
		t.Fatal("status line without frontmatter was ignored")
	}
}

func TestArtifactHash_Format(t *testing.T) {
	h := ArtifactHash([]byte(sampleDraft))
	if len(h) != 64 {
		t.Fatalf("hash length = %d, want 64", len(h))
	}
	for _, c := range h {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			t.Fatalf("hash %q is not lowercase hex", h)
		}
	}
}

func TestReadArtifact_Frontmatter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spec.md")
	if err := os.WriteFile(path, []byte(sampleDraft), 0o644); err != nil {
		t.Fatal(err)
	}
	content, sha, fm, err := ReadArtifact(path)
	if err != nil {
		t.Fatalf("ReadArtifact: %v", err)
	}
	if string(content) != sampleDraft {
		t.Errorf("content mismatch")
	}
	if sha != ArtifactHash([]byte(sampleDraft)) {
		t.Errorf("sha mismatch")
	}
	if fm["bump"] != "minor" {
		t.Errorf("fm[bump] = %q, want minor", fm["bump"])
	}
	if fm["status"] != "draft" {
		t.Errorf("fm[status] = %q, want draft", fm["status"])
	}
}

func TestReadArtifact_NoFrontmatter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plain.md")
	if err := os.WriteFile(path, []byte("# Just body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, fm, err := ReadArtifact(path)
	if err != nil {
		t.Fatalf("ReadArtifact: %v", err)
	}
	if fm == nil || len(fm) != 0 {
		t.Fatalf("fm = %#v, want empty non-nil map", fm)
	}
}

func TestReadArtifact_Missing(t *testing.T) {
	if _, _, _, err := ReadArtifact(filepath.Join(t.TempDir(), "nope.md")); err == nil {
		t.Fatal("expected error for missing file")
	}
}
