package archive

import (
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
