package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf16"

	"aihot/backend-go/internal/receipts"
)

var intervalShardQuery = regexp.MustCompile(`^from:([A-Za-z0-9_]{1,15}) -filter:replies$`)

type tweet struct {
	ID           string    `json:"id_str"`
	CreatedAt    string    `json:"tweet_created_at"`
	FullText     string    `json:"full_text"`
	Text         string    `json:"text"`
	Lang         string    `json:"lang"`
	User         xUser     `json:"user"`
	ReplyTo      string    `json:"in_reply_to_status_id_str"`
	QuotedStatus *tweet    `json:"quoted_status"`
	Retweeted    *tweet    `json:"retweeted_status"`
	Entities     xEntities `json:"entities"`
	Extended     xEntities `json:"extended_entities"`
	Favorite     *int      `json:"favorite_count"`
	Retweet      *int      `json:"retweet_count"`
	Views        *int      `json:"views_count"`
}

type xUser struct {
	Name      string `json:"name"`
	Handle    string `json:"screen_name"`
	AvatarURL string `json:"profile_image_url_https"`
}

type xEntities struct {
	URLs  []xEntityURL `json:"urls"`
	Media []xMedia     `json:"media"`
}

type xEntityURL struct {
	URL         string `json:"url"`
	ExpandedURL string `json:"expanded_url"`
}

type xMedia struct {
	Type         string     `json:"type"`
	URL          string     `json:"media_url_https"`
	OriginalInfo xMediaSize `json:"original_info"`
}

type xMediaSize struct {
	Width  *int `json:"width"`
	Height *int `json:"height"`
}

type xMediaData struct {
	Kind   string  `json:"kind"`
	URL    string  `json:"url"`
	Width  *int    `json:"width"`
	Height *int    `json:"height"`
	Poster *string `json:"poster"`
}

type xQuotedData struct {
	AuthorName string       `json:"authorName"`
	Handle     string       `json:"handle"`
	Text       string       `json:"text"`
	URL        string       `json:"url"`
	Media      []xMediaData `json:"media"`
}

type xPostData struct {
	TweetID    string       `json:"tweetId"`
	AuthorName string       `json:"authorName"`
	Handle     string       `json:"handle"`
	AvatarURL  *string      `json:"avatarUrl"`
	Text       string       `json:"text"`
	Lang       *string      `json:"lang"`
	ReplyTo    *string      `json:"replyTo"`
	Media      []xMediaData `json:"media"`
	Quoted     *xQuotedData `json:"quoted"`
}

type xRead struct {
	Tweets       []tweet
	ReceiptIDs   []int64
	LastID       string
	Backlog      []XBacklog
	Pages        int
	Truncated    bool
	BacklogPages int
	Dropped      int
}

type XBacklog struct {
	Query string `json:"query"`
	Next  string `json:"next"`
}

const (
	xMaxPages        = 10
	xMaxBacklogPages = 10
	xMaxBacklog      = 5
)

var (
	xArticleLink   = regexp.MustCompile(`(?i)https?://(?:www\.)?(?:x|twitter)\.com/i/article/\d+`)
	xTrailingMedia = regexp.MustCompile(`\s*https://t\.co/\w+\s*$`)
)

func xText(t tweet) string {
	text := t.FullText
	if text == "" {
		text = t.Text
	}
	text = strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">").Replace(text)
	urls := t.Entities.URLs
	for _, entry := range urls {
		if entry.URL != "" && entry.ExpandedURL != "" {
			text = strings.ReplaceAll(text, entry.URL, entry.ExpandedURL)
		}
	}
	media := t.Extended.Media
	if len(media) == 0 {
		media = t.Entities.Media
	}
	if len(media) > 0 {
		text = xTrailingMedia.ReplaceAllString(text, "")
	}
	return strings.TrimSpace(text)
}

