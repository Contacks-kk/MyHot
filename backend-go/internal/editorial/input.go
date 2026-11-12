package editorial

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
)

const MaxBodyChars = 60_000

var xArticleLink = regexp.MustCompile(`(?i)https?://(?:www\.)?(?:x|twitter)\.com/i/article/\d+`)

type Input struct {
	ID            string
	Revision      int
	Title         string
	URL           string
	Author        string
	PublishedAt   *time.Time
	DiscoveredAt  *time.Time
	BodyText      *string
	Excerpt       *string
	BodyStatus    string
	XPost         *XPost
	Media         []Media
	Source        Source
	TranslationZH *string
}

type Source struct {
	Name          string
	Kind          string
	Tier          string
	FirstParty    bool
	Tags          []string
	FetchesBody   bool
	OwnerEntityID *string
}

type XPost struct {
	AuthorName string      `json:"authorName"`
	Handle     string      `json:"handle"`
	Text       string      `json:"text"`
	Media      []Media     `json:"media"`
	Quoted     *QuotedPost `json:"quoted"`
}

type QuotedPost struct {
	AuthorName string  `json:"authorName"`
	Handle     string  `json:"handle"`
	Text       string  `json:"text"`
	Media      []Media `json:"media"`
}

type Media struct {
	Kind string `json:"kind"`
	URL  string `json:"url"`
}

var kindLabels = map[string]string{
	"rss": "RSS", "web_list": "网页", "json_list": "网页接口", "x_search": "X 帖子",
	"mp_account": "微信公众号", "external": "外部上报",
}

// WithXArticle joins a fetched X Article to its post, matching the material read by analysis.
func WithXArticle(post *XPost, article *XArticle) *XPost {
	if post == nil || article == nil || article.Text == "" {
		return post
	}
	parts := []string{strings.TrimSpace(post.Text)}
	if article.Title == "" {
		parts = append(parts, "【X 长文】")
	} else {
		parts = append(parts, "【X 长文】"+article.Title)
	}
	parts = append(parts, article.Text)
	joined := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			joined = append(joined, part)
		}
	}
	copy := *post
	copy.Text = strings.Join(joined, "\n\n")
	return &copy
}

type XArticle struct {
	Title string
	Text  string
}

// BuildMaterial renders the source-grounded material used by the structure prompt.
func BuildMaterial(a Input) string {
	body := func(value string) string {
		if utf16Length(value) <= MaxBodyChars {
			return value
		}
		return truncateUTF16(value, MaxBodyChars) + fmt.Sprintf("\n【原文超过 %d 字符，后文未提供】", MaxBodyChars)
	}
	lines := []string{
		"<source>",
		"名称：" + a.Source.Name,
		fmt.Sprintf("类型：%s；分级：%s；一手来源：%s", kindLabel(a.Source.Kind), a.Source.Tier, yesNo(a.Source.FirstParty)),
		"</source>",
		"<material>",
	}
	if a.PublishedAt != nil {
		at := a.PublishedAt.Add(8 * time.Hour).UTC()
		lines = append(lines, fmt.Sprintf("发布时间：%s %s（北京时间）", at.Format("2006-01-02"), at.Format("15:04")))
	}
	if a.Author != "" {
		lines = append(lines, "作者："+a.Author)
	}
	if a.XPost != nil {
		lines = append(lines,
			fmt.Sprintf("作者：%s (@%s)", a.XPost.AuthorName, a.XPost.Handle),
			"帖子：\n"+body(firstNonEmpty(a.XPost.Text, a.Title)))
		if a.XPost.Quoted != nil && a.XPost.Quoted.Text != "" {
			lines = append(lines, fmt.Sprintf("引用的帖子（@%s）：\n%s", a.XPost.Quoted.Handle, body(a.XPost.Quoted.Text)))
		}
		if a.TranslationZH != nil && *a.TranslationZH != "" {
			lines = append(lines, "帖子中文译文：\n"+truncateRunes(*a.TranslationZH, 4000))
		}
	} else {
		lines = append(lines, "标题："+collapseWhitespace(a.Title))
		original := nullish(a.BodyText, a.Excerpt)
		if original != "" {
			lines = append(lines, "正文：\n"+body(original))
		} else {
			lines = append(lines, "正文：（无）")
		}
		if a.TranslationZH != nil && *a.TranslationZH != "" && (a.BodyText == nil || *a.BodyText == "") {
			lines = append(lines, "正文中文译文：\n"+truncateRunes(*a.TranslationZH, 5000))
		}
	}
	lines = append(lines, "原文链接："+a.URL, "</material>")
	return strings.Join(lines, "\n")
}

