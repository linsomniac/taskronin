package markdown

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

var renderCases = []struct {
	name, in      string
	want, notWant []string
}{
	{"emphasis", "Hello *world*", []string{"<p>Hello <em>world</em></p>"}, nil},
	{"raw block html omitted", "<script>alert(1)</script>\n\nok", []string{"<p>ok</p>"}, []string{"<script", "alert(1)</script>"}},
	{"raw inline html omitted", `x <img src=x onerror=alert(1)> y`, nil, []string{"<img", "onerror"}},
	{"javascript link", "[c](javascript:alert(1))", nil, []string{"javascript:"}},
	{"data link", "[c](data:text/html,hi)", nil, []string{"data:"}},
	{"https link", "[docs](https://example.com/a)", []string{`href="https://example.com/a"`, `rel="noreferrer noopener"`, `target="_blank"`}, nil},
	{"mailto kept", "[m](mailto:sean@example.com)", []string{`href="mailto:sean@example.com"`}, nil},
	{"relative dropped", "[r](/ui/backlog)", []string{"<p>r</p>"}, []string{`href="/ui/backlog"`}},
	{"image as link", "![diagram](https://example.com/d.png)", []string{`<a href="https://example.com/d.png"`, ">diagram</a>"}, []string{"<img"}},
	{"image javascript", "![x](javascript:alert(1))", nil, []string{"javascript:", "<img"}},
	{"linkify", "see https://example.com/x", []string{`href="https://example.com/x"`}, nil},
	{"strikethrough", "~~old~~", []string{"<del>old</del>"}, nil},
	{"code escaped", "`<b>`", []string{"<code>&lt;b&gt;</code>"}, nil},
	{"list", "1. a\n2. b", []string{"<ol>", "<li>a</li>"}, nil},
	{"harmless text kept", "x onload=y and javascript: as words", []string{"x onload=y and javascript: as words"}, nil},
	// AIDEV-NOTE: DF02 rejects scheme-only and hostless URLs, including URLs
	// browsers could resolve relative to the loopback UI despite their scheme.
	{"https empty host", "[d](https:)", nil, []string{"href"}},
	{"https opaque host", "[d](https:example.com)", nil, []string{"href"}},
	{"http missing host", "[x](http:/ui/backlog)", nil, []string{"href"}},
	{"http empty hostname", "[x](http://:80/x)", nil, []string{"href"}},
	{"https empty hostname", "[x](https://:443/x)", nil, []string{"href"}},
	{"entity scheme", "[x](jav&#x61;script:alert(1))", nil, []string{"href"}},
	{"entity colon", "[x](javascript&#58;alert(1))", nil, []string{"href"}},
	{"network relative", "[x](//example.com/x)", nil, []string{"href"}},
	{"http opaque", "[x](http:foo)", nil, []string{"href"}},
	{"mailto empty address", "[m](mailto:)", nil, []string{"href"}},
}

func TestRender(t *testing.T) {
	for _, tc := range renderCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := Render(tc.in)
			if err != nil {
				t.Fatalf("Render(%q): %v", tc.in, err)
			}
			if strings.Contains(string(got), `class="notes-plain"`) {
				t.Error("ordinary notes took the plain-text fallback")
			}
			for _, want := range tc.want {
				if !strings.Contains(string(got), want) {
					t.Errorf("Render(%q) = %q; want substring %q", tc.in, got, want)
				}
			}
			for _, notWant := range tc.notWant {
				if strings.Contains(string(got), notWant) {
					t.Errorf("Render(%q) = %q; unwanted substring %q", tc.in, got, notWant)
				}
			}
		})
	}
}

func FuzzRender(f *testing.F) {
	f.Add(strings.Repeat(">", 17) + "\n<img src=x onerror=x>\n")
	for _, tc := range renderCases {
		f.Add(tc.in)
	}
	f.Fuzz(func(t *testing.T, src string) {
		got, err := Render(src)
		if err != nil {
			t.Fatalf("Render(%q): %v", src, err)
		}
		assertInert(t, string(got))
	})
}

