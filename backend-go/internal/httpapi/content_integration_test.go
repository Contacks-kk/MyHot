package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/config"
)

/** Verify recommendation eligibility, quotas, content visibility and private article access. */
func TestTagRecommendationsIntegration(t *testing.T) {
	pool, orm := authTestPools(t)
	ctx := context.Background()
	prefix := fmt.Sprintf("rec_%d", time.Now().UnixNano())
	tag, alias := prefix+"_tag", prefix+"_alias"
	sources := make([]string, 5)
	var feeds []string
	var users []int64
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM user_accounts WHERE id=ANY($1)", users)
		_, _ = pool.Exec(ctx, "DELETE FROM articles WHERE source_id=ANY($1)", sources)
		_, _ = pool.Exec(ctx, "DELETE FROM sources WHERE id=ANY($1)", sources)
		_, _ = pool.Exec(ctx, "DELETE FROM interest_labels WHERE id=$1", alias)
		_, _ = pool.Exec(ctx, "DELETE FROM interest_labels WHERE id=$1", tag)
	})
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	for i := range sources {
		sources[i] = fmt.Sprintf("%s_s%d", prefix, i)
		feed := "https://fixture.example.invalid/" + sources[i] + ".xml"
		exec(`INSERT INTO sources(id,name,kind,config) VALUES($1,$1,'rss',jsonb_build_object('feedUrl',$2::text))`, sources[i], feed)
		if i < 4 {
			feeds = append(feeds, feed)
		}
	}
	exec(`INSERT INTO interest_labels(id,name,name_key,kind,definition,status) VALUES($1,$1,$1,'topic','fixture','active')`, tag)
	exec(`INSERT INTO interest_labels(id,name,name_key,kind,definition,status,merged_into) VALUES($1,$1,$1,'topic','fixture','merged',$2)`, alias, tag)
	server := New(config.Config{SourceSchedule: config.SourceSchedule{RecommendationFeeds: feeds}}, pool, orm, nil)
	redisStore := attachTestRedis(t, server)
	clients := make([]authClient, 2)
	for i := range clients {
		var id int64
		if err := pool.QueryRow(ctx, `INSERT INTO user_accounts(email,password_hash,verified_at)
			VALUES($1,'fixture',now()) RETURNING id`, fmt.Sprintf("%s_%d@example.invalid", prefix, i)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		users = append(users, id)
		token := seedTestUserSession(t, redisStore, id, "csrf", time.Hour)
		exec(`INSERT INTO user_interests(user_id,label_ids) VALUES($1,$2)`, id, []string{tag, alias})
		clients[i] = authClient{handler: server.Handler(), cookie: testCookie(token), csrf: "csrf"}
	}
	exec(`INSERT INTO user_source_subscriptions(user_id,source_id,name) VALUES($1,$2,$2)`, users[0], sources[0])

	add := func(suffix string, source int, hours int, backfill, hash string) string {
		id := prefix + "_" + suffix
		exec(`INSERT INTO articles(id,source_id,identity_key,url,title,excerpt,content_hash,discovered_at,published_at,timeline_at,backfill,backfill_reason)
			VALUES($1,$2,$1,'https://fixture.example.invalid/'||$1,$1,'source excerpt',nullif($4,''),now(),
				now()-make_interval(hours=>$3),now(),$5<>'',nullif($5,''))`, id, sources[source], hours, hash, backfill)
		exec("INSERT INTO article_interest_labels(article_id,label_id) VALUES($1,$2)", id, tag)
		return id
	}
	publish := func(id string, visibility string, pending bool) {
		exec(`INSERT INTO publications(article_id,source_id,title,summary,url,channel,discovered_at,timeline_at,sort_at,visibility,eligible,score,visible_after)
			SELECT id,source_id,title,'publication summary',url,'news',discovered_at,timeline_at,timeline_at,$2,true,94,
				CASE WHEN $3 THEN now()+interval '1 day' END FROM articles WHERE id=$1`, id, visibility, pending)
	}
	duplicateHash := strings.Repeat("a", 64)
	published := add("public", 1, 10, "", "")
	owned := add("owned", 0, 1, "", duplicateHash)
	otherPrivate := add("other", 0, 2, "", "")
	warmHash := strings.Repeat("b", 64)
	warm := add("warm", 2, 3, "first-import", warmHash)
	extraA := add("extra_a", 0, 3, "", "")
	add("extra_a2", 0, 4, "", "")
	extraB := add("extra_b", 1, 6, "", "")
	extraC := add("extra_c", 2, 7, "", "")
	embargo := add("embargo", 2, 0, "", "")
	withdrawn := add("withdrawn", 2, 0, "", "")
	for _, record := range []struct {
		id   string
		user int64
	}{{owned, users[0]}, {otherPrivate, users[1]}} {
		exec(`INSERT INTO user_article_results(user_id,article_id,input_revision,model,relevance_score,title,summary) VALUES($1,$2,1,'fixture',100,'private title','private summary')`, record.user, record.id)
	}
	publish(published, "public", false)
	publish(embargo, "public", true)
	publish(withdrawn, "withdrawn", false)
	excluded := []string{owned, embargo, withdrawn, add("copy", 2, 0, "", duplicateHash),
		add("old", 2, 192, "first-import", ""), add("backfill", 2, 0, "archive", ""), add("unapproved", 4, 0, "", "")}
	var response struct {
		Items   []recommendedArticleResponse
		Page    int
		HasMore bool
	}
	body := clients[0].request(t, "GET", "/api/me/recommended-articles?limit=50", nil, 200).Body.Bytes()
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	if response.HasMore || len(response.Items) != 6 || response.Items[0].ArticleID != published ||
		response.Items[0].Score == nil || *response.Items[0].Score != 96 {
		t.Fatalf("unexpected recommended list: %s", body)
	}
	firstPage := []string{published, otherPrivate, extraA, warm, extraB, extraC}
	counts := map[string]int{}
	for i, item := range response.Items {
		if item.ArticleID != firstPage[i] {
			t.Fatalf("page 1 item %d: got %s, want %s", i, item.ArticleID, firstPage[i])
		}
		counts[item.SourceID]++
		if counts[item.SourceID] > 2 || slices.Contains(excluded, item.ArticleID) || strings.Contains(item.Summary, "private") {
			t.Fatalf("ineligible recommendation: %+v", item)
		}
		if item.ArticleID == otherPrivate && (item.Score != nil || item.Summary != "source excerpt" || !item.IsSubscribed) {
			t.Fatalf("private data or subscription flag leaked: %+v", item)
		}
	}
	if !strings.Contains(string(body), otherPrivate) || !strings.Contains(string(body), warm) {
		t.Fatal("unseen article from a followed source or recent first import is missing")
	}
	clients[0].request(t, "GET", "/api/me/articles/"+otherPrivate, nil, 404)
	clients[1].request(t, "GET", "/api/me/articles/"+otherPrivate, nil, 200)
	tagsOnly := clients[1].request(t, "GET", "/api/me/recommended-articles", nil, 200)
	if err := json.Unmarshal(tagsOnly.Body.Bytes(), &response); err != nil || len(response.Items) == 0 {
		t.Fatalf("a user with tags and without source subscriptions got no recommendations: %s", tagsOnly.Body)
	}
	for _, item := range response.Items {
		if item.IsSubscribed || item.ArticleID == otherPrivate || strings.Contains(item.Summary, "private") {
			t.Fatalf("unexpected tag-only recommendation: %+v", item)
		}
	}
	// Exhaust the first candidate batch with the leading source, then verify the page spans batches.
	for i := 0; i < 25; i++ {
		add(fmt.Sprintf("over_quota_%02d", i), 0, 24, "", "")
	}
	add("duplicate_on_next_page", 3, 8, "", warmHash)
	next := []string{add("next_a", 3, 9, "", ""), add("next_b", 3, 10, "", "")}
	add("next_over_quota", 3, 11, "", "")
	for i, want := range [][]string{firstPage, next, {}} {
		page := i + 1
		result := clients[0].request(t, "GET", fmt.Sprintf("/api/me/recommended-articles?page=%d", page), nil, 200)
		if err := json.Unmarshal(result.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		got := make([]string, 0, len(response.Items))
		for _, item := range response.Items {
			got = append(got, item.ArticleID)
		}
		if !slices.Equal(got, want) || response.Page != page || response.HasMore != (page == 1) {
			t.Fatalf("recommendation page %d: got %v, hasMore=%v, want %v", page, got, response.HasMore, want)
		}
	}
}

/** TestDailyViewIntegration covers the compact directory, issue selection, and user isolation. */
func TestDailyViewIntegration(t *testing.T) {
	pool, orm := authTestPools(t)
	ctx := context.Background()
	server := New(config.Config{}, pool, orm, nil)
	store := attachTestRedis(t, server)
	var users [2]int64
	var clients [2]authClient
	for i := range users {
		if err := pool.QueryRow(ctx, "INSERT INTO user_accounts(email,password_hash,verified_at) VALUES($1,'unused',now()) RETURNING id",
			fmt.Sprintf("daily_view_%d_%d@example.invalid", time.Now().UnixNano(), i)).Scan(&users[i]); err != nil {
			t.Fatal(err)
		}
		id := users[i]
		t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM user_accounts WHERE id=$1", id) })
		clients[i] = authClient{handler: server.Handler(), cookie: testCookie(seedTestUserSession(t, store, id, "csrf", time.Hour))}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_daily_reports(user_id,report_date,model,title,summary,entries,created_at,manual_reports)
		VALUES ($1,'2026-01-01','fixture','old edition','old-private-body','[{"title":"old-entry"}]','2026-01-01T01:00:00Z','[]'),
		($1,'2026-01-02','fixture','scheduled edition','scheduled-private-body','[]','2026-01-02T01:00:00Z',
		'[{"id":"manual-900","date":"2026-01-02","model":"fixture","title":"latest edition",
		"summary":"latest-private-body","entries":[{"title":"latest-entry"}],"createdAt":"2026-01-02T02:00:00Z"}]')`, users[0]); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ query, id, body, other string }{
		{"", "manual-900", "latest-private-body", "old-private-body"},
		{"?id=scheduled-2026-01-01", "scheduled-2026-01-01", "old-private-body", "latest-private-body"},
		{"?date=2026-01-02", "manual-900", "latest-private-body", "scheduled-private-body"},
		{"?id=scheduled-2026-01-02&date=2026-01-01", "scheduled-2026-01-02", "scheduled-private-body", "old-private-body"},
	} {
		result := clients[0].request(t, "GET", "/api/me/daily/view"+tc.query, nil, 200)
		var view struct {
			Reports []map[string]json.RawMessage
			Report  struct{ ID, Summary string }
		}
		if err := json.Unmarshal(result.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		if len(view.Reports) != 3 || view.Report.ID != tc.id || view.Report.Summary != tc.body ||
			strings.Contains(result.Body.String(), tc.other) {
			t.Fatalf("unexpected daily view for %q: %s", tc.query, result.Body)
		}
		for _, heading := range view.Reports {
			if len(heading) != 4 || heading["id"] == nil || heading["date"] == nil || heading["title"] == nil || heading["createdAt"] == nil {
				t.Fatalf("invalid report heading: %v", heading)
			}
		}
	}
	clients[0].request(t, "GET", "/api/me/daily/view?id=missing", nil, 404)
	clients[0].request(t, "GET", "/api/me/daily/view?date=2026-02-30", nil, 404)
	clients[1].request(t, "GET", "/api/me/daily/view?id=manual-900", nil, 404)
	clients[1].request(t, "GET", "/api/me/daily/view?id=scheduled-2026-01-01", nil, 404)
	empty := clients[1].request(t, "GET", "/api/me/daily/view", nil, 200).Body.String()
	if !strings.Contains(empty, `"reports":[]`) || !strings.Contains(empty, `"report":null`) {
		t.Fatalf("invalid empty view: %s", empty)
	}
	anonymous := authClient{handler: server.Handler()}
	anonymous.request(t, "GET", "/api/me/daily/view", nil, 401)
}

func TestPersonalHotWindowOrderingAndHistoryIntegration(t *testing.T) {
	pool, orm := authTestPools(t)
	ctx := context.Background()
	prefix := fmt.Sprintf("content_%d", time.Now().UnixNano())
	source := prefix + "_source"
	if _, err := pool.Exec(ctx, "INSERT INTO sources(id,name,kind) VALUES($1,'Content fixture','external')", source); err != nil {
		t.Fatal(err)
	}
	users := []int64{}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM user_accounts WHERE id=ANY($1)", users)
		_, _ = pool.Exec(ctx, "DELETE FROM articles WHERE source_id=$1", source)
		_, _ = pool.Exec(ctx, "DELETE FROM sources WHERE id=$1", source)
	})
	server := New(config.Config{}, pool, orm, nil)
	redisStore := attachTestRedis(t, server)
	clients := []authClient{}
	for i := 0; i < 2; i++ {
		var id int64
		if err := pool.QueryRow(ctx, "INSERT INTO user_accounts(email,password_hash,verified_at) VALUES($1,'unused',now()) RETURNING id", fmt.Sprintf("%s_%d@example.invalid", prefix, i)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		users = append(users, id)
		token := seedTestUserSession(t, redisStore, id, "csrf", time.Hour)
		clients = append(clients, authClient{handler: server.Handler(), cookie: testCookie(token), csrf: "csrf"})
	}
	now := time.Now().UTC().Add(-time.Minute)
	for i := 0; i < 15; i++ {
		id := fmt.Sprintf("%s_a%02d", prefix, i)
		timestamp := now.Add(-time.Duration(i) * time.Hour)
		score := 90
		if i == 13 {
			timestamp = now.Add(-49 * time.Hour)
			score = 100
		}
		if i == 14 {
			timestamp = now.Add(2 * time.Hour)
			score = 100
		}
		if _, err := pool.Exec(ctx, "INSERT INTO articles(id,source_id,identity_key,url,title,published_at,discovered_at,timeline_at) VALUES($1,$2,$1,$3,$1,$4,$4,$4)", id, source, "https://fixture.example.invalid/"+id, timestamp); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, "INSERT INTO user_article_results(user_id,article_id,input_revision,model,relevance_score,title,summary) VALUES($1,$2,1,'fixture',$3,$2,'private-summary')", users[0], id, score); err != nil {
			t.Fatal(err)
		}
	}
	var response struct{ Items []contentCard }
	result := clients[0].request(t, "GET", "/api/me/hot", nil, 200)
	if err := json.Unmarshal(result.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 10 {
		t.Fatalf("expected top10 got%d", len(response.Items))
	}
	for i, card := range response.Items {
		if card.ArticleID != fmt.Sprintf("%s_a%02d", prefix, i) {
			t.Fatalf("tie ordering or48hwindow wrong:%s", card.ArticleID)
		}
		if card.Score != 90 {
			t.Fatal("out-of-window highest score leaked into hot")
		}
	}
	result = clients[1].request(t, "GET", "/api/me/hot", nil, 200)
	json.Unmarshal(result.Body.Bytes(), &response)
	if len(response.Items) != 0 {
		t.Fatal("hot crossed user boundary")
	}
	result = clients[1].request(t, "GET", "/api/me/feed", nil, 200)
	json.Unmarshal(result.Body.Bytes(), &response)
	if len(response.Items) != 0 {
		t.Fatal("feed crossed user boundary")
	}

	// A historical date includes both edges of its Beijing day, without the rolling top-ten cap.
	dayStart := time.Date(2000, 1, 1, 16, 0, 0, 0, time.UTC)
	for i := -1; i <= 57; i++ {
		id := fmt.Sprintf("%s_day_%02d", prefix, i)
		timestamp := dayStart.Add(time.Duration(i) * time.Minute)
		if i == 56 {
			timestamp = dayStart.Add(24*time.Hour - time.Second)
		}
		if i == 57 {
			timestamp = dayStart.Add(24 * time.Hour)
		}
		if _, err := pool.Exec(ctx, "INSERT INTO articles(id,source_id,identity_key,url,title,published_at,discovered_at,timeline_at) VALUES($1,$2,$1,$3,$1,$4,$4,$4)", id, source, "https://fixture.example.invalid/"+id, timestamp); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, "INSERT INTO user_article_results(user_id,article_id,input_revision,model,relevance_score,title,summary) VALUES($1,$2,1,'fixture',90,$2,'private-summary')", users[0], id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, "UPDATE articles SET published_at=NULL WHERE id=$1", prefix+"_day_00"); err != nil {
		t.Fatal(err)
	}
	var dayResponse struct {
		Items   []contentCard
		HasMore bool
	}
	seen := map[string]bool{}
	for page := 1; page <= 2; page++ {
		result = clients[0].request(t, "GET", fmt.Sprintf("/api/me/hot?range=date&date=2000-01-02&page=%d", page), nil, 200)
		if err := json.Unmarshal(result.Body.Bytes(), &dayResponse); err != nil {
			t.Fatal(err)
		}
		want := 50
		if page == 2 {
			want = 7
		}
		if len(dayResponse.Items) != want || dayResponse.HasMore != (page == 1) {
			t.Fatalf("historical page %d: got %d articles, hasMore=%v", page, len(dayResponse.Items), dayResponse.HasMore)
		}
		for _, item := range dayResponse.Items {
			if seen[item.ArticleID] || item.ArticleID == prefix+"_day_-1" || item.ArticleID == prefix+"_day_57" {
				t.Fatalf("duplicate or out-of-day result: %s", item.ArticleID)
			}
			seen[item.ArticleID] = true
		}
	}
	if !seen[prefix+"_day_00"] || !seen[prefix+"_day_56"] {
		t.Fatal("day start, discovered-time fallback, or day end was lost")
	}
	result = clients[1].request(t, "GET", "/api/me/hot?range=date&date=2000-01-02", nil, 200)
	if err := json.Unmarshal(result.Body.Bytes(), &dayResponse); err != nil || len(dayResponse.Items) != 0 {
		t.Fatal("historical hotspots crossed user boundary")
	}
	for _, path := range []string{"/api/me/hot?range=bad", "/api/me/hot?range=date&date=2000-02-30", "/api/me/hot?range=date&date=2999-01-01", "/api/me/hot?page=-1"} {
		clients[0].request(t, "GET", path, nil, 400)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO user_daily_reports(user_id,report_date,model,title,summary) VALUES($1,current_date,'fixture','user-one-only','private-daily')", users[0]); err != nil {
		t.Fatal(err)
	}
	if r := clients[1].request(t, "GET", "/api/me/daily", nil, 200); strings.Contains(r.Body.String(), "private-daily") {
		t.Fatal("daily crossed user boundary")
	}
	// Old article and analysis are retained, visible only in the read-only shared history.
	historyID := prefix + "_history"
	if _, err := pool.Exec(ctx, "INSERT INTO articles(id,source_id,identity_key,url,title,discovered_at,timeline_at,created_at) VALUES($1,$2,$1,'https://fixture.example.invalid/history','Historical title','2000-01-01','2000-01-01','2000-01-01')", historyID, source); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO analyses(article_id,input_revision,origin,title_zh,summary_zh,score) VALUES($1,1,'rule','Historical translated title','Historical summary',NULL)", historyID); err != nil {
		t.Fatal(err)
	}
	if r := clients[0].request(t, "GET", "/api/me/history", nil, 200); !strings.Contains(r.Body.String(), "Historical translated title") || !strings.Contains(r.Body.String(), "\"readOnly\":true") {
		t.Fatal("historical result missing or not read-only")
	}
	if r := clients[0].request(t, "GET", "/api/me/feed", nil, 200); strings.Contains(r.Body.String(), "Historical summary") {
		t.Fatal("history mixed into new personal results")
	}
}

func testCookie(token string) *http.Cookie { return &http.Cookie{Name: sessionCookie, Value: token} }
