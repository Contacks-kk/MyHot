package bodyhtml

import (
	"bytes"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"aihot/backend-go/internal/mediaurl"
	"github.com/PuerkitoBio/goquery"
	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

type OutlineEntry struct {
	ID    string `json:"id"`
	Text  string `json:"text"`
	Level int    `json:"level"`
}

var promoClass = regexp.MustCompile(`(?i)(?:^|[-_])promo(?:$|[-_])`)
var languageClass = regexp.MustCompile(`^language-[\w-]+$`)

var allowedTags = setOf("p", "br", "hr", "h2", "h3", "h4", "h5", "ul", "ol", "li", "blockquote", "pre", "code", "table", "thead", "tbody", "tfoot", "tr", "th", "td", "caption", "a", "img", "figure", "figcaption", "strong", "em", "b", "i", "u", "s", "del", "sup", "sub", "mark", "span", "dl", "dt", "dd", "picture", "video")
var dropWholeTags = setOf("script", "style", "noscript", "textarea", "option", "iframe", "object", "embed", "applet", "form", "input", "select", "button", "label", "fieldset", "legend", "svg", "link", "meta", "base", "title", "head", "template", "audio", "map", "area", "frame", "frameset", "track", "source", "param")

func Sanitize(raw, baseURL, imageProxySecret string, now time.Time) (string, []OutlineEntry, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader("<body>" + raw + "</body>"))
	if err != nil {
		return "", nil, err
	}
	body := doc.Find("body").First()
	body.Find("[class]").Each(func(_ int, selection *goquery.Selection) {
		classes := strings.Fields(selection.AttrOr("class", ""))
		for _, className := range classes {
			if promoClass.MatchString(className) {
				selection.Remove()
				return
			}
		}
	})
	for tag := range dropWholeTags {
		body.Find(tag).Remove()
	}
	for _, node := range body.Find("*").Nodes {
		switch node.Data {
		case "h1":
			node.Data, node.DataAtom = "h2", atom.H2
		case "h6":
			node.Data, node.DataAtom = "h5", atom.H5
		case "div", "section", "article":
			node.Data, node.DataAtom = "p", atom.P
		}
	}
	for _, node := range body.Find("*").Nodes {
		tag := strings.ToLower(node.Data)
		if !allowedTags[tag] {
			goquery.NewDocumentFromNode(node).Selection.Unwrap()
			continue
		}
		if err := cleanAttributes(node, baseURL); err != nil {
			return "", nil, err
		}
	}
	body.Find("p").Each(func(_ int, selection *goquery.Selection) {
		if strings.TrimSpace(selection.Text()) == "" && selection.Find("img, video, picture").Length() == 0 {
			selection.Remove()
		}
	})
	body.Find("a").Each(func(_ int, selection *goquery.Selection) {
		if strings.TrimSpace(selection.Text()) == "" && selection.Find("img").Length() == 0 {
			selection.Remove()
		}
	})
	if err := proxyImages(body, now, imageProxySecret); err != nil {
		return "", nil, err
	}
	outline := make([]OutlineEntry, 0)
	body.Find("h2, h3, h4").Each(func(index int, selection *goquery.Selection) {
		id := fmt.Sprintf("sec-%d", index+1)
		selection.SetAttr("id", id)
		text := truncateUTF16(strings.TrimSpace(selection.Text()), 80)
		if text != "" {
			level, _ := strconv.Atoi(selection.Nodes[0].Data[1:])
			outline = append(outline, OutlineEntry{ID: id, Text: text, Level: level})
		}
	})
	var output bytes.Buffer
	for _, node := range body.Children().Nodes {
		if err := renderHTML(&output, node); err != nil {
			return "", nil, err
		}
	}
	return strings.TrimSpace(output.String()), outline, nil
}

