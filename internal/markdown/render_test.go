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
	for _, tc := range renderCases {
		f.Add(tc.in)
	}
	f.Fuzz(func(t *testing.T, src string) {
		got, err := Render(src)
		if err != nil {
			t.Fatalf("Render(%q): %v", src, err)
		}
		doc, err := html.Parse(strings.NewReader(string(got)))
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
	})
}
