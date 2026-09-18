package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/ziyan/cf/internal/archive"
	"github.com/ziyan/cf/internal/client"
	"github.com/ziyan/cf/internal/config"
	"github.com/ziyan/cf/internal/confluence"
	"github.com/ziyan/cf/internal/printer"
)

const (
	reportInterval = 500

	// defaultWorkers is how many requests a command keeps in flight where it
	// must make one per page. Every one is a round trip, so this is nearly all
	// of the time such a command takes. Eight is well inside what a site
	// shared with other people will answer without complaint.
	defaultWorkers = 8
	maximumWorkers = 32
)

func init() {
	archiveCommand := &cobra.Command{
		Use:   "archive",
		Short: "Archive a Confluence site to a local directory and search it offline",
	}

	syncCommand := &cobra.Command{
		Use:   "sync <directory>",
		Short: "Fetch pages and comments that changed since the last run",
		Args:  cobra.ExactArgs(1),
		RunE:  archiveSyncRun,
	}
	syncCommand.Flags().Bool("full", false, "Ignore the marks and read the whole site again")
	syncCommand.Flags().String("space", "", "Only spaces whose key contains this substring")
	syncCommand.Flags().Bool("skip-comments", false, "Do not read comments this run")
	syncCommand.Flags().Bool("skip-blogposts", false, "Do not read blog posts this run")

	statusCommand := &cobra.Command{
		Use:   "status <directory>",
		Short: "Show what the archive holds",
		Args:  cobra.ExactArgs(1),
		RunE:  archiveStatusRun,
	}

	archiveCommand.PersistentFlags().Int("workers", defaultWorkers,
		"How many requests to have in flight at once, where a command makes one per page")
	archiveCommand.AddCommand(syncCommand, statusCommand, newSearchCommand(), newAttachmentsCommand(),
		newPruneCommand(), newLabelsCommand(), newTreeCommand(), newBlogpostsNote())
	rootCommand.AddCommand(archiveCommand)
}

// userDirectory resolves account ids to names, asking the site once per person
// and remembering the answer in the archive. A page's author and everybody it
// mentions are identifiers otherwise.
type userDirectory struct {
	names     map[string]string
	apiClient *client.Client
	asked     int
}

func newUserDirectory(store *archive.Store, apiClient *client.Client) (*userDirectory, error) {
	names, err := store.LoadUsers()
	if err != nil {
		return nil, err
	}
	return &userDirectory{names: names, apiClient: apiClient}, nil
}

// learn looks up anybody in this storage who is not known yet.
func (self *userDirectory) learn(ctx context.Context, accountIds []string) {
	for _, accountId := range accountIds {
		if _, isKnown := self.names[accountId]; isKnown {
			continue
		}
		// An empty entry is remembered too: an account that cannot be
		// resolved should be asked about once, not once per page.
		self.names[accountId] = confluence.ResolveUser(ctx, self.apiClient, accountId)
		self.asked++
	}
}

// name is who an account belongs to, or the identifier when nobody knows.
func (self *userDirectory) name(accountId string) string {
	if resolved, isKnown := self.names[accountId]; isKnown && resolved != "" {
		return resolved
	}
	return accountId
}

func openClient(command *cobra.Command) (*client.Client, *config.Profile, error) {
	configuration, err := config.Load()
	if err != nil {
		return nil, nil, err
	}
	name, _ := command.Flags().GetString("profile")
	profile, err := configuration.ActiveServer(name)
	if err != nil {
		return nil, nil, err
	}
	return client.New(profile), profile, nil
}

