package confluence

import (
	"fmt"
	"strings"

	"golang.org/x/net/html"
)

// ToMarkdown turns a page's storage format into markdown.
//
// Storage is XHTML with Confluence's own elements mixed in: macros, links to
// pages by title, task lists. The markdown is for reading and grepping, and it
// is lossy by nature, which is why the archive keeps the storage beside it.
// Where a macro has no markdown equivalent this keeps the text inside it and
// says what the macro was, rather than dropping either.
func ToMarkdown(storage string) (string, error) {
	return ToMarkdownWithUsers(storage, nil)
}

// ToMarkdownWithUsers is the same, with a map from account id to name so a
// mention reads as the person rather than as an identifier.
func ToMarkdownWithUsers(storage string, users map[string]string) (string, error) {
	document, err := html.Parse(strings.NewReader(storage))
	if err != nil {
		return "", fmt.Errorf("confluence: parsing storage: %w", err)
	}
	writer := &markdownWriter{users: users}
	writer.walk(document, 0)
	return writer.finish(), nil
}

type markdownWriter struct {
	out   strings.Builder
	users map[string]string
	// listDepth is how many lists deep we are, for indenting nested items.
	listDepth int
	// inPre suppresses the escaping and collapsing done to ordinary text.
	inPre bool
}

func (self *markdownWriter) finish() string {
	text := self.out.String()
	// At most one blank line between blocks, and none at either end.
	for strings.Contains(text, "\n\n\n") {
		text = strings.ReplaceAll(text, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(text) + "\n"
}

func (self *markdownWriter) write(text string) {
	self.out.WriteString(text)
}

// block ends the current line and leaves a blank one, which is what separates
// paragraphs, headings and tables in markdown.
func (self *markdownWriter) block() {
	text := self.out.String()
	if text == "" {
		return
	}
	if !strings.HasSuffix(text, "\n\n") {
		if !strings.HasSuffix(text, "\n") {
			self.write("\n")
		}
		self.write("\n")
	}
}

func (self *markdownWriter) walk(node *html.Node, depth int) {
	if node.Type == html.TextNode {
		self.writeText(node.Data)
		return
	}
	if node.Type == html.CommentNode {
		// CDATA reaches here as a comment. Its text is content, not markup.
		if inner, isCdata := cdataOf(node.Data); isCdata {
			self.writeText(inner)
		}
		return
	}
	if node.Type != html.ElementNode {
		self.walkChildren(node, depth)
		return
	}

	switch strings.ToLower(node.Data) {
	case "h1", "h2", "h3", "h4", "h5", "h6":
		level := int(node.Data[1] - '0')
		self.block()
		self.write(strings.Repeat("#", level) + " ")
		self.walkChildren(node, depth)
		self.block()
	case "p", "div":
		self.block()
		self.walkChildren(node, depth)
		self.block()
	case "br":
		self.write("\n")
	case "hr":
		self.block()
		self.write("---")
		self.block()
	case "strong", "b":
		self.wrap("**", node, depth)
	case "em", "i":
		self.wrap("*", node, depth)
	case "s", "del", "strike":
		self.wrap("~~", node, depth)
	case "code":
		self.wrap("`", node, depth)
	case "pre":
		self.writeCodeBlock(textOf(node), "")
	case "a":
		self.writeLink(node, depth)
	case "ul", "ol":
		self.block()
		self.listDepth++
		self.walkChildren(node, depth)
		self.listDepth--
		self.block()
	case "li":
		self.writeListItem(node, depth)
	case "table":
		self.writeTable(node, depth)
	case "ac:structured-macro":
		self.writeMacro(node, depth)
	case "ac:link":
		self.writeConfluenceLink(node, depth)
	case "ac:image":
		self.writeImage(node)
	case "ac:task-list":
		self.block()
		self.walkChildren(node, depth)
		self.block()
	case "ac:task":
		self.writeTask(node, depth)
	case "ac:plain-text-body", "ac:rich-text-body", "ac:layout", "ac:layout-section", "ac:layout-cell":
		self.walkChildren(node, depth)
	case "ac:parameter", "ri:page", "ri:user", "ri:attachment", "ri:url", "ac:task-id", "ac:task-status":
		// Read by whoever needed them, not printed on their own.
	case "time":
		self.write(attribute(node, "datetime"))
	case "head", "script", "style":
		// Nothing a reader wants.
	default:
		self.walkChildren(node, depth)
	}
}

func (self *markdownWriter) walkChildren(node *html.Node, depth int) {
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		self.walk(child, depth+1)
	}
}

func (self *markdownWriter) wrap(marker string, node *html.Node, depth int) {
	inner := &markdownWriter{listDepth: self.listDepth, inPre: self.inPre, users: self.users}
	inner.walkChildren(node, depth)
	text := strings.TrimSpace(inner.out.String())
	if text == "" {
		return
	}
	self.write(marker + text + marker)
}

func (self *markdownWriter) writeText(text string) {
	if self.inPre {
		self.write(text)
		return
	}
	// Storage format is indented XHTML, so runs of whitespace carry no
	// meaning and would otherwise become stray blank lines.
	collapsed := strings.Join(strings.Fields(text), " ")
	if collapsed == "" {
		if strings.ContainsAny(text, " \t") && !strings.HasSuffix(self.out.String(), " ") {
			// One space where the markup had any, so words do not run together.
			if self.out.Len() > 0 && !strings.HasSuffix(self.out.String(), "\n") {
				self.write(" ")
			}
		}
		return
	}
	if strings.HasPrefix(text, " ") || strings.HasPrefix(text, "\n") {
		if self.out.Len() > 0 && !strings.HasSuffix(self.out.String(), " ") && !strings.HasSuffix(self.out.String(), "\n") {
			self.write(" ")
		}
	}
	self.write(collapsed)
	if strings.HasSuffix(text, " ") {
		self.write(" ")
	}
}

func (self *markdownWriter) writeCodeBlock(code, language string) {
	self.block()
	self.write("```" + language + "\n")
	self.write(strings.TrimRight(code, "\n"))
	self.write("\n```")
	self.block()
}

func (self *markdownWriter) writeLink(node *html.Node, depth int) {
	href := attribute(node, "href")
	inner := &markdownWriter{users: self.users}
	inner.walkChildren(node, depth)
	text := strings.TrimSpace(inner.out.String())
	switch {
	case href == "":
		self.write(text)
	case text == "":
		self.write("<" + href + ">")
	default:
		self.write("[" + text + "](" + href + ")")
	}
}

func (self *markdownWriter) writeListItem(node *html.Node, depth int) {
	indent := strings.Repeat("  ", maximum(self.listDepth-1, 0))
	marker := "- "
	if parent := node.Parent; parent != nil && strings.EqualFold(parent.Data, "ol") {
		marker = fmt.Sprintf("%d. ", positionAmongItems(node))
	}
	inner := &markdownWriter{listDepth: self.listDepth, users: self.users}
	inner.walkChildren(node, depth)
	text := strings.TrimSpace(inner.out.String())
	if text == "" {
		return
	}
	// A nested list inside the item keeps its own lines, indented under it.
	lines := strings.Split(text, "\n")
	self.write("\n" + indent + marker + lines[0])
	for _, line := range lines[1:] {
		self.write("\n" + indent + "  " + strings.TrimSpace(line))
	}
}

func (self *markdownWriter) writeTask(node *html.Node, depth int) {
	status := strings.TrimSpace(textOfChild(node, "ac:task-status"))
	box := "[ ]"
	if strings.EqualFold(status, "complete") {
		box = "[x]"
	}
	inner := &markdownWriter{users: self.users}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode && strings.EqualFold(child.Data, "ac:task-body") {
			inner.walkChildren(child, depth)
		}
	}
	text := strings.TrimSpace(inner.out.String())
	self.write("\n- " + box + " " + text)
}

