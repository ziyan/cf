// Package archive reads and writes a local archive of Confluence pages. The
// layout is:
//
//	spaces.json                          every space seen
//	state.json                           what is archived and at which version
//	pages/<space>/<id>-<slug>.md         the page as markdown, with front matter
//	pages/<space>/<id>-<slug>.xhtml      the storage format the server sent
//	comments/<space>/<id>-<slug>.md      the comments on that page
//	attachments.jsonl                    one record per attachment, not fetched
//	attachments/<id>__<name>             only what somebody asked for
//
// Markdown is for reading and grepping. The storage beside it is what the
// server actually holds: macros, layouts and embedded content do not survive
// the conversion, so the archive keeps both rather than choosing.
package archive

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	spacesFileName      = "spaces.json"
	stateFileName       = "state.json"
	usersFileName       = "users.json"
	labelsFileName      = "labels.json"
	attachmentsFileName = "attachments.jsonl"
	pagesDirName        = "pages"
	blogpostsDirName    = "blogposts"
	commentsDirName     = "comments"
	attachmentsDirName  = "attachments"

	// MaximumNameLength is the cap SafeName puts on one path element. A long
	// title cannot push a path past what a file system takes.
	MaximumNameLength = 80
)

// unsafeNameCharacters is what cannot appear in a path element, rather than
// what is allowed in one. An allow list of ASCII would erase a title written
// in any other script: a site whose pages are titled in another
// script would file most of them as an id and an underscore.
//
// These are the characters Windows forbids, plus the separator and control
// characters. Everything else, Japanese included, is kept.
var unsafeNameCharacters = regexp.MustCompile(`[/\\:*?"<>|\x00-\x1f\x7f]+`)

// PageState is what the archive holds for one page.
type PageState struct {
	SpaceKey string `json:"space"`
	Title    string `json:"title"`
	// ParentID is what makes a space a tree rather than a list of files.
	ParentID   string `json:"parent,omitempty"`
	Version    int    `json:"version"`
	ModifiedAt string `json:"modified"`
	Path       string `json:"path"`
	// CommentVersion is the newest comment revision written for this page, so
	// a later sync can tell whether its comments file is current.
	CommentCount int `json:"comments,omitempty"`
}

// State is everything the archive knows about what it has.
type State struct {
	// BlogpostsMark is the newest blog post modification archived.
	BlogpostsMark string                `json:"blogposts_mark,omitempty"`
	Blogposts     map[string]*PageState `json:"blogposts,omitempty"`

	// PagesMark and CommentsMark are the newest modification a whole-site walk
	// has archived. The next whole-site sync reads back to them and no further.
	//
	// A sync narrowed to a space must not touch them. It has seen only part of
	// the site, and a mark saying otherwise would make the next whole-site
	// sync stop early and never archive the pages it skipped.
	PagesMark    string `json:"pages_mark"`
	CommentsMark string `json:"comments_mark"`

	// SpaceMarks is the same thing per space, for a narrowed sync to pick up
	// where the last narrowed sync of that space left off.
	SpaceMarks map[string]string `json:"space_marks,omitempty"`

	Pages map[string]*PageState `json:"pages"`
}

// Attachment is one line of attachments.jsonl: enough to fetch the file later
// without walking the site again.
type Attachment struct {
	ID          string `json:"id"`
	PageID      string `json:"page_id"`
	SpaceKey    string `json:"space"`
	Title       string `json:"title"`
	MediaType   string `json:"media_type"`
	FileSize    int64  `json:"size"`
	DownloadURL string `json:"download"`
}

// Store is one archive directory. Several goroutines may call its methods at
// once. Each page writes its own files; the lock guards what they share.
type Store struct {
	directory string
	lock      sync.Mutex
}

// Open prepares an existing archive directory. A command that only reads must
// not create one: a mistyped path would look like an empty archive rather
// than an error.
func Open(directory string) (*Store, error) {
	store, err := newStore(directory)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(store.directory, stateFileName)); err != nil {
		return nil, fmt.Errorf("archive: %s is not an archive directory, it has no %s: %w",
			store.directory, stateFileName, err)
	}
	return store, nil
}

// Create prepares an archive directory, making it if it is not there yet.
func Create(directory string) (*Store, error) {
	store, err := newStore(directory)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(store.directory, 0o755); err != nil {
		return nil, fmt.Errorf("archive: creating %s: %w", store.directory, err)
	}
	return store, nil
}

