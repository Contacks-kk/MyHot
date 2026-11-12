package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/auth"
	"aihot/backend-go/internal/config"
)

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
	clients := []authClient{}
	for i := 0; i < 2; i++ {
		var id int64
		if err := pool.QueryRow(ctx, "INSERT INTO user_accounts(email,password_hash,verified_at) VALUES($1,'unused',now()) RETURNING id", fmt.Sprintf("%s_%d@example.invalid", prefix, i)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		users = append(users, id)
		token, hashed, err := auth.NewToken()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, "INSERT INTO user_sessions(user_id,token_hash,csrf_token,expires_at) VALUES($1,$2,'csrf',now()+interval '1 hour')", id, hashed); err != nil {
			t.Fatal(err)
		}
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