func archiveSyncRun(command *cobra.Command, arguments []string) error {
	store, err := archive.Create(arguments[0])
	if err != nil {
		return err
	}
	apiClient, profile, err := openClient(command)
	if err != nil {
		return err
	}
	ctx := context.Background()

	isFullSync, _ := command.Flags().GetBool("full")
	spaceSubstring, _ := command.Flags().GetString("space")
	shouldSkipComments, _ := command.Flags().GetBool("skip-comments")
	shouldSkipBlogposts, _ := command.Flags().GetBool("skip-blogposts")
	workerCount, _ := command.Flags().GetInt("workers")
	if workerCount < 1 || workerCount > maximumWorkers {
		return fmt.Errorf("commands: --workers must be between 1 and %d", maximumWorkers)
	}

	state, err := store.LoadState()
	if err != nil {
		return err
	}
	users, err := newUserDirectory(store, apiClient)
	if err != nil {
		return err
	}

	printer.PrintInfo("Site: %s", profile.BaseURL())

	spaces, err := confluence.Spaces(ctx, apiClient)
	if err != nil {
		return err
	}
	if err := store.SaveSpaces(spaces); err != nil {
		return err
	}
	spaceKeys := make(map[string]string, len(spaces))
	for _, space := range spaces {
		spaceKeys[space.ID] = space.Key
	}
	printer.PrintInfo("%d spaces", len(spaces))

	pagesMark := state.PagesMark
	if isFullSync {
		pagesMark = ""
	}
	// Narrowing to a space is done at the server. Reading the whole site to
	// keep a few pages would cost the same as archiving it.
	var spaceIds []string
	if spaceSubstring != "" {
		for _, space := range spaces {
			if strings.Contains(space.Key, spaceSubstring) {
				spaceIds = append(spaceIds, space.ID)
			}
		}
		if len(spaceIds) == 0 {
			return fmt.Errorf("commands: no space key contains %q", spaceSubstring)
		}
		printer.PrintInfo("%d spaces match %q", len(spaceIds), spaceSubstring)
	}

	written := 0
	var writtenPageIds []string
	if len(spaceIds) == 0 {
		count, newest, pageIds, err := archiveSyncPages(ctx, apiClient, store, state, users, spaceKeys, pagesMark, "")
		if err != nil {
			return err
		}
		written, writtenPageIds = count, pageIds
		if newest > state.PagesMark {
			state.PagesMark = newest
		}
	} else {
		for _, spaceId := range spaceIds {
			mark := state.SpaceMarks[spaceId]
			if isFullSync {
				mark = ""
			}
			count, newest, pageIds, err := archiveSyncPages(ctx, apiClient, store, state, users, spaceKeys, mark, spaceId)
			if err != nil {
				return err
			}
			written += count
			writtenPageIds = append(writtenPageIds, pageIds...)
			// Only this space's own mark moves. The whole-site mark stays
			// where it is, or the next whole-site sync would stop at a point
			// this run never reached outside this space.
			if newest > state.SpaceMarks[spaceId] {
				state.SpaceMarks[spaceId] = newest
			}
		}
	}

	// Blog posts live behind their own endpoint, so a walk over pages never
	// sees one. A site can hold thousands.
	if !shouldSkipBlogposts && len(spaceIds) == 0 {
		blogpostsMark := state.BlogpostsMark
		if isFullSync {
			blogpostsMark = ""
		}
		if err := archiveSyncBlogposts(ctx, apiClient, store, state, users, spaceKeys, blogpostsMark); err != nil {
			return err
		}
	}

	if !shouldSkipComments {
		if len(spaceIds) == 0 {
			commentsMark := state.CommentsMark
			if isFullSync {
				commentsMark = ""
			}
			if err := archiveSyncComments(ctx, apiClient, store, state, users, commentsMark, workerCount); err != nil {
				return err
			}
		} else {
			// A narrowed run asks each page it wrote for its comments. The
			// site-wide walk would read every comment on the site to find the
			// few that belong here.
			written, _, err := archiveCommentsForPages(ctx, apiClient, store, state, users, writtenPageIds, workerCount)
			if err != nil {
				return err
			}
			printer.PrintInfo("%d pages of comments written", written)
		}
	}

	if err := store.SaveState(state); err != nil {
		return err
	}
	if err := store.SaveUsers(users.names); err != nil {
		return err
	}
	if users.asked > 0 {
		printer.PrintInfo("%d people looked up, %d known", users.asked, len(users.names))
	}
	printer.PrintSuccess("Archive up to date in %s (%d pages written)", store.Directory(), written)
	return nil
}