// writeTable renders a table, which is the shape most worth keeping: a
// Confluence space is full of them and a flattened one is unreadable.
func (self *markdownWriter) writeTable(node *html.Node, depth int) {
	rows := collectRows(node)
	if len(rows) == 0 {
		return
	}
	self.block()

	width := 0
	rendered := make([][]string, 0, len(rows))
	for _, row := range rows {
		cells := make([]string, 0, len(row))
		for _, cell := range row {
			inner := &markdownWriter{users: self.users}
			inner.walkChildren(cell, depth)
			text := strings.TrimSpace(inner.out.String())
			// A cell cannot hold a line break or a bar in this notation.
			text = strings.ReplaceAll(text, "\n", " ")
			text = strings.ReplaceAll(text, "|", "\\|")
			cells = append(cells, strings.Join(strings.Fields(text), " "))
		}
		if len(cells) > width {
			width = len(cells)
		}
		rendered = append(rendered, cells)
	}

	for index, cells := range rendered {
		for len(cells) < width {
			cells = append(cells, "")
		}
		self.write("| " + strings.Join(cells, " | ") + " |\n")
		if index == 0 {
			self.write("|" + strings.Repeat(" --- |", width) + "\n")
		}
	}
	self.block()
}

// writeMacro renders the macros that have a markdown equivalent and keeps the
// contents of the ones that do not.
func (self *markdownWriter) writeMacro(node *html.Node, depth int) {
	name := attribute(node, "ac:name")
	switch name {
	case "code":
		language := parameterOf(node, "language")
		self.writeCodeBlock(textOfChild(node, "ac:plain-text-body"), language)
	case "info", "note", "warning", "tip", "panel", "error":
		self.block()
		inner := &markdownWriter{users: self.users}
		inner.walkChildren(node, depth)
		text := strings.TrimSpace(inner.out.String())
		self.write("> **" + strings.ToUpper(name[:1]) + name[1:] + "**")
		for _, line := range strings.Split(text, "\n") {
			self.write("\n> " + strings.TrimSpace(line))
		}
		self.block()
	case "expand":
		title := parameterOf(node, "title")
		if title == "" {
			title = "Details"
		}
		self.block()
		self.write("**" + title + "**")
		self.block()
		self.walkChildren(node, depth)
	case "toc", "children", "pagetree":
		self.block()
		self.write("_(" + name + " macro)_")
		self.block()
	case "status":
		self.write("`" + parameterOf(node, "title") + "`")
	case "jira":
		key := parameterOf(node, "key")
		if key == "" {
			key = parameterOf(node, "jqlQuery")
		}
		self.write("`" + strings.TrimSpace("jira "+key) + "`")
	default:
		// An unknown macro still has text in it worth keeping, and the name
		// says what was there.
		inner := &markdownWriter{users: self.users}
		inner.walkChildren(node, depth)
		text := strings.TrimSpace(inner.out.String())
		if text == "" {
			if name != "" {
				self.write("_(" + name + " macro)_")
			}
			return
		}
		self.block()
		if name != "" {
			self.write("_(" + name + " macro)_\n\n")
		}
		self.write(text)
		self.block()
	}
}