// BuildScoreInput renders the scoring prompt input, which intentionally excludes source facts.
func BuildScoreInput(a Input) string {
	var body string
	if a.XPost != nil {
		quoted := ""
		if a.XPost.Quoted != nil && a.XPost.Quoted.Text != "" {
			label := "原推文"
			if a.XPost.Quoted.Handle != "" {
				label = "@" + a.XPost.Quoted.Handle
			}
			quoted = fmt.Sprintf("\n\n[引用 %s]：%s", label, a.XPost.Quoted.Text)
		}
		body = strings.TrimSpace(strings.TrimSpace(a.XPost.Text) + quoted)
	} else {
		body = strings.TrimSpace(nullish(a.BodyText, a.Excerpt))
	}
	if body == "" {
		body = a.Title
	}
	if utf16Length(body) > MaxBodyChars {
		body = truncateUTF16(body, MaxBodyChars)
	}
	published := "未知（收录时间不代表发布时间）"
	if a.PublishedAt != nil {
		at := a.PublishedAt.Add(8 * time.Hour).UTC()
		published = at.Format("2006-01-02T15:04:05")
		millis := a.PublishedAt.Nanosecond() / int(time.Millisecond)
		if millis != 0 {
			published += fmt.Sprintf(".%03d", millis)
		}
		published += "+08:00"
	}
	return strings.Join([]string{
		"请按系统规则评估以下单篇材料所代表的事件。只输出 attentionScore。",
		"【发布时间（北京时间）】\n" + published,
		"【标题】\n" + strings.TrimSpace(a.Title),
		"【完整正文】\n" + body,
	}, "\n\n")
}