func Markdown(raw, baseURL string) (string, error) {
	safe, _, err := Sanitize(raw, baseURL, "", time.Now())
	if err != nil {
		return "", err
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader("<body>" + safe + "</body>"))
	if err != nil {
		return "", err
	}
	parts := make([]string, 0)
	for _, node := range doc.Find("body").First().Children().Nodes {
		part := markdownNode(node, 0)
		if strings.TrimSpace(part) != "" {
			parts = append(parts, strings.TrimSpace(part))
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n")), nil
}

func markdownNode(node *xhtml.Node, listDepth int) string {
	if node.Type == xhtml.TextNode {
		return node.Data
	}
	if node.Type != xhtml.ElementNode {
		return markdownChildren(node, listDepth)
	}
	inner := markdownChildren(node, listDepth)
	switch strings.ToLower(node.Data) {
	case "p", "figure", "figcaption", "caption", "dt", "dd":
		return strings.TrimSpace(inner)
	case "h2", "h3", "h4", "h5":
		level := int(node.Data[1] - '0')
		return strings.Repeat("#", level) + " " + strings.TrimSpace(inner)
	case "blockquote":
		lines := strings.Split(strings.TrimSpace(inner), "\n")
		for index := range lines {
			lines[index] = "> " + lines[index]
		}
		return strings.Join(lines, "\n")
	case "ul", "ol":
		items := make([]string, 0)
		index := 0
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if child.Type != xhtml.ElementNode || child.Data != "li" {
				continue
			}
			index++
			prefix := "- "
			if node.Data == "ol" {
				prefix = strconv.Itoa(index) + ". "
			}
			items = append(items, strings.Repeat("  ", listDepth)+prefix+strings.TrimSpace(markdownChildren(child, listDepth+1)))
		}
		return strings.Join(items, "\n")
	case "pre":
		language := ""
		code := node.FirstChild
		if code != nil && code.Type == xhtml.ElementNode && code.Data == "code" {
			for _, attr := range code.Attr {
				if attr.Key == "class" && strings.HasPrefix(attr.Val, "language-") {
					language = strings.TrimPrefix(attr.Val, "language-")
				}
			}
		}
		return "```" + language + "\n" + strings.TrimSpace(nodeTextContent(node)) + "\n```"
	case "code":
		return "`" + strings.TrimSpace(inner) + "`"
	case "strong", "b":
		return "**" + inner + "**"
	case "em", "i":
		return "*" + inner + "*"
	case "del", "s":
		return "~~" + inner + "~~"
	case "u", "sup", "sub", "mark", "dl", "video":
		return markdownKeptHTML(node)
	case "a":
		href := ""
		for _, attr := range node.Attr {
			if attr.Key == "href" {
				href = attr.Val
				break
			}
		}
		if href == "" {
			return inner
		}
		return "[" + inner + "](" + href + ")"
	case "img":
		attrs := make(map[string]string)
		for _, attr := range node.Attr {
			attrs[attr.Key] = attr.Val
		}
		if attrs["src"] == "" {
			return ""
		}
		return "![" + attrs["alt"] + "](" + attrs["src"] + ")"
	case "br":
		return "  \n"
	case "hr":
		return "---"
	case "table":
		table := goquery.NewDocumentFromNode(node).Find("table").First()
		rows := make([]string, 0)
		table.Find("tr").Each(func(_ int, row *goquery.Selection) {
			cells := make([]string, 0)
			row.ChildrenFiltered("th, td").Each(func(_ int, cell *goquery.Selection) {
				cellText := strings.TrimSpace(markdownChildren(cell.Nodes[0], 0))
				cellText = strings.ReplaceAll(cellText, "|", `\|`)
				cellText = regexp.MustCompile(`\s*\n\s*`).ReplaceAllString(cellText, "<br>")
				cells = append(cells, cellText)
			})
			if len(cells) > 0 {
				rows = append(rows, "| "+strings.Join(cells, " | ")+" |")
			}
		})
		if len(rows) == 0 {
			return ""
		}
		first := table.Find("tr").First()
		width := first.ChildrenFiltered("th, td").Length()
		complex := width == 0 || first.ChildrenFiltered("th").Length() != width || table.Find("table, [rowspan], [colspan], pre, ul, ol, dl").Length() > 0
		table.Find("tr").Each(func(_ int, row *goquery.Selection) {
			if row.ChildrenFiltered("th, td").Length() != width {
				complex = true
			}
		})
		if complex {
			var safe bytes.Buffer
			if err := xhtml.Render(&safe, node); err != nil {
				return ""
			}
			return strings.TrimSpace(safe.String())
		}
		alignments := make([]string, 0, width)
		first.ChildrenFiltered("th").Each(func(_ int, cell *goquery.Selection) {
			switch strings.ToLower(cell.AttrOr("align", "")) {
			case "left":
				alignments = append(alignments, ":---")
			case "right":
				alignments = append(alignments, "---:")
			case "center":
				alignments = append(alignments, ":---:")
			default:
				alignments = append(alignments, "---")
			}
		})
		rows = append(rows[:1], append([]string{"| " + strings.Join(alignments, " | ") + " |"}, rows[1:]...)...)
		if caption := table.ChildrenFiltered("caption"); caption.Length() > 0 {
			captionMarkdown := strings.TrimSpace(markdownChildren(caption.Nodes[0], 0))
			if captionMarkdown != "" {
				return captionMarkdown + "\n\n" + strings.Join(rows, "\n")
			}
		}
		return strings.Join(rows, "\n")
	default:
		return inner
	}
}

func markdownChildren(node *xhtml.Node, listDepth int) string {
	parts := make([]string, 0)
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		part := markdownNode(child, listDepth)
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "")
}