func assertInert(t *testing.T, got string) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(got))
	if err != nil {
		t.Fatalf("parse rendered HTML: %v", err)
	}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		// Check parsed markup only: ordinary text may contain words such
		// as "javascript:" and "onload" without being dangerous.
		if n.Type == html.ElementNode {
			switch n.Data {
			case "html", "head", "body", // Added by html.Parse.
				"p", "br", "hr", "h1", "h2", "h3", "h4", "h5", "h6", "blockquote",
				"pre", "code", "em", "strong", "del", "ul", "ol", "li", "a":
			default:
				t.Fatalf("disallowed element %q in %q", n.Data, got)
			}
			for _, attr := range n.Attr {
				if strings.HasPrefix(attr.Key, "on") {
					t.Fatalf("event attribute %q in %q", attr.Key, got)
				}
				switch attr.Key {
				case "class":
					if (n.Data != "pre" || attr.Val != "notes-plain") && (n.Data != "p" || attr.Val != "muted") {
						t.Fatalf("disallowed class %q", attr.Val)
					}
				case "href", "rel", "target", "start":
				default:
					t.Fatalf("disallowed attribute %q in %q", attr.Key, got)
				}
				if attr.Key == "href" &&
					!strings.HasPrefix(attr.Val, "http://") &&
					!strings.HasPrefix(attr.Val, "https://") &&
					!strings.HasPrefix(attr.Val, "mailto:") {
					t.Fatalf("disallowed href %q in %q", attr.Val, got)
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
}

func TestRenderComplexityFallback(t *testing.T) {
	for _, seed := range []string{">", "[a](", "*a", "1. ", "~~a", "`a``"} {
		t.Run(seed, func(t *testing.T) {
			src := strings.Repeat(seed, (64<<10)/len(seed))
			assertPlainNotes(t, src)
		})
	}
	for _, src := range []string{
		strings.Repeat(">", 17), strings.Repeat("1. ", 17),
		strings.Repeat("- ", 17), strings.Repeat("[", 17) + "text",
		strings.Repeat("*", 34) + "a" + strings.Repeat("*", 34), strings.Repeat("_", 34) + "a" + strings.Repeat("_", 34),
		strings.Repeat("*a ", 17), strings.Repeat("*a*\n", 1025),
		strings.Repeat(">", 17) + "\n<script>alert(1)</script><img src=x onerror=x>&",
	} {
		assertPlainNotes(t, src)
	}
}

func assertPlainNotes(t *testing.T, src string) {
	t.Helper()
	got, err := Render(src)
	if err != nil {
		t.Fatal(err)
	}
	assertInert(t, string(got))
	doc, err := html.Parse(strings.NewReader(string(got)))
	if err != nil {
		t.Fatal(err)
	}
	var pre, notice bool
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "pre" {
			pre = len(n.Attr) == 1 && n.Attr[0].Key == "class" && n.Attr[0].Val == "notes-plain" && n.FirstChild != nil && n.FirstChild.Type == html.TextNode && n.FirstChild.Data == src
		}
		if n.Type == html.ElementNode && n.Data == "p" && n.FirstChild != nil && n.FirstChild.Data == "Shown as plain text — too complex to format." {
			notice = len(n.Attr) == 1 && n.Attr[0].Val == "muted"
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	if !pre || !notice {
		t.Errorf("missing escaped plain notes or notice (input %d bytes)", len(src))
	}
}

func TestRenderOutputBound(t *testing.T) {
	// No delimiter/container complexity: force the post-sanitization size guard.
	assertPlainNotes(t, strings.Repeat("www.a.co ", 7200))
}

func TestRenderOrdinaryLargeNotes(t *testing.T) {
	for _, src := range []string{"", strings.Repeat("*", 32) + "a" + strings.Repeat("*", 32), strings.Repeat("ordinary notes\n", 4096), strings.Repeat(">", 16) + " quote", strings.Repeat("*a*\n", 1024)} {
		got, err := Render(src)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(got), `class="notes-plain"`) {
			t.Errorf("ordinary notes took fallback (input %d bytes)", len(src))
		}
	}
}

func TestRenderFencedDelimiters(t *testing.T) {
	for _, fence := range []string{"```", "~~~~"} {
		src := "Before\n\n  " + fence + "text\n" + strings.Repeat("*_[()]", 500) + "\n " + fence + fence + "\t\n\n**after**"
		got, err := Render(src)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(got), "<pre><code>") || !strings.Contains(string(got), "<strong>after</strong>") {
			t.Errorf("%q fence lost code or following markdown", fence)
		}
	}
}

func TestRenderFenceBounds(t *testing.T) {
	delimiters := strings.Repeat("*a*\n", 1025)
	for name, src := range map[string]string{
		"unclosed":              "```\n" + delimiters,
		"oversize unclosed":     "```\n" + strings.Repeat("x", 64<<10),
		"after closer":          "~~~~\n~~~\ncode\n~~~~~\n" + delimiters,
		"invalid backtick info": "```lang`info\n" + delimiters + "```",
		"list continuation":     "- list\n  ```\n  code\n" + delimiters + "```",
		"quote":                 "> ```\n> code\n" + strings.Repeat(">", 17) + "\n```",
		"raw HTML":              "<!--\n```\n-->\n" + delimiters + "```",
	} {
		t.Run(name, func(t *testing.T) { assertPlainNotes(t, src) })
	}
}
