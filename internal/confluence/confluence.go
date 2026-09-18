// Package confluence is the shape of the Confluence Cloud v2 API, and the
// walks over it that an archive needs.
//
// Every listing is read by cursor rather than by offset. An offset walk over a
// site this size gets slower the deeper it goes, and a page written while the
// walk is in progress shifts the window and is missed.
package confluence

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/ziyan/cf/internal/client"
)

// PageSize is how many records to ask for at once. The API caps it at 250,
// and a page of 250 with bodies comes back in about four seconds, which is
// what makes a whole site reachable in a few hundred requests.
const PageSize = 250

// Space is one Confluence space.
type Space struct {
	ID       string `json:"id"`
	Key      string `json:"key"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Status   string `json:"status"`
	HomeID   string `json:"homepageId"`
	AuthorID string `json:"authorId"`
}

// Version says which revision a page or comment is at, and when it was made.
type Version struct {
	Number    int    `json:"number"`
	CreatedAt string `json:"createdAt"`
	AuthorID  string `json:"authorId"`
	Message   string `json:"message"`
}

// Body carries one representation of the content.
type Body struct {
	Storage struct {
		Value          string `json:"value"`
		Representation string `json:"representation"`
	} `json:"storage"`
}

// Page is one Confluence page.
type Page struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	SpaceID  string   `json:"spaceId"`
	ParentID string   `json:"parentId"`
	Status   string   `json:"status"`
	AuthorID string   `json:"authorId"`
	Created  string   `json:"createdAt"`
	Version  *Version `json:"version"`
	Body     *Body    `json:"body"`
}

// ModifiedAt is when this revision was made, which is what a sync compares
// against its high-water mark.
func (self *Page) ModifiedAt() string {
	if self.Version == nil {
		return self.Created
	}
	return self.Version.CreatedAt
}

// CommentKind says whether a comment hangs off the end of a page or off a
// piece of its text.
type CommentKind string

const (
	FooterComment CommentKind = "footer"
	InlineComment CommentKind = "inline"
)

// Blogpost is one Confluence blog post. It carries the same shape as a page
// and is walked the same way, but lives behind its own endpoint: a walk over
// pages never sees one.
type Blogpost struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	SpaceID  string   `json:"spaceId"`
	Status   string   `json:"status"`
	AuthorID string   `json:"authorId"`
	Created  string   `json:"createdAt"`
	Version  *Version `json:"version"`
	Body     *Body    `json:"body"`
}

// ModifiedAt is when this revision was made.
func (self *Blogpost) ModifiedAt() string {
	if self.Version == nil {
		return self.Created
	}
	return self.Version.CreatedAt
}

// Label is one label, as the site spells it.
type Label struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Prefix string `json:"prefix"`
}

// Comment is one footer or inline comment on a page.
type Comment struct {
	Kind CommentKind `json:"-"`

	ID     string `json:"id"`
	PageID string `json:"pageId"`
	// BlogPostID is set instead of PageID when the comment is on a blog post.
	// A reader that looks only at PageID drops those on the floor.
	BlogPostID       string   `json:"blogPostId"`
	Title            string   `json:"title"`
	Status           string   `json:"status"`
	ResolutionStatus string   `json:"resolutionStatus"`
	Version          *Version `json:"version"`
	Body             *Body    `json:"body"`
}

// ParentID is the page or blog post this comment belongs to.
func (self *Comment) ParentID() string {
	if self.PageID != "" {
		return self.PageID
	}
	return self.BlogPostID
}

// ModifiedAt is when this revision of the comment was made.
func (self *Comment) ModifiedAt() string {
	if self.Version == nil {
		return ""
	}
	return self.Version.CreatedAt
}

// Attachment is a file on a page. An archive records these without fetching
// them, so a later run can fetch the ones somebody asks for.
type Attachment struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	PageID      string   `json:"pageId"`
	MediaType   string   `json:"mediaType"`
	FileSize    int64    `json:"fileSize"`
	DownloadURL string   `json:"downloadLink"`
	Version     *Version `json:"version"`
}

// listing is the envelope every v2 listing comes back in.
type listing struct {
	Results json.RawMessage `json:"results"`
	Links   struct {
		Next string `json:"next"`
	} `json:"_links"`
}

// Walk reads a listing page by page, calling visit with the raw results of
// each. Returning false stops the walk, which is how a sync stops once it
// reaches what it already has.
func Walk(ctx context.Context, apiClient *client.Client, path string, visit func(results json.RawMessage) (bool, error)) error {
	next := path
	for next != "" {
		body, err := apiClient.GetRaw(ctx, next)
		if err != nil {
			return err
		}
		batch := &listing{}
		if err := json.Unmarshal(body, batch); err != nil {
			return fmt.Errorf("confluence: parsing a listing from %s: %w", next, err)
		}
		shouldContinue, err := visit(batch.Results)
		if err != nil {
			return err
		}
		if !shouldContinue {
			return nil
		}
		next = batch.Links.Next
	}
	return nil
}

// Spaces reads every space on the site.
func Spaces(ctx context.Context, apiClient *client.Client) ([]*Space, error) {
	var spaces []*Space
	path := client.Query("/wiki/api/v2/spaces", map[string]string{"limit": fmt.Sprint(PageSize)})
	err := Walk(ctx, apiClient, path, func(results json.RawMessage) (bool, error) {
		var batch []*Space
		if err := json.Unmarshal(results, &batch); err != nil {
			return false, fmt.Errorf("confluence: parsing spaces: %w", err)
		}
		spaces = append(spaces, batch...)
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	return spaces, nil
}

// PagesNewestFirst walks pages, newest modification first, with the storage
// body included. visit stops the walk by returning false, which is how an
// incremental sync reads only what changed since its mark.
//
// A spaceId narrows the walk to one space at the server rather than here. The
// difference is the whole site: filtering after the fact would read all of it
// to find the few pages asked for.
func PagesNewestFirst(ctx context.Context, apiClient *client.Client, spaceId string, visit func(pages []*Page) (bool, error)) error {
	path := client.Query("/wiki/api/v2/pages", map[string]string{
		"limit":       fmt.Sprint(PageSize),
		"sort":        "-modified-date",
		"body-format": "storage",
		"status":      "current",
		"space-id":    spaceId,
	})
	return Walk(ctx, apiClient, path, func(results json.RawMessage) (bool, error) {
		var batch []*Page
		if err := json.Unmarshal(results, &batch); err != nil {
			return false, fmt.Errorf("confluence: parsing pages: %w", err)
		}
		return visit(batch)
	})
}

// AllComments walks every comment on the site, of both kinds, with its body.
//
// Inline comments live behind their own endpoint. A site is perfectly capable
// of holding more of them than footer comments, so reading only one endpoint
// quietly loses most of the conversation.
//
// Deliberately unsorted. Asking this endpoint for sort=-modified-date makes it
// stop after about a thousand records and report itself finished: it can end at a fraction of them and
// say it is finished. A walk that says it is done when it is not is worse
// than a slow one, so the whole set is read and
// the ordering is nobody's business.
func AllComments(ctx context.Context, apiClient *client.Client, visit func(comments []*Comment) (bool, error)) error {
	for _, kind := range []CommentKind{FooterComment, InlineComment} {
		path := client.Query(fmt.Sprintf("/wiki/api/v2/%s-comments", kind), map[string]string{
			"limit":       fmt.Sprint(PageSize),
			"body-format": "storage",
		})
		err := Walk(ctx, apiClient, path, func(results json.RawMessage) (bool, error) {
			var batch []*Comment
			if err := json.Unmarshal(results, &batch); err != nil {
				return false, fmt.Errorf("confluence: parsing %s comments: %w", kind, err)
			}
			for _, comment := range batch {
				comment.Kind = kind
			}
			return visit(batch)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// CommentsOn reads every comment on one page or blog post, of both kinds.
func CommentsOn(ctx context.Context, apiClient *client.Client, contentId, contentType string) ([]*Comment, error) {
	var comments []*Comment
	for _, kind := range []CommentKind{FooterComment, InlineComment} {
		path := client.Query(fmt.Sprintf("/wiki/api/v2/%s/%s/%s-comments", contentType, contentId, kind), map[string]string{
			"limit":       fmt.Sprint(PageSize),
			"body-format": "storage",
		})
		err := Walk(ctx, apiClient, path, func(results json.RawMessage) (bool, error) {
			var batch []*Comment
			if err := json.Unmarshal(results, &batch); err != nil {
				return false, fmt.Errorf("confluence: parsing %s comments on %s: %w", kind, contentId, err)
			}
			for _, comment := range batch {
				comment.Kind = kind
			}
			comments = append(comments, batch...)
			return true, nil
		})
		if err != nil {
			return nil, err
		}
	}
	return comments, nil
}

// PagesWithChangedComments asks which pages have had a comment written or
// edited since a moment, which is what makes a later sync cheap. Writing a
// comment does not change the page it is on, so this is the only way to find
// a conversation that happened after the last edit.
func PagesWithChangedComments(ctx context.Context, apiClient *client.Client, since string) ([]string, error) {
	when, err := cqlTime(since)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	var pageIds []string
	start := 0
	for {
		path := client.Query("/wiki/rest/api/search", map[string]string{
			"cql":    fmt.Sprintf(`type=comment and lastmodified >= "%s"`, when),
			"expand": "content.container",
			"limit":  "100",
			"start":  fmt.Sprint(start),
		})
		response := struct {
			Results []struct {
				Content struct {
					Container struct {
						ID string `json:"id"`
					} `json:"container"`
				} `json:"content"`
			} `json:"results"`
			Size int `json:"size"`
		}{}
		if err := apiClient.Get(ctx, path, &response); err != nil {
			return nil, err
		}
		for _, result := range response.Results {
			id := result.Content.Container.ID
			if id == "" {
				continue
			}
			if _, isSeen := seen[id]; isSeen {
				continue
			}
			seen[id] = struct{}{}
			pageIds = append(pageIds, id)
		}
		if response.Size < 100 {
			return pageIds, nil
		}
		start += response.Size
	}
}

// AttachmentsOn reads what is attached to one page.
func AttachmentsOn(ctx context.Context, apiClient *client.Client, pageId string) ([]*Attachment, error) {
	var attachments []*Attachment
	path := client.Query(fmt.Sprintf("/wiki/api/v2/pages/%s/attachments", pageId), map[string]string{
		"limit": fmt.Sprint(PageSize),
	})
	err := Walk(ctx, apiClient, path, func(results json.RawMessage) (bool, error) {
		var batch []*Attachment
		if err := json.Unmarshal(results, &batch); err != nil {
			return false, fmt.Errorf("confluence: parsing attachments on %s: %w", pageId, err)
		}
		attachments = append(attachments, batch...)
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	return attachments, nil
}

// userIdentifiers finds the people a page's storage refers to. Confluence has
// spelled the attribute three ways over the years.
var userIdentifiers = regexp.MustCompile(`ri:(?:account-id|userkey|username)="([^"]+)"`)