func markdownKeptHTML(node *xhtml.Node) string {
	var output bytes.Buffer
	if err := xhtml.Render(&output, node); err != nil {
		return nodeTextContent(node)
	}
	return output.String()
}

func nodeTextContent(node *xhtml.Node) string {
	var output strings.Builder
	var visit func(*xhtml.Node)
	visit = func(current *xhtml.Node) {
		if current.Type == xhtml.TextNode {
			output.WriteString(current.Data)
			return
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(node)
	return output.String()
}

func cleanAttributes(node *xhtml.Node, baseURL string) error {
	tag := strings.ToLower(node.Data)
	attrs := make(map[string]string, len(node.Attr))
	order := make([]string, 0, len(node.Attr)+1)
	for _, attr := range node.Attr {
		key := strings.ToLower(attr.Key)
		if _, exists := attrs[key]; !exists {
			order = append(order, key)
		}
		attrs[key] = attr.Val
	}
	if tag == "img" {
		source := attrs["data-src"]
		if source == "" {
			source = attrs["data-original"]
		}
		if source == "" {
			source = attrs["src"]
		}
		if source == "" {
			node.Parent.RemoveChild(node)
			return nil
		}
		if strings.HasPrefix(source, "/api/img-proxy?") {
			if parsed, err := url.Parse(source); err == nil {
				if wrapped := parsed.Query().Get("u"); strings.HasPrefix(wrapped, "http://") || strings.HasPrefix(wrapped, "https://") {
					source = wrapped
				}
			}
		}
		resolved, ok := resolveURL(source, baseURL, true)
		if !ok {
			node.Parent.RemoveChild(node)
			return nil
		}
		attrs["src"] = resolved
		found := false
		for _, key := range order {
			if key == "src" {
				found = true
				break
			}
		}
		if !found {
			order = append(order, "src")
		}
	}
	allowed := map[string]bool{}
	switch tag {
	case "a":
		allowed = setOf("href", "title")
	case "img":
		allowed = setOf("src", "alt", "width", "height", "title")
	case "video":
		allowed = setOf("src", "poster", "width", "height")
	case "code", "pre":
		allowed = setOf("class")
	case "th", "td":
		allowed = setOf("colspan", "rowspan", "align")
	}
	clean := make([]xhtml.Attribute, 0, len(allowed))
	for _, key := range order {
		if !allowed[key] {
			continue
		}
		value, exists := attrs[key]
		if !exists {
			continue
		}
		if key == "href" || key == "src" || key == "poster" {
			resolveAgainst := baseURL
			if tag == "video" && key == "src" {
				resolveAgainst = ""
			}
			resolved, ok := resolveURL(value, resolveAgainst, tag == "img" && key == "src")
			if !ok {
				continue
			}
			value = resolved
		}
		if key == "class" {
			classes := strings.Fields(value)
			kept := make([]string, 0, len(classes))
			for _, className := range classes {
				if languageClass.MatchString(className) {
					kept = append(kept, className)
				}
			}
			value = strings.Join(kept, " ")
			if value == "" {
				continue
			}
		}
		clean = append(clean, xhtml.Attribute{Key: key, Val: value})
	}
	node.Attr = clean
	return nil
}

func renderHTML(w io.Writer, node *xhtml.Node) error { return xhtml.Render(w, node) }

/** proxyImages removes tracking media and assigns signed URLs to article images. */
func proxyImages(body *goquery.Selection, now time.Time, secret string) error {
	var proxyErr error
	body.Find("img").Each(func(_ int, selection *goquery.Selection) {
		if proxyErr != nil {
			return
		}
		src := selection.AttrOr("src", "")
		var widthValue, heightValue *float64
		if raw := selection.AttrOr("width", ""); raw != "" {
			if width, err := strconv.ParseFloat(raw, 64); err == nil {
				widthValue = &width
			}
		}
		if raw := selection.AttrOr("height", ""); raw != "" {
			if height, err := strconv.ParseFloat(raw, 64); err == nil {
				heightValue = &height
			}
		}
		if mediaurl.IsNonArticleImage(src, widthValue, heightValue) {
			selection.Remove()
			return
		}
		if secret == "" {
			return
		}
		proxied, err := mediaurl.ProxyURL(&src, "full", now, 48*time.Hour, secret)
		if err != nil {
			proxyErr = err
			return
		}
		if proxied == nil {
			selection.Remove()
			return
		}
		selection.SetAttr("src", *proxied)
		if widthValue != nil && *widthValue >= 720 {
			candidates, err := mediaurl.ProxyURLSet(&src, []string{"image-720", "image-1200", "image-1600"}, []int{720, 1200, 1600}, now, 48*time.Hour, secret)
			if err != nil {
				proxyErr = err
				return
			}
			if candidates != nil {
				selection.SetAttr("srcset", *candidates)
				selection.SetAttr("sizes", "(min-width: 1536px) 760px, (min-width: 1024px) calc(100vw - 524px), (min-width: 640px) 608px, calc(100vw - 32px)")
			}
		}
		selection.SetAttr("loading", "lazy")
		selection.SetAttr("decoding", "async")
	})
	body.Find("video[poster]").Each(func(_ int, selection *goquery.Selection) {
		if proxyErr != nil {
			return
		}
		if secret == "" {
			return
		}
		poster := selection.AttrOr("poster", "")
		proxied, err := mediaurl.ProxyURL(&poster, "thumb", now, 48*time.Hour, secret)
		if err != nil {
			proxyErr = err
		} else if proxied != nil {
			selection.SetAttr("poster", *proxied)
		} else {
			selection.RemoveAttr("poster")
		}
	})
	return proxyErr
}

func resolveURL(raw, base string, allowData bool) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "" && parsed.Scheme != "http" && parsed.Scheme != "https" && !(allowData && parsed.Scheme == "data") {
		return "", false
	}
	if parsed.Scheme == "data" {
		return parsed.String(), allowData
	}
	if base != "" {
		baseURL, err := url.Parse(base)
		if err == nil {
			parsed = baseURL.ResolveReference(parsed)
		}
	}
	if parsed.Scheme != "" && parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", false
	}
	return parsed.String(), true
}

func truncateUTF16(value string, limit int) string {
	encoded := utf16.Encode([]rune(value))
	if len(encoded) > limit {
		encoded = encoded[:limit]
	}
	return string(utf16.Decode(encoded))
}

func setOf(values ...string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}