// archiveSyncPages walks the site newest modification first and stops once it
// reaches what the archive already holds.
//
// The walk is by cursor, not by offset: an offset walk over a site of this
// size gets slower the deeper it goes, and a page written while the walk is in
// progress shifts the window and is missed.
//
// The mark alone does not decide what to write. A page at or after it is
// checked against the version recorded for it, so a run cut short between
// writing a page and saving the state writes nothing twice, and two pages
// saved in the same second are told apart.
func archiveSyncPages(ctx context.Context, apiClient *client.Client, store *archive.Store,
	state *archive.State, users *userDirectory, spaceKeys map[string]string, mark, spaceId string) (int, string, []string, error) {

	seen, written, skipped := 0, 0, 0
	newestSeen := ""
	var writtenPageIds []string

	err := confluence.PagesNewestFirst(ctx, apiClient, spaceId, func(pages []*confluence.Page) (bool, error) {
		for _, page := range pages {
			seen++
			modified := page.ModifiedAt()
			if newestSeen == "" || modified > newestSeen {
				newestSeen = modified
			}

			// Older than the mark means everything from here back is already
			// archived, since the walk is newest first.
			if mark != "" && modified < mark {
				return false, nil
			}

			spaceKey := spaceKeys[page.SpaceID]
			if spaceKey == "" {
				spaceKey = "space-" + page.SpaceID
			}

			if previous, isKnown := state.Pages[page.ID]; isKnown &&
				page.Version != nil && previous.Version == page.Version.Number {
				skipped++
				continue
			}

			if err := writePage(ctx, store, state, users, page, spaceKey); err != nil {
				return false, err
			}
			written++
			writtenPageIds = append(writtenPageIds, page.ID)
			if written%reportInterval == 0 {
				printer.PrintInfo("  %d pages seen, %d written", seen, written)
				if err := store.SaveState(state); err != nil {
					return false, err
				}
			}
		}
		return true, nil
	})
	if err != nil {
		return written, newestSeen, writtenPageIds, err
	}
	printer.PrintInfo("%d pages seen, %d written, %d already current", seen, written, skipped)
	return written, newestSeen, writtenPageIds, nil
}

func writePage(ctx context.Context, store *archive.Store, state *archive.State, users *userDirectory,
	page *confluence.Page, spaceKey string) error {

	storage := ""
	if page.Body != nil {
		storage = page.Body.Storage.Value
	}
	// Whoever this page mentions, and whoever wrote it, are looked up before
	// the conversion, so the markdown carries names rather than identifiers.
	users.learn(ctx, append(confluence.MentionedUsers(storage), page.Version.AuthorID))
	markdown, err := confluence.ToMarkdownWithUsers(storage, users.names)
	if err != nil {
		return fmt.Errorf("commands: converting page %s: %w", page.ID, err)
	}

	version := 0
	author := ""
	if page.Version != nil {
		version = page.Version.Number
		author = users.name(page.Version.AuthorID)
	}
	document := frontMatter(map[string]string{
		"id":       page.ID,
		"title":    page.Title,
		"space":    spaceKey,
		"version":  fmt.Sprint(version),
		"modified": page.ModifiedAt(),
		"author":   author,
		"parent":   page.ParentID,
		"status":   page.Status,
	}) + markdown

	baseName := archive.PageBaseName(page.ID, page.Title)
	path, err := store.WritePage(spaceKey, baseName, document, storage)
	if err != nil {
		return err
	}
	state.Pages[page.ID] = &archive.PageState{
		SpaceKey:   spaceKey,
		Title:      page.Title,
		ParentID:   page.ParentID,
		Version:    version,
		ModifiedAt: page.ModifiedAt(),
		Path:       path,
	}
	return nil
}

