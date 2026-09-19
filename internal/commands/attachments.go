package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/ziyan/cf/internal/client"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/ziyan/cf/internal/archive"
	"github.com/ziyan/cf/internal/confluence"
	"github.com/ziyan/cf/internal/printer"
)

func newAttachmentsCommand() *cobra.Command {
	attachmentsCommand := &cobra.Command{
		Use:   "attachments",
		Short: "Record what is attached, and fetch the files worth keeping",
		Long: "A sync records pages and comments, not files. A large site holds " +
			"attachments by the million, which is not something to fetch by " +
			"accident.\n\n" +
			"So attachments are two deliberate steps: index says what exists, and " +
			"download fetches the ones a filter picks out.",
	}

	indexCommand := &cobra.Command{
		Use:   "index <directory>",
		Short: "Record what is attached to the archived pages",
		Args:  cobra.ExactArgs(1),
		RunE:  attachmentsIndexRun,
	}
	indexCommand.Flags().StringP("space", "s", "", "Only spaces whose key contains this substring")
	indexCommand.Flags().Bool("all", false, "Walk the whole site at once, rather than asking each archived page")

	downloadCommand := &cobra.Command{
		Use:   "download <directory>",
		Short: "Fetch attachments the filters pick out of the index",
		Args:  cobra.ExactArgs(1),
		RunE:  attachmentsDownloadRun,
	}
	downloadCommand.Flags().StringP("space", "s", "", "Only spaces whose key contains this substring")
	downloadCommand.Flags().String("type", "", "Only this media type, as a prefix such as image/ or application/pdf")
	downloadCommand.Flags().Float64("max-mb", 25, "Skip attachments larger than this many megabytes")
	downloadCommand.Flags().Int("limit", 0, "Stop after this many files (0 for no limit)")
	downloadCommand.Flags().Bool("dry-run", false, "Say what would be fetched and fetch nothing")

	attachmentsCommand.AddCommand(indexCommand, downloadCommand)
	return attachmentsCommand
}

// attachmentsIndexRun asks each archived page what is attached to it. That is
// one request per page, which is why it is scoped: the whole site at once is
// nearly two million records and hours of walking.
func attachmentsIndexRun(command *cobra.Command, arguments []string) error {
	store, err := archive.Open(arguments[0])
	if err != nil {
		return err
	}
	apiClient, _, err := openClient(command)
	if err != nil {
		return err
	}
	spaceSubstring, _ := command.Flags().GetString("space")
	indexEverything, _ := command.Flags().GetBool("all")
	if indexEverything {
		return attachmentsIndexAll(command.Context(), apiClient, store)
	}

	state, err := store.LoadState()
	if err != nil {
		return err
	}
	pageIds := make([]string, 0, len(state.Pages))
	for pageId, page := range state.Pages {
		if spaceSubstring != "" && !strings.Contains(page.SpaceKey, spaceSubstring) {
			continue
		}
		pageIds = append(pageIds, pageId)
	}
	sort.Strings(pageIds)
	if len(pageIds) == 0 {
		return fmt.Errorf("commands: no archived page matches, run cf archive sync first")
	}
	printer.PrintInfo("asking %d pages what they hold", len(pageIds))

	ctx := context.Background()
	recorded, done := 0, 0
	for _, pageId := range pageIds {
		page := state.Pages[pageId]
		attachments, err := confluence.AttachmentsOn(ctx, apiClient, pageId)
		if err != nil {
			return err
		}
		records := make([]*archive.Attachment, 0, len(attachments))
		for _, attachment := range attachments {
			records = append(records, &archive.Attachment{
				ID:          attachment.ID,
				PageID:      pageId,
				SpaceKey:    page.SpaceKey,
				Title:       attachment.Title,
				MediaType:   attachment.MediaType,
				FileSize:    attachment.FileSize,
				DownloadURL: attachment.DownloadURL,
			})
		}
		if err := store.AppendAttachments(records); err != nil {
			return err
		}
		recorded += len(records)
		done++
		if done%500 == 0 {
			printer.PrintInfo("  %d/%d pages, %d attachments recorded", done, len(pageIds), recorded)
		}
	}
	printer.PrintSuccess("%d attachments recorded in %s", recorded, store.AttachmentsPath())
	return nil
}

