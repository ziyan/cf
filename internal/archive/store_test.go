package archive

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSafeName(t *testing.T) {
	cases := map[string]string{
		"Getting started": "Getting-started",
		"ISMS_Policies":   "ISMS_Policies",
		"a/b\\c":          "a-b-c",
		"trailing   ":     "trailing",
		// A title in another script is kept. Stripping it would file a page titled in
		// another script as an id and an underscore.
		"日本語":           "日本語",
		"日油 岡崎新工場":      "日油-岡崎新工場",
		"【検討】手順書":       "【検討】手順書",
		"Release 1.2.3": "Release-1.2.3",
	}
	for input, expected := range cases {
		if actual := SafeName(input); actual != expected {
			t.Errorf("SafeName(%q) = %q, expected %q", input, actual, expected)
		}
	}
	// A name of nothing but dots names a directory, and must not survive.
	for _, dots := range []string{".", "..", "..."} {
		if name := SafeName(dots); strings.Trim(name, ".") == "" {
			t.Errorf("SafeName(%q) gave %q, which still names a directory", dots, name)
		}
	}
	if length := len(SafeName(strings.Repeat("a", 500))); length > MaximumNameLength {
		t.Errorf("a long title came back %d bytes, past the cap of %d", length, MaximumNameLength)
	}
	// The cap counts bytes, which is what a file system counts, but it must
	// not cut a character in half and leave invalid UTF-8 in a file name.
	long := SafeName(strings.Repeat("日", 200))
	if len(long) > MaximumNameLength {
		t.Errorf("a long non-ASCII title came back %d bytes", len(long))
	}
	if !utf8.ValidString(long) {
		t.Errorf("the cap cut a character in half: %q", long)
	}
}

// The id leads the file name, so a page keeps its file when it is retitled and
// two pages with the same title never land on each other.
func TestPageBaseName(t *testing.T) {
	if name := PageBaseName("12345", "Getting started"); name != "12345-Getting-started" {
		t.Errorf("got %q", name)
	}
	if name := PageBaseName("12345", ""); name != "12345" {
		t.Errorf("a page with no title should still be named by its id, got %q", name)
	}
	first := PageBaseName("1", "Same Title")
	second := PageBaseName("2", "Same Title")
	if first == second {
		t.Errorf("two pages sharing a title must not share a file name: %q", first)
	}
}

func TestOpenRequiresAnArchive(t *testing.T) {
	if _, err := Open(t.TempDir()); err == nil {
		t.Fatal("an empty directory must not open as an archive")
	}
	store, err := Create(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveState(&State{Pages: map[string]*PageState{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(store.Directory()); err != nil {
		t.Fatalf("a synced directory must open: %v", err)
	}
}

func TestStateRoundTrip(t *testing.T) {
	store, err := Create(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	empty, err := store.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Pages) != 0 || empty.PagesMark != "" {
		t.Errorf("a fresh archive should hold nothing, got %+v", empty)
	}

	empty.PagesMark = "2026-09-17T00:00:00.000Z"
	empty.SpaceMarks["144"] = "2023-01-01T00:00:00.000Z"
	empty.Pages["1"] = &PageState{SpaceKey: "ENG", Title: "One", Version: 3, Path: "pages/ENG/1-One.md"}
	if err := store.SaveState(empty); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PagesMark != empty.PagesMark || loaded.Pages["1"].Version != 3 {
		t.Errorf("state did not survive the round trip: %+v", loaded)
	}
	if loaded.SpaceMarks["144"] != "2023-01-01T00:00:00.000Z" {
		t.Errorf("per-space marks did not survive: %+v", loaded.SpaceMarks)
	}
}

func TestWritePageKeepsBothForms(t *testing.T) {
	store, err := Create(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path, err := store.WritePage("ENG", "1-Title", "# Title\n", "<h1>Title</h1>")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, "pages/") {
		t.Errorf("the recorded path should be relative to the archive, got %q", path)
	}
	for _, suffix := range []string{".md", ".xhtml"} {
		full := filepath.Join(store.PagesDirectory(), "ENG", "1-Title"+suffix)
		if _, err := os.Stat(full); err != nil {
			t.Errorf("expected %s to be written: %v", suffix, err)
		}
	}
	// Nothing half written is left behind.
	entries, _ := os.ReadDir(filepath.Join(store.PagesDirectory(), "ENG"))
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("a temporary file was left behind: %s", entry.Name())
		}
	}
}

func TestAppendAttachmentsRecordsWithoutFetching(t *testing.T) {
	store, err := Create(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendAttachments([]*Attachment{
		{ID: "a1", PageID: "1", SpaceKey: "ENG", Title: "diagram.png", FileSize: 1024},
		{ID: "a2", PageID: "1", SpaceKey: "ENG", Title: "notes.pdf", FileSize: 2048},
	}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(store.AttachmentsPath())
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected one line per attachment, got %d", len(lines))
	}
	// The index exists so a later run can fetch these; the files themselves
	// are not downloaded.
	if _, err := os.Stat(store.AttachmentsDirectory()); !os.IsNotExist(err) {
		t.Errorf("recording attachments must not create the download directory")
	}
}

// A site can hold attachments by the million, and a million files in one
// directory is slow to list and hard on some file systems.
func TestAttachmentPathsAreSpreadOverShards(t *testing.T) {
	store, err := Create(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	shards := map[string]int{}
	for index := 0; index < 2000; index++ {
		path := store.AttachmentPath(fmt.Sprintf("att%d", index), "file.pdf")
		shards[filepath.Base(filepath.Dir(path))]++
	}
	// Confluence ids share a prefix, so the shard has to come from a hash of
	// the id rather than from the id itself, or they all land together.
	if len(shards) < 200 {
		t.Errorf("2000 attachments landed in only %d shards", len(shards))
	}
	largest := 0
	for _, count := range shards {
		if count > largest {
			largest = count
		}
	}
	if largest > 40 {
		t.Errorf("one shard took %d of 2000, which is not a spread", largest)
	}
	// The same attachment always belongs in the same place, or a second run
	// would fetch everything again.
	first := store.AttachmentPath("att123", "file.pdf")
	if second := store.AttachmentPath("att123", "file.pdf"); first != second {
		t.Errorf("the path moved between calls: %q then %q", first, second)
	}
}

func TestHaveAttachmentsReadsEveryShard(t *testing.T) {
	store, err := Create(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"att1", "att2", "att3"} {
		if err := store.WriteAttachment(id, "file.pdf", []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	have, err := store.HaveAttachments()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"att1", "att2", "att3"} {
		if _, isHere := have[id]; !isHere {
			t.Errorf("%s was written but not found again", id)
		}
	}
	if len(have) != 3 {
		t.Errorf("expected 3 attachments, found %d", len(have))
	}
}
