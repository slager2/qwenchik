// Package pdf renders research reports (Markdown produced from untrusted web
// content) into printable HTML and PDF.
//
// Security posture: report Markdown quotes web pages verbatim, so it is
// treated as hostile. Raw HTML is never passed through (it is shown as
// escaped text), links keep only http, https, mailto and in-document
// fragment targets, and images are replaced by a textual reference so the
// renderer never fetches anything. The generated document additionally
// carries a Content-Security-Policy that forbids scripts and every
// subresource load.
package pdf

import (
	"bytes"
	"net/url"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/util"
)

// MaxMarkdownBytes bounds the input accepted by MarkdownToHTML. Research
// reports are capped well below this by the orchestrator.
const MaxMarkdownBytes = 4 << 20

var markdown = goldmark.New(
	goldmark.WithExtensions(extension.Table, extension.Strikethrough, extension.TaskList, extension.Linkify),
	// html.WithUnsafe is deliberately absent; the overrides below make the
	// remaining raw-HTML and URL paths explicit instead of relying on it.
	goldmark.WithRendererOptions(html.WithHardWraps(), renderer.WithNodeRenderers(util.Prioritized(safeRenderer{}, 100))),
)

// MarkdownToHTML converts report Markdown into an HTML fragment that is safe
// to embed in a document opened by a browser.
func MarkdownToHTML(source string) (string, error) {
	if len(source) > MaxMarkdownBytes {
		source = source[:MaxMarkdownBytes]
	}
	var out bytes.Buffer
	if err := markdown.Convert([]byte(source), &out); err != nil {
		return "", err
	}
	return out.String(), nil
}

type safeRenderer struct{}

func (safeRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindHTMLBlock, renderHTMLBlock)
	reg.Register(ast.KindRawHTML, renderRawHTML)
	reg.Register(ast.KindLink, renderLink)
	reg.Register(ast.KindAutoLink, renderAutoLink)
	reg.Register(ast.KindImage, renderImage)
}

// safeHref returns the escaped attribute value for an allowed link target,
// or "" when the destination must not become a link.
func safeHref(destination []byte) string {
	value := strings.TrimSpace(string(destination))
	if value == "" || len(value) > 4096 {
		return ""
	}
	if strings.HasPrefix(value, "#") {
		return string(util.EscapeHTML(util.URLEscape([]byte(value), true)))
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		if parsed.Host == "" {
			return ""
		}
	case "mailto":
	default:
		return ""
	}
	return string(util.EscapeHTML(util.URLEscape([]byte(value), true)))
}

func renderHTMLBlock(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	n := node.(*ast.HTMLBlock)
	if !entering {
		return ast.WalkContinue, nil
	}
	_, _ = w.WriteString(`<pre class="raw-html">`)
	for i := 0; i < n.Lines().Len(); i++ {
		line := n.Lines().At(i)
		_, _ = w.Write(util.EscapeHTML(line.Value(source)))
	}
	if n.HasClosure() {
		_, _ = w.Write(util.EscapeHTML(n.ClosureLine.Value(source)))
	}
	_, _ = w.WriteString("</pre>\n")
	return ast.WalkSkipChildren, nil
}

func renderRawHTML(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkSkipChildren, nil
	}
	n := node.(*ast.RawHTML)
	_, _ = w.WriteString(`<code class="raw-html">`)
	for i := 0; i < n.Segments.Len(); i++ {
		segment := n.Segments.At(i)
		_, _ = w.Write(util.EscapeHTML(segment.Value(source)))
	}
	_, _ = w.WriteString("</code>")
	return ast.WalkSkipChildren, nil
}

func nodeText(source []byte, node ast.Node) string {
	var b strings.Builder
	_ = ast.Walk(node, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch t := n.(type) {
		case *ast.Text:
			b.Write(t.Segment.Value(source))
		case *ast.String:
			b.Write(t.Value)
		case *ast.AutoLink:
			b.Write(t.Label(source))
		}
		return ast.WalkContinue, nil
	})
	return b.String()
}

func renderLink(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	n := node.(*ast.Link)
	href := safeHref(n.Destination)
	if href == "" {
		// Unsafe or unsupported target: keep the visible text only.
		return ast.WalkContinue, nil
	}
	if entering {
		_, _ = w.WriteString(`<a href="` + href + `">`)
		return ast.WalkContinue, nil
	}
	_, _ = w.WriteString("</a>")
	// Printed copies cannot be clicked: show the target next to a label that
	// differs from it.
	destination := string(n.Destination)
	if !strings.HasPrefix(destination, "#") && strings.TrimSpace(nodeText(source, n)) != destination {
		_, _ = w.WriteString(`<span class="link-url"> (`)
		_, _ = w.Write(util.EscapeHTML(n.Destination))
		_, _ = w.WriteString(")</span>")
	}
	return ast.WalkContinue, nil
}

func renderAutoLink(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.AutoLink)
	destination := n.URL(source)
	if n.AutoLinkType == ast.AutoLinkEmail && !bytes.HasPrefix(bytes.ToLower(destination), []byte("mailto:")) {
		destination = append([]byte("mailto:"), destination...)
	}
	label := util.EscapeHTML(n.Label(source))
	href := safeHref(destination)
	if href == "" {
		_, _ = w.Write(label)
		return ast.WalkContinue, nil
	}
	_, _ = w.WriteString(`<a href="` + href + `">`)
	_, _ = w.Write(label)
	_, _ = w.WriteString("</a>")
	return ast.WalkContinue, nil
}

func renderImage(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.Image)
	_, _ = w.WriteString(`<span class="image-ref">[Изображение`)
	if alt := strings.TrimSpace(nodeText(source, n)); alt != "" {
		_, _ = w.WriteString(": ")
		_, _ = w.Write(util.EscapeHTML([]byte(alt)))
	}
	_, _ = w.WriteString("]")
	if len(n.Destination) > 0 {
		_, _ = w.WriteString(" ")
		_, _ = w.Write(util.EscapeHTML(n.Destination))
	}
	_, _ = w.WriteString("</span>")
	return ast.WalkSkipChildren, nil
}
