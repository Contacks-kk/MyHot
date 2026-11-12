package translations

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/PuerkitoBio/goquery"
	xhtml "golang.org/x/net/html"
)

var (
	translatableBlocks = stringSet("p", "h2", "h3", "h4", "h5", "li", "blockquote", "figcaption", "td", "th", "dt", "dd", "caption")
	blockContainers    = stringSet("p", "h2", "h3", "h4", "h5", "li", "blockquote", "figcaption", "td", "th", "dt", "dd", "caption", "ul", "ol", "table", "pre", "figure", "div")
	shieldTags         = stringSet("picture", "video", "img", "code")
	shieldToken        = regexp.MustCompile(`⟦(\d+)⟧`)
)

type shieldedHTML struct {
	HTML   string
	Tokens []string
	Links  [][]xhtml.Attribute
}

func stringSet(values ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func parseFragment(raw string) (*goquery.Document, *goquery.Selection, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(`<div id="translation-root">` + raw + `</div>`))
	if err != nil {
		return nil, nil, err
	}
	return doc, doc.Find("#translation-root").First(), nil
}

func selectionHTML(selection *goquery.Selection) string {
	value, _ := selection.Html()
	return value
}

func nodeHTML(node *xhtml.Node) string {
	var output bytes.Buffer
	if err := xhtml.Render(&output, node); err != nil {
		return ""
	}
	return output.String()
}

func containsTranslatableLetter(value string) bool {
	for _, current := range value {
		if current >= 'A' && current <= 'Z' || current >= 'a' && current <= 'z' ||
			(current >= 0x00c0 && current <= 0x024f) || (current >= 0x0400 && current <= 0x04ff) ||
			(current >= 0x3040 && current <= 0x30ff) {
			return true
		}
	}
	return false
}

func bodySegments(raw string) ([]*xhtml.Node, error) {
	_, root, err := parseFragment(raw)
	if err != nil {
		return nil, err
	}
	return bodySegmentsIn(root), nil
}

func bodySegmentsIn(root *goquery.Selection) []*xhtml.Node {
	segments := make([]*xhtml.Node, 0)
	var visit func([]*xhtml.Node)
	visit = func(nodes []*xhtml.Node) {
		for _, node := range nodes {
			if node.Type != xhtml.ElementNode {
				continue
			}
			if node.Data == "pre" || node.Data == "code" {
				continue
			}
			hasBlockChild := false
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				if child.Type == xhtml.ElementNode {
					if _, ok := blockContainers[child.Data]; ok {
						hasBlockChild = true
						break
					}
				}
			}
			if _, ok := translatableBlocks[node.Data]; ok && !hasBlockChild {
				if containsTranslatableLetter(nodeText(node)) {
					segments = append(segments, node)
				}
				continue
			}
			children := make([]*xhtml.Node, 0)
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				children = append(children, child)
			}
			visit(children)
		}
	}
	children := make([]*xhtml.Node, 0)
	for _, node := range root.Nodes {
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			children = append(children, child)
		}
	}
	visit(children)
	return segments
}

func nodeText(node *xhtml.Node) string {
	var output strings.Builder
	var visit func(*xhtml.Node)
	visit = func(current *xhtml.Node) {
		if current.Type == xhtml.TextNode {
			output.WriteString(current.Data)
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(node)
	return output.String()
}

func shieldHTML(raw string) (shieldedHTML, error) {
	doc, root, err := parseFragment(raw)
	if err != nil {
		return shieldedHTML{}, err
	}
	result := shieldedHTML{}
	root.Find("picture, video, img, code").Each(func(_ int, selection *goquery.Selection) {
		node := selection.Get(0)
		for parent := node.Parent; parent != nil && parent != root.Get(0); parent = parent.Parent {
			if parent.Type == xhtml.ElementNode {
				if _, ok := shieldTags[parent.Data]; ok {
					return
				}
			}
		}
		result.Tokens = append(result.Tokens, nodeHTML(node))
		selection.ReplaceWithHtml(fmt.Sprintf("⟦%d⟧", len(result.Tokens)-1))
	})
	root.Find("a").Each(func(index int, selection *goquery.Selection) {
		node := selection.Get(0)
		attrs := append([]xhtml.Attribute(nil), node.Attr...)
		result.Links = append(result.Links, attrs)
		node.Attr = []xhtml.Attribute{{Key: "id", Val: "L" + strconv.Itoa(index)}}
	})
	result.HTML = selectionHTML(root)
	_ = doc
	return result, nil
}

func unshieldHTML(translated string, original shieldedHTML) (string, bool) {
	counts := make(map[int]int)
	for _, match := range shieldToken.FindAllStringSubmatch(translated, -1) {
		index, err := strconv.Atoi(match[1])
		if err != nil {
			return "", false
		}
		counts[index]++
	}
	if len(counts) != len(original.Tokens) {
		return "", false
	}
	for index := range original.Tokens {
		if counts[index] != 1 {
			return "", false
		}
	}
	doc, root, err := parseFragment(translated)
	if err != nil {
		return "", false
	}
	seen := make(map[int]struct{}, len(original.Links))
	intact := true
	root.Find("a").Each(func(_ int, selection *goquery.Selection) {
		node := selection.Get(0)
		var id string
		for _, attribute := range node.Attr {
			if attribute.Key == "id" {
				id = attribute.Val
				break
			}
		}
		if !strings.HasPrefix(id, "L") {
			intact = false
			return
		}
		index, err := strconv.Atoi(strings.TrimPrefix(id, "L"))
		if err != nil || index < 0 || index >= len(original.Links) {
			intact = false
			return
		}
		if _, exists := seen[index]; exists {
			intact = false
			return
		}
		seen[index] = struct{}{}
		node.Attr = append([]xhtml.Attribute(nil), original.Links[index]...)
	})
	if !intact || len(seen) != len(original.Links) {
		return "", false
	}
	result := selectionHTML(root)
	for index, token := range original.Tokens {
		result = strings.ReplaceAll(result, fmt.Sprintf("⟦%d⟧", index), token)
	}
	_ = doc
	return result, true
}

func utf16Length(value string) int { return len(utf16.Encode([]rune(value))) }

func truncateUTF16(value string, maximum int) string {
	units := utf16.Encode([]rune(value))
	if len(units) <= maximum {
		return value
	}
	return string(utf16.Decode(units[:maximum]))
}