// RenderContext returns the user context shared by prefilter and understand prompts.
func RenderContext(a Input, annotateQuoted bool) string {
	lines := []string{fmt.Sprintf("【来源】%s（%s，tier=%s）", a.Source.Name, a.Source.Kind, firstNonEmpty(a.Source.Tier, "未分级"))}
	if len(a.Source.Tags) > 0 {
		lines = append(lines, "【来源标签】"+strings.Join(a.Source.Tags, ", "))
	}
	name := firstNonEmpty(pointerValue(a.XPost, func(p *XPost) string { return p.AuthorName }), a.Author)
	handle := pointerValue(a.XPost, func(p *XPost) string { return p.Handle })
	if name != "" || handle != "" {
		author := name
		if handle != "" {
			if author != "" {
				author += " · "
			}
			author += "@" + handle
		}
		lines = append(lines, "【作者】"+author)
	}
	if a.PublishedAt != nil {
		lines = append(lines, "【发布时间】"+a.PublishedAt.UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z"))
	}
	media := a.Media
	if a.XPost != nil && a.XPost.Media != nil {
		media = a.XPost.Media
	}
	images, videos := 0, 0
	for _, item := range media {
		switch item.Kind {
		case "image":
			images++
		case "video":
			videos++
		}
	}
	mediaParts := make([]string, 0, 3)
	if images > 0 {
		mediaParts = append(mediaParts, fmt.Sprintf("%d 张图", images))
	}
	if videos > 0 {
		mediaParts = append(mediaParts, fmt.Sprintf("%d 个视频", videos))
	}
	if a.XPost != nil && a.BodyStatus != "ok" && onlyXArticleLink(a.XPost.Text) {
		mediaParts = append(mediaParts, "含 X 长文链接（正文未抓到）")
	}
	if len(mediaParts) > 0 {
		lines = append(lines, "【媒体】"+strings.Join(mediaParts, " · "))
	}
	lines = append(lines, "【原文链接】"+a.URL, "【标题】"+a.Title)
	var quoted *QuotedPost
	if a.XPost != nil && a.XPost.Quoted != nil && a.XPost.Quoted.Text != "" {
		quoted = a.XPost.Quoted
	}
	if quoted != nil {
		label := "原推"
		if quoted.Handle != "" {
			label = "@" + quoted.Handle
		}
		if annotateQuoted {
			lines = append(lines, fmt.Sprintf("【引用 %s】（以下是作者转发/引用的**他人**内容，不是作者本人的产出）", label), quoted.Text)
		} else {
			lines = append(lines, fmt.Sprintf("【引用 %s】%s", label, quoted.Text))
		}
	}
	lines = append(lines, "")
	if annotateQuoted && quoted != nil {
		lines = append(lines, "【正文（作者自己的内容）】")
	} else {
		lines = append(lines, "【正文】")
	}
	mainText := nullish(a.BodyText, a.Excerpt)
	if a.XPost != nil {
		mainText = firstNonEmpty(a.XPost.Text, a.Title)
	} else if a.BodyText == nil && a.Excerpt == nil {
		mainText = "(无正文)"
	}
	lines = append(lines, capBody(mainText), "", "【材料质量】"+materialQuality(a))
	return strings.Join(lines, "\n")
}

func PrefilterUser(a Input) string {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(RenderContext(a, false))
	return strings.NewReplacer(`\u2028`, "\u2028", `\u2029`, "\u2029").Replace(strings.TrimSuffix(encoded.String(), "\n"))
}

func MissingEvidence(a Input) bool {
	return strings.TrimSpace(pointerString(a.BodyText)) == "" && strings.TrimSpace(pointerString(a.Excerpt)) == "" &&
		strings.TrimSpace(pointerValue(a.XPost, func(p *XPost) string { return p.Text })) == "" &&
		strings.TrimSpace(pointerValue(a.XPost, func(p *XPost) string {
			if p.Quoted != nil {
				return p.Quoted.Text
			}
			return ""
		})) == ""
}

func UnderstandUser(a Input) string {
	return "请按系统规则理解以下单篇材料，一次返回全部六个字段。\n\n" + RenderContext(a, true)
}

func ScoreInputTime(at time.Time) string {
	at = at.UTC()
	beijing := at.Add(8 * time.Hour)
	value := beijing.Format("2006-01-02T15:04:05")
	millis := at.Nanosecond() / int(time.Millisecond)
	if millis != 0 {
		value += fmt.Sprintf(".%03d", millis)
	}
	return value + "+08:00"
}

func onlyXArticleLink(value string) bool {
	return xArticleLink.MatchString(value) && len(strings.TrimSpace(xArticleLink.ReplaceAllString(value, ""))) < 30
}

func materialQuality(a Input) string {
	if a.XPost != nil {
		return "完整正文（来自 RSS / API 自带的 content 字段）"
	}
	if a.BodyText != nil && *a.BodyText != "" {
		if a.Source.FetchesBody {
			return "完整正文（抓自原始网页）"
		}
		return "完整正文（来自 RSS / API 自带的 content 字段）"
	}
	if a.Excerpt != nil && *a.Excerpt != "" {
		return "仅摘要（feed 未提供完整正文）"
	}
	if a.BodyStatus == "unconfirmed" {
		return "抓取失败，仅标题可用"
	}
	return "无有效文本"
}

func kindLabel(kind string) string {
	if value, ok := kindLabels[kind]; ok {
		return value
	}
	return kind
}

func yesNo(value bool) string {
	if value {
		return "是"
	}
	return "否"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func nullish(primary, fallback *string) string {
	if primary != nil {
		return *primary
	}
	if fallback != nil {
		return *fallback
	}
	return ""
}

func pointerString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func pointerValue[T any](value *T, get func(*T) string) string {
	if value == nil {
		return ""
	}
	return get(value)
}

func collapseWhitespace(value string) string {
	return strings.Join(strings.FieldsFunc(value, func(r rune) bool { return unicode.IsSpace(r) || r == '\uFEFF' }), " ")
}

func utf16Length(value string) int { return len(utf16.Encode([]rune(value))) }

func truncateUTF16(value string, max int) string {
	units := 0
	for offset, r := range value {
		width := 1
		if r > 0xFFFF {
			width = 2
		}
		if units+width > max {
			return value[:offset]
		}
		units += width
	}
	return value
}

func truncateRunes(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	if max <= 0 {
		return "…"
	}
	return strings.TrimRightFunc(string(runes[:max-1]), unicode.IsSpace) + "…"
}

func capBody(value string) string {
	if utf16Length(value) <= MaxBodyChars {
		return value
	}
	return truncateUTF16(value, MaxBodyChars)
}
