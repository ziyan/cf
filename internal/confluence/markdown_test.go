package confluence

import (
	"strings"
	"testing"
)

func convert(t *testing.T, storage string) string {
	t.Helper()
	markdown, err := ToMarkdown(storage)
	if err != nil {
		t.Fatalf("converting %q: %v", storage, err)
	}
	return markdown
}

func TestToMarkdownBasics(t *testing.T) {
	cases := []struct {
		name     string
		storage  string
		expected string
	}{
		{"heading", "<h2>Title</h2>", "## Title"},
		{"paragraph", "<p>Hello there</p>", "Hello there"},
		{"bold", "<p><strong>loud</strong></p>", "**loud**"},
		{"italic", "<p><em>soft</em></p>", "*soft*"},
		{"code span", "<p><code>value</code></p>", "`value`"},
		{"link", `<p><a href="https://example.com">site</a></p>`, "[site](https://example.com)"},
		{"bullet", "<ul><li>one</li><li>two</li></ul>", "- one"},
		{"numbered", "<ol><li>first</li><li>second</li></ol>", "1. first"},
		{"rule", "<hr/>", "---"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if actual := convert(t, testCase.storage); !strings.Contains(actual, testCase.expected) {
				t.Errorf("expected %q in:\n%s", testCase.expected, actual)
			}
		})
	}
}

// A Confluence space is mostly tables, and a flattened one is unreadable, so
// this is the shape most worth keeping.
func TestToMarkdownTable(t *testing.T) {
	storage := `<table><tbody>
		<tr><th>Device</th><th>Result</th></tr>
		<tr><td>Estop</td><td>Pass</td></tr>
		<tr><td>Scanner</td><td>Fail</td></tr>
	</tbody></table>`
	actual := convert(t, storage)
	for _, expected := range []string{"| Device | Result |", "| --- | --- |", "| Estop | Pass |", "| Scanner | Fail |"} {
		if !strings.Contains(actual, expected) {
			t.Errorf("expected %q in:\n%s", expected, actual)
		}
	}
}

// A cell cannot hold a bar or a line break in this notation, and a table whose
// rows have different lengths still has to line up.
func TestToMarkdownTableEscapesAndPads(t *testing.T) {
	storage := `<table><tbody>
		<tr><th>a</th><th>b</th><th>c</th></tr>
		<tr><td>x | y</td><td>two<br/>lines</td></tr>
	</tbody></table>`
	actual := convert(t, storage)
	if !strings.Contains(actual, `x \| y`) {
		t.Errorf("a bar in a cell must be escaped:\n%s", actual)
	}
	if strings.Contains(actual, "two\nlines") {
		t.Errorf("a break inside a cell must not end the row:\n%s", actual)
	}
	for _, line := range strings.Split(strings.TrimSpace(actual), "\n") {
		if strings.HasPrefix(line, "|") && strings.Count(strings.ReplaceAll(line, "\\|", ""), "|") != 4 {
			t.Errorf("every row needs the same number of cells, got %q", line)
		}
	}
}

func TestToMarkdownCodeMacro(t *testing.T) {
	storage := `<ac:structured-macro ac:name="code">
		<ac:parameter ac:name="language">go</ac:parameter>
		<ac:plain-text-body><![CDATA[fmt.Println("hi")]]></ac:plain-text-body>
	</ac:structured-macro>`
	actual := convert(t, storage)
	if !strings.Contains(actual, "```go") {
		t.Errorf("expected a fenced block tagged go:\n%s", actual)
	}
	if !strings.Contains(actual, `fmt.Println("hi")`) {
		t.Errorf("the code itself must survive:\n%s", actual)
	}
}

func TestToMarkdownPanelMacros(t *testing.T) {
	storage := `<ac:structured-macro ac:name="warning"><ac:rich-text-body><p>Mind the gap</p></ac:rich-text-body></ac:structured-macro>`
	actual := convert(t, storage)
	if !strings.Contains(actual, "> **Warning**") || !strings.Contains(actual, "> Mind the gap") {
		t.Errorf("a warning should become a quoted block:\n%s", actual)
	}
}

