// Package markdown renders task notes as sanitized HTML.
package markdown

import (
	"bytes"
	"html"
	"html/template"
	"net/url"
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/util"
)

var converter = goldmark.New(
	goldmark.WithExtensions(extension.Linkify, extension.Strikethrough),
	// AIDEV-NOTE: Priority 100 overrides the default image renderer (1000).
	// Raw HTML remains disabled; images render as links without fetching them.
	goldmark.WithRendererOptions(renderer.WithNodeRenderers(util.Prioritized(imageAsLink{}, 100))),
)

// AIDEV-NOTE: The notes allowlist (D§10.4). DF02 requires a host for HTTP(S)
// and an address for mailto so scheme-only and relative URLs cannot survive.
var policy = func() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements("p", "br", "hr", "h1", "h2", "h3", "h4", "h5", "h6", "blockquote",
		"pre", "code", "em", "strong", "del", "ul", "ol", "li")
	p.AllowAttrs("start").Matching(bluemonday.Integer).OnElements("ol")
	p.AllowAttrs("href").OnElements("a")
	p.AllowAttrs("class").Matching(regexp.MustCompile(`^notes-plain$`)).OnElements("pre")
	p.AllowAttrs("class").Matching(regexp.MustCompile(`^muted$`)).OnElements("p")
	hostRequired := func(u *url.URL) bool { return u.Hostname() != "" }
	p.AllowURLSchemeWithCustomPolicy("http", hostRequired)
	p.AllowURLSchemeWithCustomPolicy("https", hostRequired)
	p.AllowURLSchemeWithCustomPolicy("mailto", func(u *url.URL) bool { return u.Opaque != "" })
	p.RequireParseableURLs(true)
	p.RequireNoReferrerOnLinks(true)
	p.AddTargetBlankToFullyQualifiedLinks(true)
	return p
}()

type imageAsLink struct{}

func (r imageAsLink) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindImage, r.render)
}

func (imageAsLink) render(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		n := node.(*ast.Image)
		if _, err := w.WriteString(`<a href="`); err != nil {
			return ast.WalkStop, err
		}
		if _, err := w.Write(util.EscapeHTML(util.URLEscape(n.Destination, true))); err != nil {
			return ast.WalkStop, err
		}
		if _, err := w.WriteString(`">`); err != nil {
			return ast.WalkStop, err
		}
	} else if _, err := w.WriteString("</a>"); err != nil {
		return ast.WalkStop, err
	}
	return ast.WalkContinue, nil
}

// Budget bounds markdown conversion for one page. Its zero value is ready to use.
type Budget struct {
	used int
}

const pageNoteBytes = 256 << 10

// Render converts Markdown notes to HTML using the notes allowlist.
func Render(src string) (template.HTML, error) {
	return (*Budget)(nil).Render(src)
}

// Render uses the DF28 fallback once the page's markdown input budget is spent.
func (b *Budget) Render(src string) (template.HTML, error) {
	// Mirror the service's 64 KiB input limit for direct callers, too.
	var rendered []byte
	plain := len(src) > 64<<10 || (b != nil && b.used == pageNoteBytes) || tooComplex(src)
	if !plain && b != nil {
		// AIDEV-NOTE: DF32 charges bytes sent to goldmark, even if its output
		// trips the size guard. A note that won't fit exhausts the page budget.
		if len(src) > pageNoteBytes-b.used {
			b.used = pageNoteBytes
			plain = true
		} else {
			b.used += len(src)
		}
	}
	if !plain {
		var buf bytes.Buffer
		if err := converter.Convert([]byte(src), &buf); err != nil {
			return "", err
		}
		rendered = policy.SanitizeBytes(buf.Bytes())
	}
	if plain || len(rendered) > 512<<10 {
		// A leading newline in pre is consumed by the HTML parser.
		rendered = policy.SanitizeBytes([]byte("<pre class=\"notes-plain\">\n" + html.EscapeString(src) +
			"</pre><p class=\"muted\">Shown as plain text — too complex to format.</p>"))
	}
	return template.HTML(rendered), nil //nolint:gosec // G203: bluemonday-sanitized output
}

