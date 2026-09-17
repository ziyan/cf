package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"github.com/ziyan/cf/internal/archive"
	"github.com/ziyan/cf/internal/confluence"
	"github.com/ziyan/cf/internal/printer"
)

func newPruneCommand() *cobra.Command {
	pruneCommand := &cobra.Command{
		Use:   "prune <directory>",
		Short: "Say which archived pages the site no longer has, and optionally remove them",
		Long: "A sync only ever adds. A page deleted on the site, or moved to another space, " +
			"stays in the archive, so an archive that is years old quietly stops matching " +
			"what it is a copy of.\n\n" +
			"This reads every page id the site still has, which is cheap because it asks for " +
			"no bodies, and reports what the archive holds beyond that. It removes nothing " +
			"unless asked.",
		Args: cobra.ExactArgs(1),
		RunE: pruneRun,
	}
	pruneCommand.Flags().Bool("remove", false, "Delete the files, rather than only listing them")
	return pruneCommand
}

func pruneRun(command *cobra.Command, arguments []string) error {
	store, err := archive.Open(arguments[0])
	if err != nil {
		return err
	}
	apiClient, _, err := openClient(command)
	if err != nil {
		return err
	}
	shouldRemove, _ := command.Flags().GetBool("remove")

	state, err := store.LoadState()
	if err != nil {
		return err
	}

	onSite := make(map[string]struct{}, len(state.Pages))
	seen := 0
	err = confluence.PageIdentifiers(context.Background(), apiClient, func(pages []*confluence.Page) error {
		for _, page := range pages {
			onSite[page.ID] = struct{}{}
			seen++
		}
		if seen%20000 == 0 {
			printer.PrintInfo("  %d pages on the site", seen)
		}
		return nil
	})
	if err != nil {
		return err
	}

	// A walk that came back with nothing is a walk that failed, not a site
	// with no pages, and acting on it would empty the archive.
	if seen == 0 {
		return fmt.Errorf("commands: the site reported no pages at all, refusing to prune against that")
	}

	var stale []string
	for pageId := range state.Pages {
		if _, isThere := onSite[pageId]; !isThere {
			stale = append(stale, pageId)
		}
	}
	sort.Strings(stale)

	if printer.JSONOutput {
		records := make([]map[string]string, 0, len(stale))
		for _, pageId := range stale {
			page := state.Pages[pageId]
			records = append(records, map[string]string{
				"id": pageId, "title": page.Title, "space": page.SpaceKey, "path": page.Path,
			})
		}
		printer.PrintJSON(map[string]interface{}{
			"on_site": seen, "archived": len(state.Pages), "stale": records, "removed": shouldRemove,
		})
		return nil
	}

	printer.PrintInfo("%d pages on the site, %d in the archive", seen, len(state.Pages))
	if len(stale) == 0 {
		printer.PrintSuccess("nothing to prune")
		return nil
	}
	for _, pageId := range stale {
		page := state.Pages[pageId]
		printer.PrintInfo("  %s/%s  %s", page.SpaceKey, page.Title, pageId)
	}
	if !shouldRemove {
		printer.PrintInfo("")
		printer.PrintInfo("%d pages the site no longer has. Pass --remove to delete them.", len(stale))
		return nil
	}

	for _, pageId := range stale {
		page := state.Pages[pageId]
		if err := store.RemoveDocument(page.Path); err != nil {
			return err
		}
		delete(state.Pages, pageId)
	}
	if err := store.SaveState(state); err != nil {
		return err
	}
	printer.PrintSuccess("%d pages removed", len(stale))
	return nil
}

func newLabelsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "labels <directory>",
		Short: "Record which labels each page carries",
		Long: "Labels are read label by label rather than page by page. There are a few " +
			"thousand labels against a hundred thousand pages, so this way costs a few " +
			"thousand requests and the other way costs one per page.",
		Args: cobra.ExactArgs(1),
		RunE: labelsRun,
	}
}

func labelsRun(command *cobra.Command, arguments []string) error {
	store, err := archive.Open(arguments[0])
	if err != nil {
		return err
	}
	apiClient, _, err := openClient(command)
	if err != nil {
		return err
	}
	workerCount, _ := command.Flags().GetInt("workers")
	ctx := context.Background()

	labels, err := confluence.Labels(ctx, apiClient)
	if err != nil {
		return err
	}
	printer.PrintInfo("%d labels", len(labels))

	type labelPages struct {
		name    string
		pageIds []string
		err     error
	}
	work := make(chan *confluence.Label)
	outcomes := make(chan *labelPages)
	var waitGroup sync.WaitGroup
	for worker := 0; worker < workerCount; worker++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			for label := range work {
				pageIds, err := confluence.PagesWithLabel(ctx, apiClient, label.ID)
				outcomes <- &labelPages{name: label.Name, pageIds: pageIds, err: err}
			}
		}()
	}
	go func() {
		for _, label := range labels {
			work <- label
		}
		close(work)
		waitGroup.Wait()
		close(outcomes)
	}()

	byPage := map[string][]string{}
	done, tagged := 0, 0
	for outcome := range outcomes {
		if outcome.err != nil {
			return outcome.err
		}
		for _, pageId := range outcome.pageIds {
			byPage[pageId] = append(byPage[pageId], outcome.name)
			tagged++
		}
		done++
		if done%500 == 0 {
			printer.PrintInfo("  %d/%d labels, %d pages tagged", done, len(labels), len(byPage))
		}
	}
	for pageId := range byPage {
		sort.Strings(byPage[pageId])
	}
	if err := store.SaveLabels(byPage); err != nil {
		return err
	}
	printer.PrintSuccess("%d labels on %d pages recorded", len(labels), len(byPage))
	return nil
}

func newTreeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "tree <directory>",
		Short: "Write an index of each space, so the hierarchy is readable",
		Long: "Confluence is a tree and the archive is a flat directory per space. This " +
			"writes _index.md beside the pages of each space, with the titles nested the " +
			"way the site nests them and a link to each file.",
		Args: cobra.ExactArgs(1),
		RunE: treeRun,
	}
}

func treeRun(_ *cobra.Command, arguments []string) error {
	store, err := archive.Open(arguments[0])
	if err != nil {
		return err
	}
	state, err := store.LoadState()
	if err != nil {
		return err
	}
	labels, err := store.LoadLabels()
	if err != nil {
		return err
	}

	bySpace := map[string][]string{}
	for pageId, page := range state.Pages {
		bySpace[page.SpaceKey] = append(bySpace[page.SpaceKey], pageId)
	}

	written := 0
	for spaceKey, pageIds := range bySpace {
		document := spaceIndex(spaceKey, pageIds, state, labels)
		if err := store.WriteIndex(store.PagesDirectory(), spaceKey, document); err != nil {
			return err
		}
		written++
	}
	printer.PrintSuccess("%d space indexes written", written)
	return nil
}

// spaceIndex renders one space as the tree the site holds. A page whose parent
// is outside this space, or is not archived, is shown at the top rather than
// dropped.
func spaceIndex(spaceKey string, pageIds []string, state *archive.State, labels map[string][]string) string {
	children := map[string][]string{}
	inSpace := make(map[string]struct{}, len(pageIds))
	for _, pageId := range pageIds {
		inSpace[pageId] = struct{}{}
	}
	var roots []string
	for _, pageId := range pageIds {
		parent := state.Pages[pageId].ParentID
		if _, isHere := inSpace[parent]; parent == "" || !isHere {
			roots = append(roots, pageId)
			continue
		}
		children[parent] = append(children[parent], pageId)
	}

	byTitle := func(ids []string) {
		sort.SliceStable(ids, func(first, second int) bool {
			return state.Pages[ids[first]].Title < state.Pages[ids[second]].Title
		})
	}
	byTitle(roots)
	for parent := range children {
		byTitle(children[parent])
	}

	builder := &strings.Builder{}
	builder.WriteString("# " + spaceKey + "\n\n")
	_, _ = fmt.Fprintf(builder, "%d pages.\n\n", len(pageIds))

	// Iterative, because a space can nest deeply enough to matter and a
	// cycle in the parent links must not hang this.
	type frame struct {
		pageId string
		depth  int
	}
	visited := map[string]struct{}{}
	stack := make([]frame, 0, len(roots))
	for index := len(roots) - 1; index >= 0; index-- {
		stack = append(stack, frame{roots[index], 0})
	}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if _, isSeen := visited[current.pageId]; isSeen {
			continue
		}
		visited[current.pageId] = struct{}{}

		page := state.Pages[current.pageId]
		name := page.Path
		if index := strings.LastIndex(name, "/"); index >= 0 {
			name = name[index+1:]
		}
		builder.WriteString(strings.Repeat("  ", current.depth) + "- [" + page.Title + "](" + name + ")")
		if tags := labels[current.pageId]; len(tags) > 0 {
			builder.WriteString("  `" + strings.Join(tags, "` `") + "`")
		}
		builder.WriteString("\n")

		below := children[current.pageId]
		for index := len(below) - 1; index >= 0; index-- {
			stack = append(stack, frame{below[index], current.depth + 1})
		}
	}
	return builder.String()
}

// newBlogpostsNote is not a command of its own: blog posts are archived by a
// sync, and this says so where somebody would look for them.
func newBlogpostsNote() *cobra.Command {
	return &cobra.Command{
		Use:    "blogposts",
		Short:  "Blog posts are archived by sync, into blogposts/<space>/",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			printer.PrintInfo("Blog posts are archived by cf archive sync, into blogposts/<space>/.")
			return nil
		},
	}
}