// archiveCommentsForPages reads the comments of the pages named, which is what
// a narrowed or incremental run wants: the site-wide walk reads every comment
// there is to find the few that changed.
func archiveCommentsForPages(ctx context.Context, apiClient *client.Client, store *archive.Store,
	state *archive.State, users *userDirectory, pageIds []string, workerCount int) (int, string, error) {

	// Reading one page's comments is a round trip, and the round trip is
	// nearly all of the time, so several are kept in flight. The writing and
	// the counting stay on this goroutine, where the state is.
	type fetched struct {
		pageId   string
		comments []*confluence.Comment
		err      error
	}
	work := make(chan string)
	outcomes := make(chan *fetched)
	var waitGroup sync.WaitGroup
	for worker := 0; worker < workerCount; worker++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			for pageId := range work {
				contentType := "pages"
				if _, isBlogpost := state.Blogposts[pageId]; isBlogpost {
					contentType = "blogposts"
				}
				comments, err := confluence.CommentsOn(ctx, apiClient, pageId, contentType)
				outcomes <- &fetched{pageId: pageId, comments: comments, err: err}
			}
		}()
	}
	go func() {
		for _, pageId := range pageIds {
			_, isPage := state.Pages[pageId]
			_, isBlogpost := state.Blogposts[pageId]
			if isPage || isBlogpost {
				work <- pageId
			}
		}
		close(work)
		waitGroup.Wait()
		close(outcomes)
	}()

	written, newest := 0, ""
	for outcome := range outcomes {
		if outcome.err != nil {
			return written, newest, outcome.err
		}
		pageId, comments := outcome.pageId, outcome.comments
		pageState, isKnown := state.Pages[pageId]
		if !isKnown {
			pageState = state.Blogposts[pageId]
		}
		if len(comments) == 0 {
			continue
		}
		for _, comment := range comments {
			if modified := comment.ModifiedAt(); modified > newest {
				newest = modified
			}
		}
		document, err := commentsMarkdown(ctx, users, pageState, comments)
		if err != nil {
			return written, newest, err
		}
		if err := store.WriteComments(pageState.SpaceKey, archive.PageBaseName(pageId, pageState.Title), document); err != nil {
			return written, newest, err
		}
		pageState.CommentCount = len(comments)
		written++
	}
	return written, newest, nil
}

// archiveSyncBlogposts brings the blog posts up to date, the same way pages
// are done and with a mark of their own.
func archiveSyncBlogposts(ctx context.Context, apiClient *client.Client, store *archive.Store,
	state *archive.State, users *userDirectory, spaceKeys map[string]string, mark string) error {

	seen, written, newest := 0, 0, ""
	err := confluence.BlogpostsNewestFirst(ctx, apiClient, func(posts []*confluence.Blogpost) (bool, error) {
		for _, post := range posts {
			seen++
			modified := post.ModifiedAt()
			if modified > newest {
				newest = modified
			}
			if mark != "" && modified < mark {
				return false, nil
			}
			if previous, isKnown := state.Blogposts[post.ID]; isKnown &&
				post.Version != nil && previous.Version == post.Version.Number {
				continue
			}
			spaceKey := spaceKeys[post.SpaceID]
			if spaceKey == "" {
				spaceKey = "space-" + post.SpaceID
			}
			if err := writeBlogpost(ctx, store, state, users, post, spaceKey); err != nil {
				return false, err
			}
			written++
		}
		return true, nil
	})
	if err != nil {
		return err
	}
	if newest > state.BlogpostsMark {
		state.BlogpostsMark = newest
	}
	printer.PrintInfo("%d blog posts seen, %d written", seen, written)
	return nil
}

