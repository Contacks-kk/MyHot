package admincontent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode"

	"aihot/backend-go/internal/materials"
)

var xEncodingEntity = regexp.MustCompile(`https?://\S+|&(?:amp|lt|gt);`)
var xEncodingHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type XEncodingRepairRepository interface {
	/** PreviewXEncodingRepair loads the proof material without mutation. */
	PreviewXEncodingRepair(context.Context, string) (*XEncodingPlan, error)
	/** NormalizeXEncoding applies an approved plan and returns whether the article exists. */
	NormalizeXEncoding(context.Context, string, XEncodingRepairInput, string) (*XEncodingRepairResult, bool, error)
}

type XEncodingRepairInput struct {
	Version   int    `json:"version"`
	Hash      string `json:"hash"`
	RequestID string `json:"requestId"`
	Reason    string `json:"reason"`
}

type XEncodingRepairResult struct {
	ArticleID string `json:"articleId"`
	Revision  int    `json:"revision"`
	Status    string `json:"status"`
}

type XEncodingMaterial struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"`
	Identity    string          `json:"identity_key"`
	URL         string          `json:"url"`
	Revision    int             `json:"revision"`
	ContentHash *string         `json:"content_hash"`
	Title       string          `json:"title"`
	BodyText    *string         `json:"body_text"`
	Excerpt     *string         `json:"excerpt"`
	XPost       json.RawMessage `json:"x_post"`
	XArticle    json.RawMessage `json:"x_article"`
}

type XEncodingSnapshot struct {
	Title       string          `json:"title"`
	BodyText    *string         `json:"body_text"`
	XPost       json.RawMessage `json:"x_post"`
	ContentHash *string         `json:"content_hash"`
}

type XEncodingPlan struct {
	ArticleID      string            `json:"articleId"`
	Version        int               `json:"version"`
	Hash           string            `json:"hash"`
	Changed        bool              `json:"changed"`
	PreservedTitle bool              `json:"preservedTitle"`
	Before         XEncodingSnapshot `json:"before"`
	After          XEncodingSnapshot `json:"after"`
}

type xEncodingPost map[string]json.RawMessage

type xEncodingArticle struct {
	Title *string `json:"title"`
	Text  *string `json:"text"`
}

/** PreviewXEncodingRepair returns the current proof-bound repair plan for one article. */
func (s *Service) PreviewXEncodingRepair(ctx context.Context, id string) (*XEncodingPlan, error) {
	if s.xEncoding == nil {
		return nil, ErrXEncodingRepairUnavailable
	}
	if !articleIDPattern.MatchString(id) {
		return nil, ErrInvalidCommand
	}
	return s.xEncoding.PreviewXEncodingRepair(ctx, id)
}

