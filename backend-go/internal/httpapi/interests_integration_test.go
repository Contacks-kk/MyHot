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
	if response := clients[0].request(t, "GET", "/api/me/interests", nil, 200); response.Body.String() != "{\"entityIds\":[],\"keywords\":[],\"version\":4}\n" {
		t.Fatalf("rejected updates changed stored interests: %s", response.Body.String())
	}
}