func newStore(directory string) (*Store, error) {
	if directory == "" {
		return nil, fmt.Errorf("archive: no archive directory given")
	}
	expanded, err := ExpandPath(directory)
	if err != nil {
		return nil, err
	}
	return &Store{directory: expanded}, nil
}

// ExpandPath resolves a leading ~, so a caller may give a path as a shell
// takes it.
func ExpandPath(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("archive: resolving the home directory: %w", err)
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

// Directory is the root of the archive.
func (self *Store) Directory() string { return self.directory }

// BlogpostsDirectory holds one file per blog post, under a directory per space.
func (self *Store) BlogpostsDirectory() string {
	return filepath.Join(self.directory, blogpostsDirName)
}

// LoadLabels reads which labels a page carries.
func (self *Store) LoadLabels() (map[string][]string, error) {
	labels := map[string][]string{}
	content, err := os.ReadFile(filepath.Join(self.directory, labelsFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return labels, nil
		}
		return nil, fmt.Errorf("archive: reading %s: %w", labelsFileName, err)
	}
	if err := json.Unmarshal(content, &labels); err != nil {
		return nil, fmt.Errorf("archive: parsing %s: %w", labelsFileName, err)
	}
	return labels, nil
}

// SaveLabels records which labels a page carries.
func (self *Store) SaveLabels(labels map[string][]string) error {
	return self.writeJson(labelsFileName, labels)
}

// WriteBlogpost writes one blog post's markdown and the storage it came from.
func (self *Store) WriteBlogpost(spaceKey, baseName, markdown, storage string) (string, error) {
	return self.writeDocument(self.BlogpostsDirectory(), spaceKey, baseName, markdown, storage)
}

// PagesDirectory holds one file per page, under a directory per space.
func (self *Store) PagesDirectory() string { return filepath.Join(self.directory, pagesDirName) }

// CommentsDirectory holds one file per page that has comments.
func (self *Store) CommentsDirectory() string { return filepath.Join(self.directory, commentsDirName) }

// AttachmentsDirectory holds the attachment files somebody asked for.
func (self *Store) AttachmentsDirectory() string {
	return filepath.Join(self.directory, attachmentsDirName)
}

// SafeName turns a title into something safe to use as a path element. A name
// of nothing but dots names the current or parent directory, so it is replaced
// rather than handed back.
func SafeName(name string) string {
	safe := unsafeNameCharacters.ReplaceAllString(name, "-")
	// Whitespace becomes a dash, so a name needs no quoting on a command line.
	safe = strings.Join(strings.Fields(safe), "-")
	safe = strings.Trim(safe, "-. ")
	if safe == "" || strings.Trim(safe, ".") == "" {
		safe = strings.Repeat("_", len(safe)+1)
	}
	// The cap is in bytes, since that is what a file system counts, but it
	// must not cut a character in half.
	if len(safe) > MaximumNameLength {
		cut := MaximumNameLength
		for cut > 0 && !utf8.RuneStart(safe[cut]) {
			cut--
		}
		safe = strings.Trim(safe[:cut], "-. ")
	}
	if safe == "" {
		return "_"
	}
	return safe
}

// PageBaseName is the file name a page is written under, without a suffix.
// The id leads, so a retitled page keeps its place and no two pages collide.
func PageBaseName(pageId, title string) string {
	if strings.TrimSpace(title) == "" {
		return pageId
	}
	return pageId + "-" + SafeName(title)
}

// LoadState reads what the archive holds. A missing file means nothing is
// archived yet, which is not an error.
func (self *Store) LoadState() (*State, error) {
	state := &State{
		Pages:      map[string]*PageState{},
		Blogposts:  map[string]*PageState{},
		SpaceMarks: map[string]string{},
	}
	content, err := os.ReadFile(filepath.Join(self.directory, stateFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return state, nil
		}
		return nil, fmt.Errorf("archive: reading %s: %w", stateFileName, err)
	}
	if err := json.Unmarshal(content, state); err != nil {
		return nil, fmt.Errorf("archive: parsing %s: %w", stateFileName, err)
	}
	if state.Pages == nil {
		state.Pages = map[string]*PageState{}
	}
	if state.SpaceMarks == nil {
		state.SpaceMarks = map[string]string{}
	}
	if state.Blogposts == nil {
		state.Blogposts = map[string]*PageState{}
	}
	return state, nil
}

// SaveState writes what the archive holds.
func (self *Store) SaveState(state *State) error {
	return self.writeJson(stateFileName, state)
}

// LoadUsers reads the account id to name map an earlier sync built. A missing
// file simply means nobody has been looked up yet.
func (self *Store) LoadUsers() (map[string]string, error) {
	users := map[string]string{}
	content, err := os.ReadFile(filepath.Join(self.directory, usersFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return users, nil
		}
		return nil, fmt.Errorf("archive: reading %s: %w", usersFileName, err)
	}
	if err := json.Unmarshal(content, &users); err != nil {
		return nil, fmt.Errorf("archive: parsing %s: %w", usersFileName, err)
	}
	return users, nil
}

// SaveUsers records who an account id belongs to, so a later sync need not ask
// again and a reader need not squint at an identifier.
func (self *Store) SaveUsers(users map[string]string) error {
	return self.writeJson(usersFileName, users)
}

// SaveSpaces records the spaces seen.
func (self *Store) SaveSpaces(spaces interface{}) error {
	return self.writeJson(spacesFileName, spaces)
}

// WritePage writes one page's markdown and the storage it came from.
func (self *Store) WritePage(spaceKey, baseName, markdown, storage string) (string, error) {
	return self.writeDocument(self.PagesDirectory(), spaceKey, baseName, markdown, storage)
}

func (self *Store) writeDocument(root, spaceKey, baseName, markdown, storage string) (string, error) {
	directory := filepath.Join(root, SafeName(spaceKey))
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", fmt.Errorf("archive: creating %s: %w", directory, err)
	}
	markdownPath := filepath.Join(directory, baseName+".md")
	if err := writeFileAtomic(markdownPath, []byte(markdown)); err != nil {
		return "", err
	}
	if err := writeFileAtomic(filepath.Join(directory, baseName+".xhtml"), []byte(storage)); err != nil {
		return "", err
	}
	relative, err := filepath.Rel(self.directory, markdownPath)
	if err != nil {
		return markdownPath, nil
	}
	return relative, nil
}

