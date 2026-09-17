package commands

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"github.com/ziyan/cf/internal/archive"
	"github.com/ziyan/cf/internal/config"
	"github.com/ziyan/cf/internal/printer"
)

// searchMatch is one line of one page that matched.
type searchMatch struct {
	SpaceKey string `json:"space"`
	Title    string `json:"title"`
	PageID   string `json:"page_id"`
	Line     int    `json:"line"`
	Text     string `json:"text"`
	URL      string `json:"url,omitempty"`
}

func newSearchCommand() *cobra.Command {
	searchCommand := &cobra.Command{
		Use:   "search <directory> <query>",
		Short: "Search the archived pages",
		Args:  cobra.ExactArgs(2),
		RunE:  archiveSearchRun,
	}
	searchCommand.Flags().StringP("space", "s", "", "Only spaces whose key contains this substring")
	searchCommand.Flags().IntP("limit", "n", 50, "Maximum matches to print (0 for no limit)")
	searchCommand.Flags().Bool("regex", false, "Treat the query as a regular expression")
	searchCommand.Flags().Bool("case-sensitive", false, "Match case exactly")
	searchCommand.Flags().Bool("comments", false, "Search the comments instead of the pages")
	return searchCommand
}

func archiveSearchRun(command *cobra.Command, arguments []string) error {
	store, err := archive.Open(arguments[0])
	if err != nil {
		return err
	}
	query := arguments[1]

	spaceSubstring, _ := command.Flags().GetString("space")
	limit, _ := command.Flags().GetInt("limit")
	isRegex, _ := command.Flags().GetBool("regex")
	isCaseSensitive, _ := command.Flags().GetBool("case-sensitive")
	searchComments, _ := command.Flags().GetBool("comments")

	matches, err := newMatcher(query, isRegex, isCaseSensitive)
	if err != nil {
		return err
	}

	root := store.PagesDirectory()
	if searchComments {
		root = store.CommentsDirectory()
	}
	files, err := filesUnder(root, spaceSubstring)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("commands: %s holds no archived pages, run cf archive sync first", store.Directory())
	}

	found, err := searchFiles(files, matches)
	if err != nil {
		return err
	}
	sort.SliceStable(found, func(first, second int) bool {
		if found[first].SpaceKey != found[second].SpaceKey {
			return found[first].SpaceKey < found[second].SpaceKey
		}
		return found[first].Title < found[second].Title
	})

	baseURL := ""
	if configuration, err := config.Load(); err == nil {
		name, _ := command.Flags().GetString("profile")
		if profile, err := configuration.ActiveServer(name); err == nil {
			baseURL = profile.BaseURL()
		}
	}
	for _, match := range found {
		if baseURL != "" && match.PageID != "" {
			match.URL = baseURL + "/wiki/spaces/" + match.SpaceKey + "/pages/" + match.PageID
		}
	}

	total := len(found)
	if limit > 0 && len(found) > limit {
		found = found[:limit]
	}

	if printer.JSONOutput {
		printer.PrintJSON(map[string]interface{}{"query": query, "total": total, "matches": found})
		return nil
	}
	if total == 0 {
		printer.PrintInfo("No matches.")
		return nil
	}
	for _, match := range found {
		printer.PrintInfo("%s/%s:%d", match.SpaceKey, match.Title, match.Line)
		printer.PrintInfo("    %s", match.Text)
		if match.URL != "" {
			printer.PrintInfo("    %s", match.URL)
		}
		printer.PrintInfo("")
	}
	if limit > 0 && total > limit {
		printer.PrintInfo("%d matches, showing %d. Raise --limit to see more.", total, limit)
	} else {
		printer.PrintInfo("%d matches.", total)
	}
	return nil
}

func filesUnder(root, spaceSubstring string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("commands: reading %s: %w", root, err)
	}
	var files []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if spaceSubstring != "" && !strings.Contains(entry.Name(), spaceSubstring) {
			continue
		}
		spaceFiles, err := os.ReadDir(root + "/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("commands: reading %s: %w", entry.Name(), err)
		}
		for _, file := range spaceFiles {
			if strings.HasSuffix(file.Name(), ".md") {
				files = append(files, root+"/"+entry.Name()+"/"+file.Name())
			}
		}
	}
	sort.Strings(files)
	return files, nil
}