// MentionedUsers is every account this storage refers to.
func MentionedUsers(storage string) []string {
	var found []string
	seen := map[string]struct{}{}
	for _, match := range userIdentifiers.FindAllStringSubmatch(storage, -1) {
		if _, isSeen := seen[match[1]]; isSeen {
			continue
		}
		seen[match[1]] = struct{}{}
		found = append(found, match[1])
	}
	return found
}

// ResolveUser asks who an account id belongs to. An account that no longer
// exists is not an error: the archive keeps the identifier and moves on.
func ResolveUser(ctx context.Context, apiClient *client.Client, accountId string) string {
	response := struct {
		DisplayName string `json:"displayName"`
		Username    string `json:"username"`
	}{}
	path := client.Query("/wiki/rest/api/user", map[string]string{"accountId": accountId})
	if err := apiClient.Get(ctx, path, &response); err != nil {
		return ""
	}
	if response.DisplayName != "" {
		return response.DisplayName
	}
	return response.Username
}

// BlogpostsNewestFirst walks blog posts, newest modification first, with the
// storage body included.
func BlogpostsNewestFirst(ctx context.Context, apiClient *client.Client, visit func(posts []*Blogpost) (bool, error)) error {
	path := client.Query("/wiki/api/v2/blogposts", map[string]string{
		"limit":       fmt.Sprint(PageSize),
		"sort":        "-modified-date",
		"body-format": "storage",
		"status":      "current",
	})
	return Walk(ctx, apiClient, path, func(results json.RawMessage) (bool, error) {
		var batch []*Blogpost
		if err := json.Unmarshal(results, &batch); err != nil {
			return false, fmt.Errorf("confluence: parsing blog posts: %w", err)
		}
		return visit(batch)
	})
}