/** NormalizeXEncoding validates operator approval before committing the reviewed repair. */
func (s *Service) NormalizeXEncoding(ctx context.Context, id string, input XEncodingRepairInput, actor string) (*XEncodingRepairResult, bool, error) {
	if s.xEncoding == nil {
		return nil, false, ErrXEncodingRepairUnavailable
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if !articleIDPattern.MatchString(id) || input.Version < 1 || !xEncodingHashPattern.MatchString(input.Hash) ||
		!requestIDPattern.MatchString(input.RequestID) || input.Reason == "" || strings.TrimSpace(actor) == "" {
		return nil, false, ErrInvalidCommand
	}
	return s.xEncoding.NormalizeXEncoding(ctx, id, input, actor)
}

/** PlanXEncodingRepair previews a single-pass repair of confirmed provider text. */
func PlanXEncodingRepair(material XEncodingMaterial) (XEncodingPlan, error) {
	if material.Kind != "x_search" {
		return XEncodingPlan{}, errors.New("only X source material can be normalized")
	}
	var post xEncodingPost
	if err := json.Unmarshal(material.XPost, &post); err != nil || len(post) == 0 {
		return XEncodingPlan{}, errors.New("X post has an unsupported shape")
	}
	var tweetID string
	var postText *string
	if json.Unmarshal(post["tweetId"], &tweetID) != nil || tweetID == "" || json.Unmarshal(post["text"], &postText) != nil || postText == nil {
		return XEncodingPlan{}, errors.New("X post has an unsupported shape")
	}
	identity, ok := materials.IdentityKeyForURL(material.URL)
	if !ok || material.Identity != "x:"+tweetID || identity != material.Identity {
		return XEncodingPlan{}, errors.New("X material identity does not match its post")
	}
	nextPost := make(xEncodingPost, len(post))
	for key, value := range post {
		nextPost[key] = value
	}
	decodedText, err := json.Marshal(decodeStoredXPost(*postText))
	if err != nil {
		return XEncodingPlan{}, err
	}
	nextPost["text"] = decodedText
	if len(post["quoted"]) > 0 && string(post["quoted"]) != "null" {
		var quoted map[string]json.RawMessage
		if err := json.Unmarshal(post["quoted"], &quoted); err != nil {
			return XEncodingPlan{}, errors.New("X quote text has an unsupported shape")
		}
		var handle *string
		var quoteText *string
		if json.Unmarshal(quoted["handle"], &handle) != nil || handle == nil || json.Unmarshal(quoted["text"], &quoteText) != nil || quoteText == nil {
			return XEncodingPlan{}, errors.New("X quote text has an unsupported shape")
		}
		decoded, err := json.Marshal(decodeStoredXPost(*quoteText))
		if err != nil {
			return XEncodingPlan{}, err
		}
		quoted["text"] = decoded
		nextQuoted, err := json.Marshal(quoted)
		if err != nil {
			return XEncodingPlan{}, err
		}
		nextPost["quoted"] = nextQuoted
	}
	nextPostJSON, err := json.Marshal(nextPost)
	if err != nil {
		return XEncodingPlan{}, err
	}
	canonicalPost, err := canonicalXJSON(material.XPost)
	if err != nil {
		return XEncodingPlan{}, err
	}
	canonicalNextPost, err := canonicalXJSON(nextPostJSON)
	if err != nil {
		return XEncodingPlan{}, err
	}
	oldBase, err := xPostBody(post)
	if err != nil {
		return XEncodingPlan{}, err
	}
	nextBase, err := xPostBody(nextPost)
	if err != nil {
		return XEncodingPlan{}, err
	}
	tail, err := xEncodingArticleTail(material.XArticle)
	if err != nil {
		return XEncodingPlan{}, err
	}
	if tail != "" {
		oldBase = joinNonempty(oldBase, tail)
	}
	storedBody := ""
	if material.BodyText != nil {
		storedBody = *material.BodyText
	}
	if storedBody != oldBase {
		return XEncodingPlan{}, errors.New("stored body is not the confirmed X post/quote/article composition")
	}
	if tail != "" {
		nextBase = joinNonempty(nextBase, tail)
	}
	nextBody := &nextBase
	if material.BodyText == nil && nextBase == "" {
		nextBody = nil
	}
	title := material.Title
	firstLine := firstNonemptyLine(*postText)
	truncated := strings.TrimSuffix(strings.TrimSuffix(material.Title, "..."), "…")
	sourceTitle := material.Title == firstLine || (truncated != material.Title && strings.HasPrefix(firstLine, truncated))
	if sourceTitle {
		title = decodeStoredXPost(material.Title)
	}
	changed := title != material.Title || nextBase != storedBody || !bytes.Equal(canonicalNextPost, canonicalPost)
	var nextHash *string
	if changed {
		excerpt := ""
		if material.Excerpt != nil {
			excerpt = *material.Excerpt
		}
		hash := materials.ContentHash(title, nextBase, excerpt)
		nextHash = &hash
	} else {
		nextHash = material.ContentHash
	}
	materialHash, err := xEncodingMaterialHash(material)
	if err != nil {
		return XEncodingPlan{}, err
	}
	return XEncodingPlan{
		ArticleID:      material.ID,
		Version:        material.Revision,
		Hash:           materialHash,
		Changed:        changed,
		PreservedTitle: !sourceTitle,
		Before: XEncodingSnapshot{
			Title:       material.Title,
			BodyText:    material.BodyText,
			XPost:       material.XPost,
			ContentHash: material.ContentHash,
		},
		After: XEncodingSnapshot{
			Title:       title,
			BodyText:    nextBody,
			XPost:       nextPostJSON,
			ContentHash: nextHash,
		},
	}, nil
}

/** xEncodingMaterialHash binds approval to every value used by the repair plan. */
func xEncodingMaterialHash(material XEncodingMaterial) (string, error) {
	postJSON, err := canonicalXJSON(material.XPost)
	if err != nil {
		return "", err
	}
	articleJSON, err := canonicalXJSON(material.XArticle)
	if err != nil {
		return "", err
	}
	post := json.RawMessage(postJSON)
	article := json.RawMessage(articleJSON)
	bound := struct {
		ID          string          `json:"id"`
		Kind        string          `json:"kind"`
		Identity    string          `json:"identity"`
		URL         string          `json:"url"`
		Revision    int             `json:"revision"`
		ContentHash *string         `json:"contentHash"`
		Title       string          `json:"title"`
		Body        *string         `json:"body"`
		Excerpt     *string         `json:"excerpt"`
		Post        json.RawMessage `json:"post"`
		Article     json.RawMessage `json:"article"`
	}{
		ID:          material.ID,
		Kind:        material.Kind,
		Identity:    material.Identity,
		URL:         material.URL,
		Revision:    material.Revision,
		ContentHash: material.ContentHash,
		Title:       material.Title,
		Body:        material.BodyText,
		Excerpt:     material.Excerpt,
		Post:        post,
		Article:     article,
	}
	raw, err := json.Marshal(bound)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:]), nil
}

