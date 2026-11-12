package publiccontent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"aihot/backend-go/internal/bodyhtml"
)

type SiteItemMarkdown struct {
	Filename string
	Body     string
}

type siteItemMarkdownX struct {
	Text   any                    `json:"text"`
	Quoted *siteItemMarkdownQuote `json:"quoted"`
}

type siteItemMarkdownQuote struct {
	Handle any `json:"handle"`
	Text   any `json:"text"`
	URL    any `json:"url"`
}

var markdownPrefixPattern = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)
var markdownBlankLinesPattern = regexp.MustCompile(`\n{3,}`)

// SiteItemMarkdown exports only public editorial items that have something the reader can download.
func (s *Service) SiteItemMarkdown(ctx context.Context, id, siteName, siteURL, prefix string, xPostIsFullText bool) (*SiteItemMarkdown, error) {
	if !siteItemIDPattern.MatchString(id) {
		return nil, nil
	}
	repository, ok := s.repository.(SiteItemRepository)
	if !ok {
		return nil, errors.New("site item repository is unavailable")
	}
	row, err := repository.SiteItem(ctx, id, s.topics)
	if err != nil || row == nil {
		return nil, err
	}
	if !siteItemMarkdownAvailable(row, xPostIsFullText) {
		return nil, nil
	}

	lines := []string{"# " + row.Item.Title, ""}
	if row.OriginalTitle != nil && *row.OriginalTitle != "" {
		lines = append(lines, "> 原标题："+*row.OriginalTitle, "")
	}
	lines = append(lines, "- 来源："+PublicSourceName(row.Item.SourceName))
	if row.Item.PublishedAt != nil {
		lines = append(lines, "- 发布时间："+markdownTime(*row.Item.PublishedAt))
	} else {
		lines = append(lines, "- 收录时间："+markdownTime(row.DiscoveredAt))
	}
	itemURL := strings.TrimRight(siteURL, "/") + "/items/" + row.Item.ID
	lines = append(lines,
		"- "+siteName+"："+itemURL,
		"- 原文："+row.OriginalURL,
		"",
	)
	if row.Item.Summary != nil && *row.Item.Summary != "" {
		lines = append(lines, "## 摘要", "", *row.Item.Summary, "")
	}
	if row.Item.Selected != nil && *row.Item.Selected && row.Seat && row.Item.Reason != nil && *row.Item.Reason != "" {
		lines = append(lines, "## 推荐理由", "", *row.Item.Reason, "")
	}
	if row.Item.Channel == "x" && (!xPostIsFullText || row.Item.BodyMode == "full") {
		var post siteItemMarkdownX
		if len(row.Item.XPost) > 0 && string(row.Item.XPost) != "null" {
			if err := json.Unmarshal(row.Item.XPost, &post); err != nil {
				return nil, errors.New("invalid X post data")
			}
		}
		if text := siteItemString(post.Text, ""); strings.TrimSpace(text) != "" {
			lines = append(lines, "## 正文", "", text, "")
		}
		if translated := optionalTrimmed(row.TranslationText); translated != nil {
			lines = append(lines, "## 中文译文", "", *translated, "")
		}
		if post.Quoted != nil {
			quotedText := siteItemString(post.Quoted.Text, "")
			if quotedText != "" {
				lines = append(lines, "## 引用 @"+siteItemString(post.Quoted.Handle, ""), "")
				lines = append(lines, quoteLines(quotedText)...)
				lines = append(lines, "")
				if quotedURL := siteItemString(post.Quoted.URL, ""); quotedURL != "" {
					lines = append(lines, quotedURL, "")
				}
				if translated := optionalTrimmed(row.Item.QuotedZH); translated != nil {
					lines = append(lines, "### 引用中文译文", "")
					lines = append(lines, quoteLines(*translated)...)
					lines = append(lines, "")
				}
			}
		}
	} else if row.Item.BodyMode == "full" && row.BodyHTML != nil && *row.BodyHTML != "" {
		chinese := isChineseBody(row.Language, row.BodyText)
		if !chinese && row.TranslationHTML != nil && *row.TranslationHTML != "" && row.TranslationDone != nil && *row.TranslationDone {
			translation, err := bodyhtml.Markdown(*row.TranslationHTML, row.OriginalURL)
			if err != nil {
				return nil, err
			}
			if translation != "" {
				lines = append(lines, "## 正文 · 中文译文", "", translation, "")
			}
		}
		original, err := bodyhtml.Markdown(*row.BodyHTML, row.OriginalURL)
		if err != nil {
			return nil, err
		}
		heading := "## 正文 · 原文"
		if chinese {
			heading = "## 正文"
		}
		lines = append(lines, heading, "", original, "")
	}

	filenamePrefix := markdownPrefixPattern.ReplaceAllString(strings.TrimSpace(prefix), "-")
	filenamePrefix = strings.Trim(filenamePrefix, "-")
	if filenamePrefix == "" {
		filenamePrefix = "site"
	}
	return &SiteItemMarkdown{
		Filename: fmt.Sprintf("%s-%s.md", filenamePrefix, row.Item.ID),
		Body:     markdownBlankLinesPattern.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"),
	}, nil
}

func markdownTime(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}

func quoteLines(value string) []string {
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(value, "\n")
	for index := range lines {
		lines[index] = "> " + lines[index]
	}
	return lines
}