func xMediaItems(t tweet) []xMediaData {
	items := t.Extended.Media
	if len(items) == 0 {
		items = t.Entities.Media
	}
	result := make([]xMediaData, 0, len(items))
	for _, media := range items {
		kind := "video"
		var poster *string
		if media.Type == "photo" {
			kind = "image"
		} else {
			poster = stringPointer(media.URL)
		}
		result = append(result, xMediaData{Kind: kind, URL: media.URL, Width: media.OriginalInfo.Width, Height: media.OriginalInfo.Height, Poster: poster})
	}
	return result
}

func toXPost(t tweet) xPostData {
	avatar := strings.Replace(t.User.AvatarURL, "_normal.", "_bigger.", 1)
	post := xPostData{TweetID: t.ID, AuthorName: t.User.Name, Handle: t.User.Handle, AvatarURL: stringPointer(avatar), Text: xText(t), Lang: stringPointer(t.Lang), ReplyTo: stringPointer(t.ReplyTo), Media: xMediaItems(t)}
	if quoted := t.QuotedStatus; quoted != nil {
		post.Quoted = &xQuotedData{AuthorName: quoted.User.Name, Handle: quoted.User.Handle, Text: xText(*quoted), URL: "https://x.com/" + quoted.User.Handle + "/status/" + quoted.ID, Media: xMediaItems(*quoted)}
	}
	return post
}

func tweetCandidate(t tweet) item {
	post := toXPost(t)
	firstLine := ""
	for _, line := range strings.Split(post.Text, "\n") {
		if strings.TrimSpace(line) != "" {
			firstLine = line
			break
		}
	}
	if firstLine == "" {
		firstLine = post.Text
	}
	if utf16Len(firstLine) > 140 {
		firstLine = truncateUTF16(firstLine, 137) + "..."
	}
	var published *time.Time
	if parsed, err := time.Parse(time.RFC3339Nano, t.CreatedAt); err == nil {
		value := parsed.UTC()
		published = &value
	}
	xpost, _ := json.Marshal(post)
	media, _ := json.Marshal(post.Media)
	raw, _ := json.Marshal(map[string]any{"favorite": t.Favorite, "retweet": t.Retweet, "views": t.Views})
	status := "ok"
	if xArticleLink.MatchString(post.Text) {
		status = "pending"
	}
	body := post.Text
	if post.Quoted != nil {
		body += "\n\n【引用 @" + post.Quoted.Handle + "】" + post.Quoted.Text
	}
	return item{URL: "https://x.com/" + t.User.Handle + "/status/" + t.ID, IdentityKey: "x:" + t.ID, Title: firstLine,
		Author: t.User.Handle, Language: t.Lang, Body: strings.TrimSpace(body), BodyStatus: status,
		Published: published, Media: media, XPost: xpost, Raw: raw}
}

func utf16Len(value string) int { return len(utf16.Encode([]rune(value))) }

func stringPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func (s *Service) readXSearch(ctx context.Context, base string, lastID string, backlog []XBacklog, subject, searchType string) (xRead, error) {
	if s.XProvider == nil {
		return xRead{}, errors.New("SocialData provider is not configured")
	}
	if searchType == "" {
		searchType = "Latest"
	}
	result := xRead{Backlog: append([]XBacklog(nil), backlog...)}
	window := xWindowKey(time.Now())
	search := func(query, cursor string) (XPage, error) {
		return s.XProvider.Search(ctx, query, searchType, cursor, subject, window)
	}
	query := base
	if lastID != "" {
		query += " since_id:" + lastID
	}
	cursor := ""
	for {
		page, err := search(query, cursor)
		if err != nil {
			if cursor == "" {
				return xRead{}, err
			}
			result.Truncated = true
			result.Backlog = append(result.Backlog, XBacklog{Query: query, Next: cursor})
			break
		}
		result.Pages++
		result.ReceiptIDs = appendReceiptID(result.ReceiptIDs, page.ReceiptID)
		result.Tweets = append(result.Tweets, page.Tweets...)
		if lastID == "" || page.NextCursor == "" || len(page.Tweets) == 0 {
			break
		}
		if result.Pages >= xMaxPages {
			result.Truncated = true
			result.Backlog = append(result.Backlog, XBacklog{Query: query, Next: page.NextCursor})
			break
		}
		cursor = page.NextCursor
	}
	for len(result.Backlog) > xMaxBacklog {
		result.Backlog = result.Backlog[1:]
		result.Dropped++
	}
	for len(result.Backlog) > 0 && result.BacklogPages < xMaxBacklogPages {
		stretch := &result.Backlog[0]
		page, err := search(stretch.Query, stretch.Next)
		if err != nil {
			var rejected xProviderError
			if errors.As(err, &rejected) && (rejected.status == httpStatusBadRequest || rejected.status == httpStatusUnprocessable) {
				result.Backlog = result.Backlog[1:]
				result.Dropped++
				continue
			}
			break
		}
		result.BacklogPages++
		result.ReceiptIDs = appendReceiptID(result.ReceiptIDs, page.ReceiptID)
		result.Tweets = append(result.Tweets, page.Tweets...)
		if page.NextCursor == "" || len(page.Tweets) == 0 {
			result.Backlog = result.Backlog[1:]
		} else {
			stretch.Next = page.NextCursor
		}
	}
	seen := map[string]bool{}
	filtered := result.Tweets[:0]
	for _, post := range result.Tweets {
		if post.Retweeted != nil || post.ID == "" || seen[post.ID] {
			continue
		}
		seen[post.ID] = true
		filtered = append(filtered, post)
	}
	result.Tweets = filtered
	result.LastID = maxTweetID(lastID, result.Tweets)
	return result, nil
}

const (
	httpStatusBadRequest    = 400
	httpStatusUnprocessable = 422
)

func appendReceiptID(ids []int64, id int64) []int64 {
	if id == 0 {
		return ids
	}
	for _, existing := range ids {
		if existing == id {
			return ids
		}
	}
	return append(ids, id)
}

func maxTweetID(lastID string, tweets []tweet) string {
	max := new(big.Int)
	if lastID != "" {
		if _, ok := max.SetString(lastID, 10); !ok {
			max.SetInt64(0)
		}
	}
	for _, post := range tweets {
		value, ok := new(big.Int).SetString(post.ID, 10)
		if ok && value.Cmp(max) > 0 {
			max = value
		}
	}
	if max.Sign() == 0 {
		return lastID
	}
	return max.String()
}

func cursorString(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case json.Number:
		return value.String()
	case float64:
		return fmt.Sprintf("%.0f", value)
	case int64:
		return fmt.Sprintf("%d", value)
	default:
		return ""
	}
}