// attachmentsIndexAll walks the whole site's attachments in one pass. Asking
// each page what it holds is one request per page, which for a large site is
// a hundred thousand requests against a few thousand for this.
func attachmentsIndexAll(ctx context.Context, apiClient *client.Client, store *archive.Store) error {
	if ctx == nil {
		ctx = context.Background()
	}
	state, err := store.LoadState()
	if err != nil {
		return err
	}
	// The space a page belongs to is already known, so the index can say
	// which space an attachment is in without asking again.
	spaceOf := make(map[string]string, len(state.Pages)+len(state.Blogposts))
	for pageId, page := range state.Pages {
		spaceOf[pageId] = page.SpaceKey
	}
	for pageId, page := range state.Blogposts {
		spaceOf[pageId] = page.SpaceKey
	}

	seen, recorded := 0, 0
	err = confluence.AllAttachments(ctx, apiClient, func(attachments []*confluence.Attachment) (bool, error) {
		records := make([]*archive.Attachment, 0, len(attachments))
		for _, attachment := range attachments {
			seen++
			records = append(records, &archive.Attachment{
				ID:          attachment.ID,
				PageID:      attachment.PageID,
				SpaceKey:    spaceOf[attachment.PageID],
				Title:       attachment.Title,
				MediaType:   attachment.MediaType,
				FileSize:    attachment.FileSize,
				DownloadURL: attachment.DownloadURL,
			})
		}
		if err := store.AppendAttachments(records); err != nil {
			return false, err
		}
		recorded += len(records)
		if seen%50000 == 0 {
			printer.PrintInfo("  %d attachments recorded", seen)
		}
		return true, nil
	})
	if err != nil {
		return err
	}
	printer.PrintSuccess("%d attachments recorded in %s", recorded, store.AttachmentsPath())
	return nil
}

func attachmentsDownloadRun(command *cobra.Command, arguments []string) error {
	store, err := archive.Open(arguments[0])
	if err != nil {
		return err
	}
	apiClient, _, err := openClient(command)
	if err != nil {
		return err
	}
	spaceSubstring, _ := command.Flags().GetString("space")
	mediaType, _ := command.Flags().GetString("type")
	maximumMegabytes, _ := command.Flags().GetFloat64("max-mb")
	limit, _ := command.Flags().GetInt("limit")
	isDryRun, _ := command.Flags().GetBool("dry-run")

	wanted, err := readAttachmentIndex(store, func(record *archive.Attachment) bool {
		if spaceSubstring != "" && !strings.Contains(record.SpaceKey, spaceSubstring) {
			return false
		}
		if mediaType != "" && !strings.HasPrefix(record.MediaType, mediaType) {
			return false
		}
		if maximumMegabytes > 0 && float64(record.FileSize) > maximumMegabytes*1024*1024 {
			return false
		}
		return true
	})
	if err != nil {
		return err
	}
	if len(wanted) == 0 {
		return fmt.Errorf("commands: nothing in the index matches, run cf archive attachments index first")
	}

	var totalBytes int64
	for _, record := range wanted {
		totalBytes += record.FileSize
	}
	printer.PrintInfo("%d attachments match, %.2f GB", len(wanted), float64(totalBytes)/1e9)
	if isDryRun {
		return nil
	}
	if limit > 0 && len(wanted) > limit {
		wanted = wanted[:limit]
		printer.PrintInfo("stopping after %d", limit)
	}

	have, err := store.HaveAttachments()
	if err != nil {
		return err
	}

	ctx := context.Background()
	saved, skipped, unavailable := 0, 0, 0
	var savedBytes int64
	for _, record := range wanted {
		if _, isHere := have[archive.SafeName(record.ID)]; isHere {
			skipped++
			continue
		}
		content, err := apiClient.GetRaw(ctx, downloadPath(record.DownloadURL))
		if err != nil {
			unavailable++
			continue
		}
		if err := store.WriteAttachment(record.ID, record.Title, content); err != nil {
			return err
		}
		saved++
		savedBytes += int64(len(content))
		if saved%200 == 0 {
			printer.PrintInfo("  %d saved, %.2f GB", saved, float64(savedBytes)/1e9)
		}
	}
	printer.PrintSuccess("%d attachments saved (%.2f GB), %d already here, %d unavailable",
		saved, float64(savedBytes)/1e9, skipped, unavailable)
	return nil
}

// readAttachmentIndex reads the records a filter keeps, without holding the
// whole index in memory when the filter throws most of it away.
func readAttachmentIndex(store *archive.Store, keep func(*archive.Attachment) bool) ([]*archive.Attachment, error) {
	file, err := os.Open(store.AttachmentsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("commands: no attachment index yet, run cf archive attachments index first")
		}
		return nil, fmt.Errorf("commands: opening %s: %w", store.AttachmentsPath(), err)
	}
	defer func() { _ = file.Close() }()

	seen := map[string]struct{}{}
	var wanted []*archive.Attachment
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		record := &archive.Attachment{}
		if json.Unmarshal(line, record) != nil || record.ID == "" {
			continue
		}
		// The index is appended to, so a re-index repeats records.
		if _, isSeen := seen[record.ID]; isSeen {
			continue
		}
		seen[record.ID] = struct{}{}
		if keep(record) {
			wanted = append(wanted, record)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("commands: reading %s: %w", store.AttachmentsPath(), err)
	}
	return wanted, nil
}

// downloadPath fixes the link the API hands back. It is rooted at the site
// rather than at the wiki, so fetching it as given is a 401 rather than a
// file.
func downloadPath(link string) string {
	if strings.HasPrefix(link, "http") || strings.HasPrefix(link, "/wiki/") {
		return link
	}
	return "/wiki" + link
}