/** decodeStoredXPost decodes only the three provider-escaped entities and leaves URL bytes intact. */
func decodeStoredXPost(value string) string {
	return xEncodingEntity.ReplaceAllStringFunc(value, func(part string) string {
		if strings.HasPrefix(part, "http://") || strings.HasPrefix(part, "https://") {
			return part
		}
		switch part {
		case "&amp;":
			return "&"
		case "&lt;":
			return "<"
		case "&gt;":
			return ">"
		default:
			return part
		}
	})
}

/** xPostBody reconstructs the provider's stored post and quote composition. */
func xPostBody(post xEncodingPost) (string, error) {
	var postText *string
	if json.Unmarshal(post["text"], &postText) != nil || postText == nil {
		return "", errors.New("X post has an unsupported shape")
	}
	body := *postText
	if len(post["quoted"]) == 0 || string(post["quoted"]) == "null" {
		return trimXWhitespace(body), nil
	}
	var quoted map[string]json.RawMessage
	if json.Unmarshal(post["quoted"], &quoted) != nil {
		return "", errors.New("X quote text has an unsupported shape")
	}
	var handle *string
	var quoteText *string
	if json.Unmarshal(quoted["handle"], &handle) != nil || handle == nil || json.Unmarshal(quoted["text"], &quoteText) != nil || quoteText == nil {
		return "", errors.New("X quote text has an unsupported shape")
	}
	quote := "【引用 @" + *handle + "】" + *quoteText
	return trimXWhitespace(body + "\n\n" + quote), nil
}

/** xEncodingArticleTail preserves appended long-form text outside the provider post. */
func xEncodingArticleTail(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var article xEncodingArticle
	if err := json.Unmarshal(raw, &article); err != nil {
		return "", errors.New("X article has an unsupported shape")
	}
	parts := make([]string, 0, 2)
	if article.Title != nil && *article.Title != "" {
		parts = append(parts, "# "+*article.Title)
	}
	if article.Text != nil && *article.Text != "" {
		parts = append(parts, *article.Text)
	}
	return strings.Join(parts, "\n\n"), nil
}

/** firstNonemptyLine returns the normalized first content line used by source-title detection. */
func firstNonemptyLine(value string) string {
	for _, line := range strings.Split(value, "\n") {
		if trimXWhitespace(line) != "" {
			return materials.CollapseWhitespace(line)
		}
	}
	return materials.CollapseWhitespace(value)
}

/** trimXWhitespace matches JavaScript trim for provider text, including the byte order mark. */
func trimXWhitespace(value string) string {
	return strings.TrimFunc(value, func(character rune) bool {
		return unicode.IsSpace(character) || character == '\uFEFF'
	})
}

/** joinNonempty joins present body sections with the collector's blank-line separator. */
func joinNonempty(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, "\n\n")
}

/** canonicalXJSON normalizes JSONB bytes and treats absent optional data as null. */
func canonicalXJSON(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 {
		return []byte("null"), nil
	}
	return canonicalJSON(raw)
}