// searchFiles reads the archived files in parallel, which is the difference
// between a few seconds and most of a minute over a site of any size.
func searchFiles(files []string, matches func([]byte) bool) ([]*searchMatch, error) {
	workerCount := runtime.NumCPU()
	if workerCount > len(files) {
		workerCount = len(files)
	}
	if workerCount < 1 {
		workerCount = 1
	}

	work := make(chan string)
	var waitGroup sync.WaitGroup
	var lock sync.Mutex
	var found []*searchMatch
	var firstError error

	for worker := 0; worker < workerCount; worker++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			for path := range work {
				fileMatches, err := searchOneFile(path, matches)
				lock.Lock()
				found = append(found, fileMatches...)
				if err != nil && firstError == nil {
					firstError = err
				}
				lock.Unlock()
			}
		}()
	}
	for _, path := range files {
		work <- path
	}
	close(work)
	waitGroup.Wait()
	return found, firstError
}

func searchOneFile(path string, matches func([]byte) bool) ([]*searchMatch, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("commands: opening %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	space, title, pageId := describe(path)
	var results []*searchMatch
	reader := bufio.NewReaderSize(file, 1<<20)
	lineNumber, inFrontMatter, seenFence := 0, false, 0
	for {
		line, err := readLine(reader)
		lineNumber++
		text := strings.TrimRight(string(line), "\r")
		// The front matter is metadata, and matching it would report every
		// page whose title happens to hold the query.
		if lineNumber == 1 && text == "---" {
			inFrontMatter, seenFence = true, 1
		} else if inFrontMatter && text == "---" {
			seenFence++
			if seenFence == 2 {
				inFrontMatter = false
			}
		} else if !inFrontMatter && len(line) > 0 && matches(line) {
			if value := fieldFrom(text, "title: "); value != "" && title == "" {
				title = value
			}
			results = append(results, &searchMatch{
				SpaceKey: space, Title: title, PageID: pageId,
				Line: lineNumber, Text: trimTo(text, 300),
			})
		}
		if err != nil {
			return results, nil
		}
	}
}

func readLine(reader *bufio.Reader) ([]byte, error) {
	var collected []byte
	for {
		chunk, isPrefix, err := reader.ReadLine()
		collected = append(collected, chunk...)
		if err != nil || !isPrefix {
			return collected, err
		}
	}
}

// describe reads the space, title and page id out of the file's path, which
// is where the archive puts them.
func describe(path string) (space, title, pageId string) {
	parts := strings.Split(path, "/")
	if len(parts) >= 2 {
		space = parts[len(parts)-2]
	}
	name := strings.TrimSuffix(parts[len(parts)-1], ".md")
	if index := strings.Index(name, "-"); index > 0 {
		pageId, title = name[:index], strings.ReplaceAll(name[index+1:], "-", " ")
	} else {
		title = name
	}
	return space, title, pageId
}

func fieldFrom(line, prefix string) string {
	if strings.HasPrefix(line, prefix) {
		return strings.Trim(strings.TrimPrefix(line, prefix), `"`)
	}
	return ""
}

func trimTo(text string, length int) string {
	text = strings.TrimSpace(text)
	if len(text) <= length {
		return text
	}
	return text[:length] + "..."
}

// newMatcher builds the one test a search applies. A plain query is a byte
// search, which runs through the archive several times faster than a regular
// expression compiled from the same text.
func newMatcher(query string, isRegex, isCaseSensitive bool) (func([]byte) bool, error) {
	if isRegex {
		expression := query
		if !isCaseSensitive {
			expression = "(?i)" + expression
		}
		pattern, err := regexp.Compile(expression)
		if err != nil {
			return nil, fmt.Errorf("commands: compiling the query: %w", err)
		}
		return pattern.Match, nil
	}
	if isCaseSensitive {
		wanted := []byte(query)
		return func(text []byte) bool { return bytesContains(text, wanted) }, nil
	}
	wanted := []byte(strings.ToLower(query))
	return func(text []byte) bool { return bytesContainsFold(text, wanted) }, nil
}