// RemoveDocument deletes an archived page and the storage beside it.
func (self *Store) RemoveDocument(relativePath string) error {
	full := filepath.Join(self.directory, relativePath)
	if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("archive: removing %s: %w", relativePath, err)
	}
	beside := strings.TrimSuffix(full, ".md") + ".xhtml"
	if err := os.Remove(beside); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("archive: removing %s: %w", beside, err)
	}
	return nil
}

// WriteIndex writes a file at the root of one space's directory.
func (self *Store) WriteIndex(root, spaceKey, markdown string) error {
	directory := filepath.Join(root, SafeName(spaceKey))
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("archive: creating %s: %w", directory, err)
	}
	return writeFileAtomic(filepath.Join(directory, "_index.md"), []byte(markdown))
}

// WriteComments writes the comments on one page.
func (self *Store) WriteComments(spaceKey, baseName, markdown string) error {
	directory := filepath.Join(self.CommentsDirectory(), SafeName(spaceKey))
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("archive: creating %s: %w", directory, err)
	}
	return writeFileAtomic(filepath.Join(directory, baseName+".md"), []byte(markdown))
}

// AppendAttachments adds records to the attachment index. The files
// themselves are not fetched; this is what lets a later run fetch the ones
// somebody asks for without walking the site again.
func (self *Store) AppendAttachments(records []*Attachment) error {
	if len(records) == 0 {
		return nil
	}
	self.lock.Lock()
	defer self.lock.Unlock()

	path := filepath.Join(self.directory, attachmentsFileName)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("archive: opening %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	encoder := json.NewEncoder(file)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			return fmt.Errorf("archive: writing %s: %w", path, err)
		}
	}
	return nil
}

// AttachmentsPath is the attachment index.
func (self *Store) AttachmentsPath() string {
	return filepath.Join(self.directory, attachmentsFileName)
}

// PageFiles lists every archived page's markdown file, sorted by path.
func (self *Store) PageFiles() ([]string, error) {
	var files []string
	root := self.PagesDirectory()
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) && path == root {
				return nil
			}
			return err
		}
		if !info.IsDir() && strings.HasSuffix(path, ".md") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("archive: walking %s: %w", root, err)
	}
	return files, nil
}

func (self *Store) writeJson(name string, value interface{}) error {
	self.lock.Lock()
	defer self.lock.Unlock()
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("archive: encoding %s: %w", name, err)
	}
	return writeFileAtomic(filepath.Join(self.directory, name), content)
}

// writeFileAtomic writes beside the target and renames over it, so a run cut
// short leaves the previous copy rather than half of a new one.
func writeFileAtomic(path string, content []byte) error {
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, content, 0o644); err != nil {
		return fmt.Errorf("archive: writing %s: %w", path, err)
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("archive: replacing %s: %w", path, err)
	}
	return nil
}