func writeBlogpost(ctx context.Context, store *archive.Store, state *archive.State, users *userDirectory,
	post *confluence.Blogpost, spaceKey string) error {

	storage := ""
	if post.Body != nil {
		storage = post.Body.Storage.Value
	}
	authorId := post.AuthorID
	version := 0
	if post.Version != nil {
		version = post.Version.Number
		if post.Version.AuthorID != "" {
			authorId = post.Version.AuthorID
		}
	}
	users.learn(ctx, append(confluence.MentionedUsers(storage), authorId))
	markdown, err := confluence.ToMarkdownWithUsers(storage, users.names)
	if err != nil {
		return fmt.Errorf("commands: converting blog post %s: %w", post.ID, err)
	}

	document := frontMatter(map[string]string{
		"id":       post.ID,
		"title":    post.Title,
		"space":    spaceKey,
		"type":     "blogpost",
		"version":  fmt.Sprint(version),
		"modified": post.ModifiedAt(),
		"author":   users.name(authorId),
		"status":   post.Status,
	}) + markdown

	baseName := archive.PageBaseName(post.ID, post.Title)
	path, err := store.WriteBlogpost(spaceKey, baseName, document, storage)
	if err != nil {
		return err
	}
	state.Blogposts[post.ID] = &archive.PageState{
		SpaceKey:   spaceKey,
		Title:      post.Title,
		Version:    version,
		ModifiedAt: post.ModifiedAt(),
		Path:       path,
	}
	return nil
}

func archiveSyncComments(ctx context.Context, apiClient *client.Client, store *archive.Store,
	state *archive.State, users *userDirectory, mark string, workerCount int) error {

	if mark == "" {
		return archiveSyncAllComments(ctx, apiClient, store, state, users)
	}

	pageIds, err := confluence.PagesWithChangedComments(ctx, apiClient, mark)
	if err != nil {
		return err
	}
	if len(pageIds) == 0 {
		printer.PrintInfo("no comments changed")
		return nil
	}
	written, newest, err := archiveCommentsForPages(ctx, apiClient, store, state, users, pageIds, workerCount)
	if err != nil {
		return err
	}
	if newest > state.CommentsMark {
		state.CommentsMark = newest
	}
	printer.PrintInfo("%d pages had comments change, %d written", len(pageIds), written)
	return nil
}

// archiveSyncAllComments reads the whole site's comments and files them under
// the pages they belong to.
func archiveSyncAllComments(ctx context.Context, apiClient *client.Client, store *archive.Store,
	state *archive.State, users *userDirectory) error {

	byPage := map[string][]*confluence.Comment{}
	seen, newest := 0, ""
	err := confluence.AllComments(ctx, apiClient, func(comments []*confluence.Comment) (bool, error) {
		for _, comment := range comments {
			seen++
			if modified := comment.ModifiedAt(); modified > newest {
				newest = modified
			}
			byPage[comment.ParentID()] = append(byPage[comment.ParentID()], comment)
		}
		if seen%10000 == 0 {
			printer.PrintInfo("  %d comments seen", seen)
		}
		return true, nil
	})
	if err != nil {
		return err
	}

	written, missing := 0, 0
	for pageId, comments := range byPage {
		// A comment belongs to a page or to a blog post, and both are
		// archived, so both are looked in.
		pageState, isKnown := state.Pages[pageId]
		if !isKnown {
			pageState, isKnown = state.Blogposts[pageId]
		}
		if !isKnown {
			missing++
			continue
		}
		document, err := commentsMarkdown(ctx, users, pageState, comments)
		if err != nil {
			return err
		}
		if err := store.WriteComments(pageState.SpaceKey, archive.PageBaseName(pageId, pageState.Title), document); err != nil {
			return err
		}
		pageState.CommentCount = len(comments)
		written++
	}
	if newest > state.CommentsMark {
		state.CommentsMark = newest
	}
	printer.PrintInfo("%d comments on %d pages, %d written, %d on pages not archived", seen, len(byPage), written, missing)
	return nil
}

