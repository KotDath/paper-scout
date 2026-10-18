package documents

import (
	"bytes"
	"errors"
	"strings"
	"unicode"

	"golang.org/x/net/html"
)

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
func class(n *html.Node, name string) bool {
	for _, c := range strings.Fields(attr(n, "class")) {
		if c == name {
			return true
		}
	}
	return false
}
func excluded(n *html.Node) bool {
	if n.Type != html.ElementNode {
		return false
	}
	switch n.Data {
	case "script", "style", "nav", "header", "footer", "form", "noscript", "annotation", "annotation-xml":
		return true
	}
	for _, a := range n.Attr {
		if a.Key == "hidden" || (a.Key == "aria-hidden" && a.Val == "true") {
			return true
		}
	}
	return class(n, "ltx_page_navbar") || class(n, "ltx_page_footer")
}
func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if excluded(n) {
			return
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		if n.Type == html.ElementNode && n.Data == "br" {
			b.WriteByte(' ')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode {
			switch n.Data {
			case "td", "th", "p", "li", "tr":
				b.WriteByte(' ')
			}
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}
func extractHTML(body []byte) (string, []Segment, []Warning, error) {
	root, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return "", nil, nil, errors.New("html_invalid")
	}
	var article *html.Node
	var find func(*html.Node)
	find = func(n *html.Node) {
		if article != nil || excluded(n) {
			return
		}
		if n.Type == html.ElementNode && (n.Data == "article" || class(n, "ltx_document")) {
			article = n
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			find(c)
		}
	}
	find(root)
	if article == nil {
		return "", nil, nil, errors.New("html_not_article")
	}
	parts := []textPart{}
	section := ""
	var lines []string
	flush := func() {
		if len(lines) > 0 {
			parts = append(parts, textPart{text: strings.Join(lines, "\n\n"), section: section})
			lines = nil
		}
	}
	paragraphs, headings := 0, 0
	var loose strings.Builder
	flushLoose := func() {
		if text := strings.Join(strings.Fields(loose.String()), " "); text != "" {
			lines = append(lines, text)
		}
		loose.Reset()
	}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if excluded(n) {
			return
		}
		if n.Type == html.TextNode {
			loose.WriteString(n.Data)
			return
		}
		if n.Type == html.ElementNode {
			switch n.Data {
			case "br":
				loose.WriteByte(' ')
				return
			case "h1", "h2", "h3", "h4", "h5", "h6":
				flushLoose()
				title := nodeText(n)
				if title != "" {
					flush()
					section = title
					lines = append(lines, title)
					lower := strings.ToLower(title)
					if n.Data != "h1" && !strings.Contains(lower, "abstract") && !strings.Contains(lower, "references") && !strings.Contains(lower, "bibliography") {
						headings++
					}
				}
				return
			case "p", "li", "pre", "table", "figcaption", "math":
				flushLoose()
				text := nodeText(n)
				if text != "" {
					lines = append(lines, text)
					paragraphs++
				}
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode && (n.Data == "div" || n.Data == "section") {
			loose.WriteByte(' ')
		}
	}
	walk(article)
	flushLoose()
	flush()
	text, segments := assemble(parts)
	letters := 0
	for _, r := range text {
		if unicode.IsLetter(r) {
			letters++
		}
	}
	if letters < 400 || paragraphs < 3 || headings == 0 {
		return "", nil, nil, errors.New("html_text_unusable")
	}
	warnings := []Warning{{Code: "html_images_and_math_not_interpreted"}}
	return text, segments, warnings, nil
}