// An unknown macro still has text in it worth keeping, and its name says what
// was there. Dropping either would lose the page's meaning quietly.
func TestToMarkdownUnknownMacroKeepsItsText(t *testing.T) {
	storage := `<ac:structured-macro ac:name="whatever"><ac:rich-text-body><p>still words</p></ac:rich-text-body></ac:structured-macro>`
	actual := convert(t, storage)
	if !strings.Contains(actual, "still words") {
		t.Errorf("the text inside an unknown macro must be kept:\n%s", actual)
	}
	if !strings.Contains(actual, "whatever") {
		t.Errorf("the macro name says what was there:\n%s", actual)
	}
}

func TestToMarkdownTasks(t *testing.T) {
	storage := `<ac:task-list>
		<ac:task><ac:task-status>complete</ac:task-status><ac:task-body>done thing</ac:task-body></ac:task>
		<ac:task><ac:task-status>incomplete</ac:task-status><ac:task-body>open thing</ac:task-body></ac:task>
	</ac:task-list>`
	actual := convert(t, storage)
	if !strings.Contains(actual, "- [x] done thing") {
		t.Errorf("a finished task should be ticked:\n%s", actual)
	}
	if !strings.Contains(actual, "- [ ] open thing") {
		t.Errorf("an open task should not be:\n%s", actual)
	}
}

// A link to another page names its target rather than addressing it, so the
// name has to be kept or the link means nothing offline.
func TestToMarkdownPageLink(t *testing.T) {
	storage := `<ac:link><ri:page ri:content-title="Other Page" ri:space-key="ENG"/><ac:plain-text-link-body><![CDATA[see this]]></ac:plain-text-link-body></ac:link>`
	actual := convert(t, storage)
	if !strings.Contains(actual, "see this") || !strings.Contains(actual, "ENG/Other Page") {
		t.Errorf("expected the label and the target:\n%s", actual)
	}
}

func TestToMarkdownAttachmentImage(t *testing.T) {
	storage := `<ac:image><ri:attachment ri:filename="diagram.png"/></ac:image>`
	actual := convert(t, storage)
	if !strings.Contains(actual, "attachment:diagram.png") {
		t.Errorf("an image should point at the attachment it names:\n%s", actual)
	}
}

func TestToMarkdownCollapsesWhitespace(t *testing.T) {
	// Storage is indented XHTML, so runs of whitespace carry no meaning.
	storage := "<p>one\n\n   two\t\tthree</p>"
	actual := strings.TrimSpace(convert(t, storage))
	if actual != "one two three" {
		t.Errorf("expected the words on one line, got %q", actual)
	}
}

func TestToMarkdownEmptyAndBroken(t *testing.T) {
	if actual := strings.TrimSpace(convert(t, "")); actual != "" {
		t.Errorf("empty storage should convert to nothing, got %q", actual)
	}
	// Storage that is not well formed still has to come back with its text.
	if actual := convert(t, "<p>unclosed <strong>bold"); !strings.Contains(actual, "unclosed") {
		t.Errorf("broken markup should still yield its text:\n%s", actual)
	}
}

func TestCqlTimeIsAcceptableAndReachesBack(t *testing.T) {
	// A full timestamp is what the API hands back and what CQL refuses.
	when, err := cqlTime("2026-09-17T14:10:10.456Z")
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(when, "TZ.") {
		t.Errorf("CQL will not parse %q", when)
	}
	// The margin has to cover any timezone the site might be keeping.
	if when >= "2026-09-17 14:10" {
		t.Errorf("expected a margin before the mark, got %q", when)
	}
	if when < "2026-09-16 00:00" {
		t.Errorf("the margin is wider than it needs to be, got %q", when)
	}
	if _, err := cqlTime("not a time"); err == nil {
		t.Error("an unparseable moment should be an error, not a query")
	}
}