func (s *Service) collectXSource(ctx context.Context, run *sourceRun) error {
	if unknown := unsupportedXConfig(run.Source.Config); len(unknown) > 0 {
		return s.failXRun(run, 0, 0, json.RawMessage(`{}`), fmt.Errorf("unsupported config: %s", strings.Join(unknown, ", ")))
	}
	firstImport := strings.TrimSpace(run.Source.Cursor.text("initializedAt")) == ""
	run.Source.First = firstImport
	handle := ""
	if self := selfThreadHandle(run.Source); self != "" {
		handle = self
	}
	base := run.Source.Config.text("query")
	if handle != "" {
		base = "from:" + handle + " (-filter:replies OR filter:self_threads)"
	}
	if base == "" {
		return s.failXRun(run, 0, 0, json.RawMessage(`{}`), errors.New("X search query is missing"))
	}
	lastID := cursorString(run.Source.Cursor["lastTweetId"])
	backlog := readXBacklog(run.Source.Cursor["xBacklog"])
	read, err := s.readXSearch(ctx, base, lastID, backlog, "source:"+run.Source.ID, run.Source.Config.text("searchType"))
	if err != nil {
		return s.failXRun(run, 0, 0, json.RawMessage(`{}`), err)
	}
	found := len(read.Tweets)
	items := make([]item, 0, found)
	for _, post := range read.Tweets {
		items = append(items, tweetCandidate(post))
	}
	items = admitXItems(items, run.Source.Config)
	items = filterItems(items, run.Source.Config, firstImport, time.Now().UTC())
	if run.Source.Config.boolean("sortByPublishedAt") {
		sort.SliceStable(items, func(i, j int) bool {
			if items[i].Published == nil {
				return false
			}
			if items[j].Published == nil {
				return true
			}
			return items[i].Published.After(*items[j].Published)
		})
	}
	created := 0
	for _, candidate := range items {
		added, storeErr := s.storeItem(ctx, run, candidate)
		if storeErr != nil {
			return s.failXRun(run, found, created, json.RawMessage(`{}`), storeErr)
		}
		if added {
			created++
		}
	}
	cursor := cloneSettings(run.Source.Cursor)
	if cursor == nil {
		cursor = settings{}
	}
	now := time.Now().UTC()
	if firstImport {
		cursor["initializedAt"] = isoMillis(now)
	}
	if read.LastID != "" {
		cursor["lastTweetId"] = read.LastID
	}
	if len(read.Backlog) > 0 {
		cursor["xBacklog"] = read.Backlog
	} else {
		delete(cursor, "xBacklog")
	}
	cursor["lastOkAt"] = isoMillis(now)
	cursorJSON, _ := json.Marshal(cursor)
	detail, _ := json.Marshal(map[string]any{"pages": read.Pages, "truncated": read.Truncated, "backlog": len(read.Backlog), "backlogPages": read.BacklogPages, "dropped": read.Dropped})
	countsFound, countsCreated := map[string]int{run.Source.ID: found}, map[string]int{run.Source.ID: created}
	finishCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Repository.FinishXSuccess(finishCtx, []*sourceRun{run}, countsFound, countsCreated, map[string]json.RawMessage{run.Source.ID: cursorJSON}, detail, read.ReceiptIDs); err != nil {
		return err
	}
	return nil
}

func (s *Service) failXRun(run *sourceRun, found, created int, detail json.RawMessage, cause error) error {
	message := cause.Error()
	if len(message) > 1000 {
		message = message[:1000]
	}
	var budgetErr *receipts.BudgetExceededError
	budget := errors.As(cause, &budgetErr)
	finishCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Repository.FinishXFailure(finishCtx, []*sourceRun{run}, map[string]int{run.Source.ID: found}, map[string]int{run.Source.ID: created}, detail, message, budget); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", ErrFetchRecorded, message)
}

func (s *Service) DueXShards(ctx context.Context) ([]XShard, error) {
	if s.XProvider == nil {
		return nil, nil
	}
	return s.Repository.DueXShards(ctx)
}

func (s *Service) DeferXSources(ctx context.Context, sourceIDs []string) error {
	return s.Repository.DeferXSources(ctx, sourceIDs)
}

func (s *Service) CollectXShard(ctx context.Context, key string, sourceIDs []string) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	run, acquired, err := s.Repository.BeginXShardRun(ctx, sourceIDs)
	if err != nil || !acquired {
		return err
	}
	defer s.Repository.ReleaseXShard(run)
	if s.XProvider == nil {
		return s.failXShard(run, key, errors.New("SocialData provider is not configured"))
	}
	return s.collectXShard(ctx, run, key)
}