func commentsMarkdown(ctx context.Context, users *userDirectory, pageState *archive.PageState,
	comments []*confluence.Comment) (string, error) {
	sort.SliceStable(comments, func(first, second int) bool {
		return comments[first].ModifiedAt() < comments[second].ModifiedAt()
	})
	builder := &strings.Builder{}
	builder.WriteString(frontMatter(map[string]string{
		"title": pageState.Title,
		"space": pageState.SpaceKey,
		"count": fmt.Sprint(len(comments)),
	}))
	builder.WriteString("# Comments on " + pageState.Title + "\n")
	for _, comment := range comments {
		storage := ""
		if comment.Body != nil {
			storage = comment.Body.Storage.Value
		}
		users.learn(ctx, confluence.MentionedUsers(storage))
		markdown, err := confluence.ToMarkdownWithUsers(storage, users.names)
		if err != nil {
			return "", err
		}
		author, when := "", comment.ModifiedAt()
		if comment.Version != nil {
			users.learn(ctx, []string{comment.Version.AuthorID})
			author = users.name(comment.Version.AuthorID)
		}
		builder.WriteString("\n## " + when)
		if comment.Kind != "" {
			builder.WriteString(" (" + string(comment.Kind) + ")")
		}
		if author != "" {
			builder.WriteString(" by " + author)
		}
		if comment.ResolutionStatus != "" && comment.ResolutionStatus != "open" {
			builder.WriteString(" (" + comment.ResolutionStatus + ")")
		}
		builder.WriteString("\n\n" + strings.TrimSpace(markdown) + "\n")
	}
	return builder.String(), nil
}

// frontMatter is the YAML block at the top of an archived file, so a reader
// knows what the page was without going back to the site.
func frontMatter(fields map[string]string) string {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	builder := &strings.Builder{}
	builder.WriteString("---\n")
	for _, key := range keys {
		value := fields[key]
		if value == "" {
			continue
		}
		builder.WriteString(key + ": " + quoteYaml(value) + "\n")
	}
	builder.WriteString("---\n\n")
	return builder.String()
}

// quoteYaml quotes a value when it would otherwise be read as something other
// than a string.
func quoteYaml(value string) string {
	if strings.ContainsAny(value, `:#"'{}[]|>&*!%@`+"`\n") || strings.TrimSpace(value) != value {
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", " ").Replace(value) + `"`
	}
	return value
}

func archiveStatusRun(command *cobra.Command, arguments []string) error {
	store, err := archive.Open(arguments[0])
	if err != nil {
		return err
	}
	state, err := store.LoadState()
	if err != nil {
		return err
	}
	files, err := store.PageFiles()
	if err != nil {
		return err
	}

	spaces := map[string]int{}
	for _, page := range state.Pages {
		spaces[page.SpaceKey]++
	}

	if printer.JSONOutput {
		printer.PrintJSON(map[string]interface{}{
			"directory":     store.Directory(),
			"pages":         len(state.Pages),
			"page_files":    len(files),
			"spaces":        len(spaces),
			"pages_mark":    state.PagesMark,
			"comments_mark": state.CommentsMark,
		})
		return nil
	}
	printer.PrintInfo("Directory:  %s", store.Directory())
	printer.PrintInfo("Spaces:     %d", len(spaces))
	printer.PrintInfo("Pages:      %d", len(state.Pages))
	printer.PrintInfo("Files:      %d", len(files))
	if state.PagesMark != "" {
		printer.PrintInfo("Pages to:   %s", formatMark(state.PagesMark))
	}
	if state.CommentsMark != "" {
		printer.PrintInfo("Comments to: %s", formatMark(state.CommentsMark))
	}
	return nil
}

func formatMark(mark string) string {
	parsed, err := time.Parse(time.RFC3339, mark)
	if err != nil {
		return mark
	}
	return parsed.Local().Format("2006-01-02 15:04")
}
