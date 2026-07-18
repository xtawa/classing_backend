package buildinfo

import "testing"

func TestInjectedRevision(t *testing.T) {
	previous := Commit
	Commit = "56ef80e51be8c1fe4fdf839ce5c270062e650d75"
	t.Cleanup(func() { Commit = previous })

	if got := Revision(); got != Commit {
		t.Fatalf("Revision() = %q, want %q", got, Commit)
	}
	if got := ShortRevision(); got != "56ef80e" {
		t.Fatalf("ShortRevision() = %q", got)
	}
}