func (s *Service) collectXShard(ctx context.Context, run *xShardRun, key string) error {
	accounts := make([]xShardAccount, 0, len(run.Members))
	since := ""
	backlog := []XBacklog{}
	seenBacklog := map[string]bool{}
	for _, member := range run.Members {
		handle := shardHandleForSource(member.Source)
		if handle == "" {
			continue
		}
		accounts = append(accounts, xShardAccount{id: member.Source.ID, handle: handle, own: selfThreadHandle(member.Source) != ""})
		watermark := coveredTo(member.Source)
		if since == "" || compareTweetID(watermark, since) < 0 {
			since = watermark
		}
		for _, stretch := range readXBacklog(member.Source.Cursor["xBacklog"]) {
			key := stretch.Query + "\x00" + stretch.Next
			if !seenBacklog[key] {
				seenBacklog[key] = true
				backlog = append(backlog, stretch)
			}
		}
	}
	if len(accounts) == 0 {
		return nil
	}
	read, err := s.readXSearch(ctx, shardQueryForAccounts(accounts), since, backlog, "x-shard:"+key, "Latest")
	if err != nil {
		return s.failXShard(run, key, err)
	}
	detail, _ := json.Marshal(map[string]any{"shard": key, "accounts": len(accounts), "pages": read.Pages, "truncated": read.Truncated, "backlog": len(read.Backlog), "backlogPages": read.BacklogPages, "dropped": read.Dropped})
	found, created := map[string]int{}, map[string]int{}
	cursors := map[string]json.RawMessage{}
	now := time.Now().UTC()
	for _, member := range run.Members {
		handle := strings.ToLower(shardHandleForSource(member.Source))
		mine := make([]item, 0)
		for _, post := range read.Tweets {
			if strings.EqualFold(post.User.Handle, handle) {
				mine = append(mine, tweetCandidate(post))
			}
		}
		found[member.Source.ID] = len(mine)
		mine = admitXItems(mine, member.Source.Config)
		mine = filterItems(mine, member.Source.Config, false, now)
		for _, candidate := range mine {
			added, err := s.storeItem(ctx, member, candidate)
			if err != nil {
				return s.failXShard(run, key, err)
			}
			if added {
				created[member.Source.ID]++
			}
		}
		cursor := cloneSettings(member.Source.Cursor)
		if cursor == nil {
			cursor = settings{}
		}
		own := cursorString(cursor["lastTweetId"])
		cursor["lastTweetId"] = maxTweetID(own, []tweet{{ID: read.LastID}})
		cursor["lastOkAt"] = isoMillis(now)
		if len(read.Backlog) > 0 {
			cursor["xBacklog"] = read.Backlog
		} else {
			delete(cursor, "xBacklog")
		}
		cursors[member.Source.ID], _ = json.Marshal(cursor)
	}
	finishCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.Repository.FinishXSuccess(finishCtx, run.Members, found, created, cursors, detail, read.ReceiptIDs)
}

func (s *Service) failXShard(run *xShardRun, key string, cause error) error {
	message := cause.Error()
	if len(message) > 1000 {
		message = message[:1000]
	}
	var budgetErr *receipts.BudgetExceededError
	budget := errors.As(cause, &budgetErr)
	detail, _ := json.Marshal(map[string]any{"shard": key, "accounts": len(run.Members)})
	finishCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Repository.FinishXFailure(finishCtx, run.Members, map[string]int{}, map[string]int{}, detail, message, budget); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", ErrFetchRecorded, message)
}

func readXBacklog(value any) []XBacklog {
	data, err := json.Marshal(value)
	if err != nil || len(data) == 0 || string(data) == "null" {
		return nil
	}
	var backlog []XBacklog
	if json.Unmarshal(data, &backlog) != nil {
		return nil
	}
	return backlog
}

func cloneSettings(input settings) settings {
	if input == nil {
		return nil
	}
	copy := make(settings, len(input))
	for key, value := range input {
		copy[key] = value
	}
	return copy
}

func coveredTo(source source) string {
	own := cursorString(source.Cursor["lastTweetId"])
	if own == "" {
		return "0"
	}
	var cursor xCursor
	if raw, err := json.Marshal(source.Cursor); err == nil {
		_ = json.Unmarshal(raw, &cursor)
	}
	if cursor.LastOKAt == nil {
		return own
	}
	checked := xTweetIDAt(cursor.LastOKAt.Add(-10 * time.Minute))
	if compareTweetID(checked, own) > 0 {
		return checked
	}
	return own
}

