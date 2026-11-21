package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"aihot/backend-go/internal/auth"
	"aihot/backend-go/internal/config"
	"aihot/backend-go/internal/interests"
	"aihot/backend-go/internal/modelcall"
	"aihot/backend-go/internal/provider"
	"aihot/backend-go/internal/receipts"
	"aihot/backend-go/internal/tagging"
	"aihot/backend-go/internal/topics"
)

func TestInterestsHTTPRoundTripIntegration(t *testing.T) {
	pool, orm := authTestPools(t)
	ctx := context.Background()
	prefix := fmt.Sprintf("interests_http_%d", time.Now().UnixNano())
	emails := []string{prefix + "_1@example.invalid", prefix + "_2@example.invalid"}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM user_accounts WHERE email=ANY($1)`, emails) })
	password := "fixture-password-123"
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	for _, email := range emails {
		if _, err := pool.Exec(ctx, `INSERT INTO user_accounts(email,password_hash,verified_at) VALUES($1,$2,now())`, email, hash); err != nil {
			t.Fatal(err)
		}
	}
	server := New(config.Config{PublicURL: "http://localhost"}, pool, orm, map[string]interests.Entity{"openai": {Name: "OpenAI"}, "google": {Name: "Google"}})
	clients := []authClient{{handler: server.Handler()}, {handler: server.Handler()}}
	anonymous := authClient{handler: server.Handler()}
	anonymous.request(t, "GET", "/api/me/interests", nil, 401)
	anonymous.request(t, "PUT", "/api/me/interests", map[string]any{"keywords": []string{"changed"}}, 401)
	for i := range clients {
		clients[i].login(t, emails[i], password)
	}
	cases := []struct {
		keywords, entities, wantKeywords, wantEntities []string
	}{
		{nil, nil, []string{}, []string{}},
		{[]string{"OpenAI"}, []string{"openai"}, []string{"OpenAI"}, []string{"openai"}},
		{[]string{" OpenAI ", "openai", "具身智能", `引号"`, `逗号,`, `反\斜杠`, "{花括号}"}, []string{"openai", "openai", "google"}, []string{"OpenAI", "具身智能", `引号"`, `逗号,`, `反\斜杠`, "{花括号}"}, []string{"openai", "google"}},
		{[]string{}, []string{}, []string{}, []string{}},
	}
	for i, test := range cases {
		response := clients[0].request(t, "PUT", "/api/me/interests", map[string]any{"keywords": test.keywords, "entityIds": test.entities}, 200)
		var saved struct {
			Keywords  []string `json:"keywords"`
			EntityIDs []string `json:"entityIds"`
			Version   int      `json:"version"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil || !reflect.DeepEqual(saved.Keywords, test.wantKeywords) || !reflect.DeepEqual(saved.EntityIDs, test.wantEntities) {
			t.Fatalf("PUT interests round trip: body=%s err=%v", response.Body.String(), err)
		}
		response = clients[0].request(t, "GET", "/api/me/interests", nil, 200)
		if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil || !reflect.DeepEqual(saved.Keywords, test.wantKeywords) || !reflect.DeepEqual(saved.EntityIDs, test.wantEntities) || saved.Version != i+1 {
			t.Fatalf("GET interests round trip: body=%s err=%v", response.Body.String(), err)
		}
		response = clients[1].request(t, "GET", "/api/me/interests", nil, 200)
		if strings.Contains(response.Body.String(), "OpenAI") || strings.Contains(response.Body.String(), "具身智能") {
			t.Fatal("interests crossed user boundary")
		}
	}
	savedCSRF := clients[0].csrf
	clients[0].csrf = ""
	clients[0].request(t, "PUT", "/api/me/interests", map[string]any{"keywords": []string{"changed"}}, 403)
	clients[0].csrf = savedCSRF
	clients[0].request(t, "PUT", "/api/me/interests", map[string]any{"entityIds": []string{"unknown"}}, 400)
	clients[0].request(t, "PUT", "/api/me/interests", map[string]any{"keywords": make([]string, 31)}, 400)
	if response := clients[0].request(t, "GET", "/api/me/interests", nil, 200); response.Body.String() != "{\"entityIds\":[],\"keywords\":[],\"labelIds\":[],\"version\":4}\n" {
		t.Fatalf("rejected updates changed stored interests: %s", response.Body.String())
	}
	t.Run("stable aliases respect subscription scope and survive legacy saves", func(t *testing.T) {
		id := prefix + "-agent"
		service := tagging.NewService(tagging.NewRepository(orm), tagging.Builtins(nil, []topics.Definition{
			{Slug: id, Name: "Agent 智能体", Group: "field", Aliases: []string{"智能体", "AI Agent"}, Definition: "自主规划与执行任务的系统"},
		}), nil)
		source, article, candidate := prefix+"-source", prefix+"-article", prefix+"-candidate"
		t.Cleanup(func() {
			_, _ = pool.Exec(ctx, "DELETE FROM articles WHERE id=$1", article)
			_, _ = pool.Exec(ctx, "DELETE FROM sources WHERE id=$1", source)
			_, _ = pool.Exec(ctx, "DELETE FROM interest_labels WHERE id=ANY($1)", []string{id, candidate})
		})
		for _, statement := range []struct {
			sql  string
			args []any
		}{
			{"INSERT INTO sources(id,name,kind) VALUES($1,'Fixture','external')", []any{source}},
			{"INSERT INTO articles(id,source_id,identity_key,url,title,discovered_at,timeline_at) VALUES($1,$2,$1,'https://example.com/agent','智能体发布',now(),now())", []any{article, source}},
			{"INSERT INTO article_discoveries(article_id,source_id,via) VALUES($1,$2,'fixture')", []any{article, source}},
			{"INSERT INTO user_source_subscriptions(user_id,source_id,name) SELECT id,$1,'Fixture' FROM user_accounts WHERE email=$2", []any{source, emails[0]}},
			{"INSERT INTO interest_labels(id,name,name_key,kind,definition,status) VALUES($1,'候选主题',$1,'topic','fixture','candidate')", []any{candidate}},
		} {
			if _, err := pool.Exec(ctx, statement.sql, statement.args...); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := service.Prepare(ctx); err != nil {
			t.Fatal(err)
		}
		anonymous.request(t, "GET", "/api/me/labels", nil, 401)
		for i := range clients {
			response := clients[i].request(t, "GET", "/api/me/labels", nil, 200)
			var result struct{ Labels []tagging.CatalogEntry }
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, label := range result.Labels {
				if label.ID == candidate {
					t.Fatal("candidate leaked into stable directory")
				}
				if label.ID == id {
					found = true
					if label.Count == nil || *label.Count != int64(1-i) {
						t.Fatalf("wrong scoped count: %s", response.Body.String())
					}
				}
			}
			if !found {
				t.Fatal("stable label missing")
			}
		}
		clients[0].request(t, "PUT", "/api/me/interests", map[string]any{"labelIds": []string{candidate}}, 400)
		clients[0].request(t, "PUT", "/api/me/interests", map[string]any{"labelIds": []string{id}}, 200)
		response := clients[0].request(t, "PUT", "/api/me/interests", map[string]any{"keywords": []string{"legacy"}}, 200)
		if !strings.Contains(response.Body.String(), id) {
			t.Fatal("legacy save erased label follows")
		}
		rules, err := service.LoadMatchRules(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if hits, configured := rules.Match([]string{id}, "智能体发布", ""); !configured || len(hits) != 1 {
			t.Fatalf("selected alias did not match: %v %v", hits, configured)
		}
	})
}

/** TestLabelDiscoveryAndPromotionIntegration verifies the lifecycle against local PostgreSQL and a fake provider. */
func TestLabelDiscoveryAndPromotionIntegration(t *testing.T) {
	pool, orm := authTestPools(t)
	ctx := context.Background()
	now := time.Now()
	const name = "智能体运行追踪"
	prefix := fmt.Sprintf("label_lifecycle_%d", now.UnixNano())
	sources := []string{prefix + "-a", prefix + "-b", prefix + "-c"}
	hosts := []string{"example.com", "example.net", "example.org"}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM audit_log WHERE subject IN (SELECT 'label:'||id FROM interest_labels WHERE name=$1)", name)
		_, _ = pool.Exec(ctx, "DELETE FROM interest_labels WHERE name=$1", name)
		_, _ = pool.Exec(ctx, "DELETE FROM articles WHERE source_id=ANY($1)", sources)
		_, _ = pool.Exec(ctx, "DELETE FROM sources WHERE id=ANY($1)", sources)
		_, _ = pool.Exec(ctx, "DELETE FROM label_model_attempts WHERE subject=ANY($1)", sources)
	})
	for _, source := range sources {
		if _, err := pool.Exec(ctx, "INSERT INTO sources(id,name,kind) VALUES($1,'Fixture','external')", source); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 7; i++ {
		index := i % len(sources)
		at := now.AddDate(0, 0, -2)
		if i%2 == 1 {
			at = now.AddDate(0, 0, -12)
		}
		id := fmt.Sprintf("%s-%d", prefix, i)
		title := fmt.Sprintf("%s在项目%d的实践", name, i)
		if i == 6 {
			title = name + "在项目0的实践"
		}
		if _, err := pool.Exec(ctx, "INSERT INTO articles(id,source_id,identity_key,url,title,discovered_at,timeline_at) VALUES($1,$2,$1,$3,$4,$5,$5)",
			id, sources[index], fmt.Sprintf("https://%s/article-%d", hosts[index], i), title, at); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, "INSERT INTO article_discoveries(article_id,source_id,via,discovered_at) VALUES($1,$2,'fixture',$3)", id, sources[index], at); err != nil {
			t.Fatal(err)
		}
	}
	values := map[string]string{"LLM_MODEL": "label-fixture", "LLM_API_KEY": "fixture", "LLM_BASE_URL": "https://model.example.invalid"}
	resolver := modelcall.NewCatalogResolver(nil, modelcall.Catalog{
		Capabilities: map[string]modelcall.Capability{"prefilter": {Key: "prefilter", Default: "default"}},
	}, nil, func(key string) string { return values[key] })
	models := modelcall.NewClient(true, resolver, receipts.NewService(receipts.NewRepository(orm)),
		func(_ context.Context, cfg provider.Config, _ string) (provider.Completion, error) {
			answer := fmt.Sprintf("{\"name\":%q,\"definition\":\"面向多步骤智能体的运行链路追踪与诊断技术\",\"terms\":[],\"articleIds\":[%q]}", name, prefix+"-0")
			if strings.Contains(cfg.System, "检查候选") {
				answer = "{\"decision\":\"distinct\"}"
			}
			body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": answer}}}})
			return provider.Completion{Response: body}, nil
		})
	service := tagging.NewService(tagging.NewRepository(orm), nil, models)
	if err := service.Run(ctx, now); err != nil {
		t.Fatal(err)
	}
	var id, status string
	if err := pool.QueryRow(ctx, "SELECT id,status FROM interest_labels WHERE name=$1", name).Scan(&id, &status); err != nil || status != "candidate" {
		t.Fatalf("expected discovery as candidate: %s, %v", status, err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM label_model_attempts WHERE subject=$1", id) })
	if err := service.Run(ctx, now); err != nil {
		t.Fatal(err)
	}
	rows, err := service.AdminCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	promoted := false
	for _, row := range rows {
		if row.ID == id {
			promoted = row.Status == "active" && row.Articles == 6 && row.Publishers == 3 && row.Weeks >= 2
		}
	}
	if !promoted {
		t.Fatalf("candidate did not promote with deduplicated evidence: %+v", rows)
	}
	off := tagging.NewService(tagging.NewRepository(orm), nil, nil)
	if err := off.Run(ctx, now.AddDate(0, 0, 91)); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT status FROM interest_labels WHERE id=$1", id).Scan(&status); err != nil || status != "dormant" {
		t.Fatalf("expected dormant after 90 days: %s, %v", status, err)
	}
	rules, err := off.LoadMatchRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hits, configured := rules.Match([]string{id}, name, ""); !configured || len(hits) != 1 {
		t.Fatal("dormant follow stopped matching")
	}
	target := prefix + "-canonical"
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM audit_log WHERE subject=$1", "label:"+id)
		_, _ = pool.Exec(ctx, "DELETE FROM interest_labels WHERE id=$1", id)
		_, _ = pool.Exec(ctx, "DELETE FROM interest_labels WHERE id=$1", target)
	})
	if _, err := pool.Exec(ctx, "INSERT INTO interest_labels(id,name,name_key,kind,definition,status) VALUES($1,'智能体链路诊断',$1,'topic','运行链路诊断','active')", target); err != nil {
		t.Fatal(err)
	}
	if err := off.Curate(ctx, "fixture-admin", id, "merge", target, "同义标签归并"); err != nil {
		t.Fatal(err)
	}
	rules, err = off.LoadMatchRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hits, configured := rules.Match([]string{id}, name, ""); !configured || !reflect.DeepEqual(hits, []string{"标签：智能体链路诊断"}) {
		t.Fatalf("merged follow did not resolve to the surviving label: %v", hits)
	}
}
