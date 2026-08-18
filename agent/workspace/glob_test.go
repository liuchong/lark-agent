package workspace

import "testing"

func TestMatchGlobSupportsDoubleStarAndBasename(t *testing.T) {
	if !MatchGlob("**/*.go", "service/router.go") {
		t.Fatal("expected nested go file to match")
	}
	if MatchGlob("**/*.go", "service/readme.md") {
		t.Fatal("markdown matched go glob")
	}
	if !MatchGlob("router.go", "service/router.go") {
		t.Fatal("expected basename match")
	}
}