// PageIdentifiers reads the id and version of every page, without the bodies.
// It is what tells an archive which of its pages the site no longer has.
func PageIdentifiers(ctx context.Context, apiClient *client.Client, visit func(pages []*Page) error) error {
	path := client.Query("/wiki/api/v2/pages", map[string]string{
		"limit":  fmt.Sprint(PageSize),
		"status": "current",
	})
	return Walk(ctx, apiClient, path, func(results json.RawMessage) (bool, error) {
		var batch []*Page
		if err := json.Unmarshal(results, &batch); err != nil {
			return false, fmt.Errorf("confluence: parsing page identifiers: %w", err)
		}
		return true, visit(batch)
	})
}

// Labels reads every label on the site.
func Labels(ctx context.Context, apiClient *client.Client) ([]*Label, error) {
	var labels []*Label
	path := client.Query("/wiki/api/v2/labels", map[string]string{"limit": fmt.Sprint(PageSize)})
	err := Walk(ctx, apiClient, path, func(results json.RawMessage) (bool, error) {
		var batch []*Label
		if err := json.Unmarshal(results, &batch); err != nil {
			return false, fmt.Errorf("confluence: parsing labels: %w", err)
		}
		labels = append(labels, batch...)
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	return labels, nil
}

// PagesWithLabel reads the ids of the pages carrying one label.
//
// The mapping is read this way round because the other way round is one
// request per page, and there are far fewer labels than pages.
func PagesWithLabel(ctx context.Context, apiClient *client.Client, labelId string) ([]string, error) {
	var pageIds []string
	path := client.Query(fmt.Sprintf("/wiki/api/v2/labels/%s/pages", labelId), map[string]string{
		"limit": fmt.Sprint(PageSize),
	})
	err := Walk(ctx, apiClient, path, func(results json.RawMessage) (bool, error) {
		var batch []*Page
		if err := json.Unmarshal(results, &batch); err != nil {
			return false, fmt.Errorf("confluence: parsing pages for label %s: %w", labelId, err)
		}
		for _, page := range batch {
			pageIds = append(pageIds, page.ID)
		}
		return true, nil
	})
	if err != nil {
		return nil, err
	}
	return pageIds, nil
}

// cqlSafetyMargin is how far back a CQL query reaches beyond the mark.
//
// CQL takes a date to the minute and reads it in the site's timezone, which is
// not ours to know. A margin wider than any offset on earth is what stops a
// comment being missed because the two ends disagreed about what hour it was.
// Reading a few comments twice costs nothing: a page's comments are rewritten
// whole.
const cqlSafetyMargin = 26 * time.Hour

// cqlTime renders a moment the way CQL will accept it. A full timestamp is
// rejected outright: "Could not parse cql".
func cqlTime(moment string) (string, error) {
	parsed, err := time.Parse(time.RFC3339, moment)
	if err != nil {
		return "", fmt.Errorf("confluence: reading %q as a time: %w", moment, err)
	}
	return parsed.Add(-cqlSafetyMargin).Format("2006-01-02 15:04"), nil
}