// AIDEV-NOTE: DF28: scan in linear time before goldmark. Its repeated container,
// link, emphasis, strike and backtick parsing can be quadratic. Allow at most
// 16 potential nested openers per line and 2048 inline delimiter bytes overall;
// exclude confirmed fence contents and cap sanitized Markdown at 512 KiB. The plain fallback stays escaped and
// sanitized (at most six times the service's 64 KiB notes limit). The 10 s GET
// deadline cancels service calls, not template rendering or goldmark.
func tooComplex(src string) bool {
	delimiters := 0
	// Only exempt confirmed top-level fences. An HTML block can consume an
	// apparent opener, and a fence indented inside a list can end on dedent.
	// In ambiguous context, keep the original checks for the rest of the note.
	checkFences, sawContainer := true, false
	for src != "" {
		line, rest, _ := strings.Cut(src, "\n")
		src = rest
		containers := containerDepth(line)
		if checkFences {
			sawContainer = sawContainer || containers > 0
			trimmed := strings.TrimLeft(line, " ")
			if strings.HasPrefix(trimmed, "<") {
				checkFences = false
			} else if marker, info := fenceMarker(line); marker != "" && (marker[0] == '~' || !strings.Contains(info, "`")) {
				if line == trimmed || !sawContainer {
					if after, closed := afterFence(src, marker); closed {
						src, sawContainer = after, false
						continue
					}
				}
				// Do not search again through an unclosed or ambiguous suffix.
				checkFences = false
			}
		}
		if containers > 16 {
			return true
		}
		brackets, parens := 0, 0
		var emphasis []byte
		for i := 0; i < len(line); i++ {
			c := line[i]
			if strings.ContainsRune("*_~`[]", rune(c)) {
				delimiters++
				if delimiters > 2048 {
					return true
				}
			}
			switch c {
			case '[':
				brackets++
			case ']':
				brackets = max(0, brackets-1)
			case '(':
				if parens > 0 || (i > 0 && line[i-1] == ']') {
					parens++
				}
			case ')':
				parens = max(0, parens-1)
			case '*', '_', '~':
				// A run can produce nested strong/emphasis nodes. Prefer matching
				// closers; ambiguous runs also have the global delimiter budget.
				end := i + 1
				for end < len(line) && line[end] == c {
					end++
				}
				delimiters += end - i - 1
				if delimiters > 2048 {
					return true
				}
				levels := (end - i + 1) / 2
				if i > 0 && !markdownSpace(line[i-1]) {
					for levels > 0 && len(emphasis) > 0 && emphasis[len(emphasis)-1] == c {
						emphasis = emphasis[:len(emphasis)-1]
						levels--
					}
				}
				if end < len(line) && !markdownSpace(line[end]) {
					if containers+brackets+parens+len(emphasis)+levels > 16 {
						return true
					}
					for range levels {
						emphasis = append(emphasis, c)
					}
				}
				i = end - 1
			}
			if containers+brackets+parens+len(emphasis) > 16 {
				return true
			}
		}
	}
	return false
}

// fenceMarker splits a fence run at up to three columns of indentation.
func fenceMarker(line string) (marker, tail string) {
	s := strings.TrimLeft(line, " ")
	if len(line)-len(s) > 3 || len(s) < 3 || (s[0] != '`' && s[0] != '~') {
		return "", ""
	}
	i := 1
	for i < len(s) && s[i] == s[0] {
		i++
	}
	if i < 3 {
		return "", ""
	}
	return s[:i], s[i:]
}

func afterFence(src, opener string) (string, bool) {
	for src != "" {
		line, rest, _ := strings.Cut(src, "\n")
		src = rest
		marker, tail := fenceMarker(line)
		if len(marker) >= len(opener) && marker[0] == opener[0] && strings.Trim(tail, " \t\r") == "" {
			return src, true
		}
	}
	return "", false
}

func markdownSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' }

func containerDepth(line string) int {
	depth, indent := 0, 0
	for i := 0; i < len(line); {
		if markdownSpace(line[i]) {
			if depth == 0 {
				indent++
				if line[i] == '\t' {
					indent += 3
				}
			}
			i++
			continue
		}
		end := i
		switch line[i] {
		case '>':
			end++
		case '-', '+', '*':
			if i+1 == len(line) || markdownSpace(line[i+1]) {
				end++
			}
		default:
			for end < len(line) && end-i < 9 && line[end] >= '0' && line[end] <= '9' {
				end++
			}
			if end == i || end == len(line) || (line[end] != '.' && line[end] != ')') ||
				(end+1 < len(line) && !markdownSpace(line[end+1])) {
				return depth
			}
			end++
		}
		if end == i {
			return depth
		}
		// Two spaces is the minimum indentation for a nested list item.
		if depth == 0 && line[i] != '>' {
			depth += indent / 2
		}
		depth++
		if depth > 16 {
			return depth
		}
		i = end
	}
	return depth
}
