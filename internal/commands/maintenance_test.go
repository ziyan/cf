package commands

import (
	"strings"
	"testing"

	"github.com/ziyan/cf/internal/archive"
)

func testState(pages map[string]*archive.PageState) *archive.State {
	return &archive.State{Pages: pages}
}

func TestSpaceIndexNestsByParent(t *testing.T) {
	state := testState(map[string]*archive.PageState{
		"1": {SpaceKey: "ENG", Title: "Home", Path: "pages/ENG/1-Home.md"},
		"2": {SpaceKey: "ENG", Title: "Child", ParentID: "1", Path: "pages/ENG/2-Child.md"},
		"3": {SpaceKey: "ENG", Title: "Grandchild", ParentID: "2", Path: "pages/ENG/3-Grandchild.md"},
	})
	document := spaceIndex("ENG", []string{"1", "2", "3"}, state, nil)
	for _, expected := range []string{
		"- [Home](1-Home.md)",
		"  - [Child](2-Child.md)",
		"    - [Grandchild](3-Grandchild.md)",
	} {
		if !strings.Contains(document, expected) {
			t.Errorf("expected %q in:\n%s", expected, document)
		}
	}
}

// A page whose parent is in another space, or was never archived, still has to
// appear. Dropping it would hide it from the only index there is.
func TestSpaceIndexKeepsOrphans(t *testing.T) {
	state := testState(map[string]*archive.PageState{
		"1": {SpaceKey: "ENG", Title: "Orphan", ParentID: "999", Path: "pages/ENG/1-Orphan.md"},
	})
	document := spaceIndex("ENG", []string{"1"}, state, nil)
	if !strings.Contains(document, "- [Orphan](1-Orphan.md)") {
		t.Errorf("a page whose parent is missing must still be listed:\n%s", document)
	}
}

// Parent links come from the server and nothing guarantees they are a tree.
// A cycle must not hang the walk or repeat a page for ever.
func TestSpaceIndexSurvivesACycle(t *testing.T) {
	state := testState(map[string]*archive.PageState{
		"1": {SpaceKey: "ENG", Title: "One", ParentID: "2", Path: "pages/ENG/1-One.md"},
		"2": {SpaceKey: "ENG", Title: "Two", ParentID: "1", Path: "pages/ENG/2-Two.md"},
	})
	document := spaceIndex("ENG", []string{"1", "2"}, state, nil)
	if count := strings.Count(document, "- ["); count > 2 {
		t.Errorf("a cycle repeated pages %d times:\n%s", count, document)
	}
}

func TestSpaceIndexShowsLabels(t *testing.T) {
	state := testState(map[string]*archive.PageState{
		"1": {SpaceKey: "ENG", Title: "Tagged", Path: "pages/ENG/1-Tagged.md"},
	})
	document := spaceIndex("ENG", []string{"1"}, state, map[string][]string{"1": {"safety", "sat"}})
	if !strings.Contains(document, "`safety` `sat`") {
		t.Errorf("expected the labels beside the page:\n%s", document)
	}
}