func compareTweetID(a, b string) int {
	left, lok := new(big.Int).SetString(a, 10)
	right, rok := new(big.Int).SetString(b, 10)
	if !lok || !rok {
		return strings.Compare(a, b)
	}
	return left.Cmp(right)
}

func isoMillis(value time.Time) string { return value.UTC().Format("2006-01-02T15:04:05.000Z") }

func selfThreadHandle(source source) string {
	if source.Config.text("publisherRole") != "organization" && source.Config.text("publisherRole") != "person" {
		return ""
	}
	match := intervalShardQuery.FindStringSubmatch(source.Config.text("query"))
	if len(match) != 2 {
		return ""
	}
	return match[1]
}

func admitXItems(items []item, config settings) []item {
	kept := make([]item, 0, len(items))
	rewrite := config.object("itemUrlPrefixRewrite")
	from, to := rewrite.text("from"), rewrite.text("to")
	noise := config.object("ingestNoiseFilter")
	keepWords := lowerList(noise.list("keepIfMatches"))
	dropWords := lowerList(noise.list("dropMarkers"))
	dropTitle := lowerList(noise.list("dropMarkersTitleOnly"))
	for _, candidate := range items {
		if from != "" && to != "" && strings.HasPrefix(candidate.URL, from) {
			candidate.URL = to + strings.TrimPrefix(candidate.URL, from)
		}
		title := strings.ToLower(candidate.Title)
		haystack := title + "\n" + strings.ToLower(candidate.Excerpt)
		if containsAny(haystack, keepWords) {
			kept = append(kept, candidate)
			continue
		}
		if containsAny(title, dropTitle) || containsAny(haystack, dropWords) {
			continue
		}
		kept = append(kept, candidate)
	}
	return kept
}

func lowerList(values []string) []string {
	for i := range values {
		values[i] = strings.ToLower(values[i])
	}
	return values
}

func unsupportedXConfig(config settings) []string {
	allowed := map[string]bool{
		"publisherRole": true, "publisherUrlPrefixes": true, "_aihot": true,
		"ingestNoiseFilter": true, "itemUrlPrefixRewrite": true, "query": true, "searchType": true,
	}
	nested := map[string]map[string]bool{
		"_aihot":               {"initialBackfillLimit": true, "initialBackfillMonths": true},
		"ingestNoiseFilter":    {"dropMarkers": true, "dropMarkersTitleOnly": true, "keepIfMatches": true},
		"itemUrlPrefixRewrite": {"from": true, "to": true},
	}
	unsupported := []string{}
	for key, value := range config {
		if !allowed[key] {
			unsupported = append(unsupported, key)
			continue
		}
		switch key {
		case "publisherRole":
			role, _ := value.(string)
			if role != "organization" && role != "person" {
				unsupported = append(unsupported, "publisherRole="+fmt.Sprint(value))
			}
		case "searchType":
			searchType, _ := value.(string)
			if searchType != "Latest" && searchType != "Top" {
				unsupported = append(unsupported, "searchType="+fmt.Sprint(value))
			}
		case "publisherUrlPrefixes":
			prefixes, ok := value.([]any)
			if !ok {
				unsupported = append(unsupported, key)
				break
			}
			for _, prefix := range prefixes {
				text, ok := prefix.(string)
				parsed, err := url.Parse(text)
				if !ok || err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
					unsupported = append(unsupported, key)
					break
				}
			}
		default:
			if allowedNested, ok := nested[key]; ok {
				switch object := value.(type) {
				case map[string]any:
					for nestedKey := range object {
						if !allowedNested[nestedKey] {
							unsupported = append(unsupported, key+"."+nestedKey)
						}
					}
				case []any:
					for index := range object {
						unsupported = append(unsupported, fmt.Sprintf("%s.%d", key, index))
					}
				}
			}
		}
	}
	return unsupported
}