// writeConfluenceLink renders a link to a page, a user or an attachment. The
// storage format names the target rather than addressing it, so this keeps the
// name and says what kind of thing it was.
//
// The parts are looked for anywhere inside the link, not only as its direct
// children: <ri:page/> is not a void element to an HTML parser, so everything
// written after it is parsed as its child.
func (self *markdownWriter) writeConfluenceLink(node *html.Node, depth int) {
	label := ""
	if body := findDescendant(node, "ac:plain-text-link-body"); body != nil {
		label = strings.TrimSpace(textOf(body))
	}
	if label == "" {
		if body := findDescendant(node, "ac:link-body"); body != nil {
			inner := &markdownWriter{users: self.users}
			inner.walkChildren(body, depth)
			label = strings.TrimSpace(inner.out.String())
		}
	}

	if page := findDescendant(node, "ri:page"); page != nil {
		title := attribute(page, "ri:content-title")
		space := attribute(page, "ri:space-key")
		if label == "" {
			label = title
		}
		target := title
		if space != "" {
			target = space + "/" + title
		}
		self.write("[" + label + "](confluence:" + target + ")")
		return
	}
	if user := findDescendant(node, "ri:user"); user != nil {
		name := label
		if name == "" {
			// Confluence has used three spellings for this over the years,
			// and a page written under an older one still mentions somebody.
			for _, key := range []string{"ri:account-id", "ri:userkey", "ri:username"} {
				if value := attribute(user, key); value != "" {
					name = value
					break
				}
			}
		}
		if name == "" {
			return
		}
		if resolved, isKnown := self.users[name]; isKnown && resolved != "" {
			name = resolved
		}
		self.write("@" + name)
		return
	}
	if attached := findDescendant(node, "ri:attachment"); attached != nil {
		name := attribute(attached, "ri:filename")
		if label == "" {
			label = name
		}
		self.write("[" + label + "](attachment:" + name + ")")
		return
	}
	if label != "" {
		self.write(label)
	}
}

