// Package markdown renders task notes as sanitized HTML.
package markdown

import (
	"bytes"
	"html/template"
	"net/url"

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
	hostRequired := func(u *url.URL) bool { return u.Host != "" }
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

// Render converts Markdown notes to HTML using the notes allowlist.
func Render(src string) (template.HTML, error) {
	var buf bytes.Buffer
	if err := converter.Convert([]byte(src), &buf); err != nil {
		return "", err
	}
	return template.HTML(policy.SanitizeBytes(buf.Bytes())), nil //nolint:gosec // G203: bluemonday-sanitized output
}