// findDescendant is the first element with this tag anywhere below node.
func findDescendant(node *html.Node, tag string) *html.Node {
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode && strings.EqualFold(child.Data, tag) {
			return child
		}
		if found := findDescendant(child, tag); found != nil {
			return found
		}
	}
	return nil
}

func (self *markdownWriter) writeImage(node *html.Node) {
	if attached := findDescendant(node, "ri:attachment"); attached != nil {
		name := attribute(attached, "ri:filename")
		self.write("![" + name + "](attachment:" + name + ")")
		return
	}
	if address := findDescendant(node, "ri:url"); address != nil {
		self.write("![](" + attribute(address, "ri:value") + ")")
	}
}

func attribute(node *html.Node, name string) string {
	for _, candidate := range node.Attr {
		full := candidate.Key
		if candidate.Namespace != "" {
			full = candidate.Namespace + ":" + candidate.Key
		}
		if strings.EqualFold(full, name) || strings.EqualFold(candidate.Key, name) {
			return candidate.Val
		}
	}
	return ""
}

func parameterOf(node *html.Node, name string) string {
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode && strings.EqualFold(child.Data, "ac:parameter") &&
			strings.EqualFold(attribute(child, "ac:name"), name) {
			return strings.TrimSpace(textOf(child))
		}
	}
	return ""
}

// cdataOf unwraps the comment the parser makes of a CDATA section.
//
// The parser is an HTML one, and HTML has no CDATA: it reads <![CDATA[x]]> as
// a comment whose text is "[CDATA[x]]". Confluence puts the body of a code
// macro and the label of a link in exactly that, so a reader that looks only
// at text nodes loses every code block on the site and says nothing about it.
func cdataOf(data string) (string, bool) {
	if !strings.HasPrefix(data, "[CDATA[") {
		return "", false
	}
	return strings.TrimSuffix(strings.TrimPrefix(data, "[CDATA["), "]]"), true
}

func textOf(node *html.Node) string {
	builder := &strings.Builder{}
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			builder.WriteString(current.Data)
		}
		if current.Type == html.CommentNode {
			if inner, isCdata := cdataOf(current.Data); isCdata {
				builder.WriteString(inner)
			}
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return builder.String()
}

func textOfChild(node *html.Node, tag string) string {
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode && strings.EqualFold(child.Data, tag) {
			return textOf(child)
		}
	}
	return ""
}

// collectRows finds the rows of a table, wherever thead and tbody put them.
func collectRows(node *html.Node) [][]*html.Node {
	var rows [][]*html.Node
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			if child.Type != html.ElementNode {
				continue
			}
			switch strings.ToLower(child.Data) {
			case "tr":
				var cells []*html.Node
				for cell := child.FirstChild; cell != nil; cell = cell.NextSibling {
					if cell.Type == html.ElementNode && (strings.EqualFold(cell.Data, "td") || strings.EqualFold(cell.Data, "th")) {
						cells = append(cells, cell)
					}
				}
				if len(cells) > 0 {
					rows = append(rows, cells)
				}
			case "thead", "tbody", "tfoot":
				walk(child)
			}
		}
	}
	walk(node)
	return rows
}

func positionAmongItems(node *html.Node) int {
	position := 1
	for sibling := node.PrevSibling; sibling != nil; sibling = sibling.PrevSibling {
		if sibling.Type == html.ElementNode && strings.EqualFold(sibling.Data, "li") {
			position++
		}
	}
	return position
}

func maximum(first, second int) int {
	if first > second {
		return first
	}
	return second
}
