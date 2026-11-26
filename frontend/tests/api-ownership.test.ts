import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import path from "node:path";
import { test } from "node:test";
import { isApiOwned, resolveRedirect } from "@aihot/contracts/http-policy";

const root = path.resolve(import.meta.dirname, "../..");
const compose = readFileSync(path.join(root, "deploy/compose.yaml"), "utf8");
const webProxy = readFileSync(path.join(root, "frontend/app/lib/api-proxy.server.ts"), "utf8");
const ssrApi = readFileSync(path.join(root, "frontend/app/lib/api.server.ts"), "utf8");
const adminApi = readFileSync(path.join(root, "frontend/app/lib/admin.server.ts"), "utf8");
const goServer = readFileSync(path.join(root, "backend-go/internal/httpapi/server.go"), "utf8");
const siteModules = readFileSync(path.join(root, "frontend/packages/site/modules/server.ts"), "utf8");

test("personal sources paths are not redirected into the admin area", () => {
  for (const pathname of ["/sources", "/sources/new", "/sources/example.xml"]) {
    assert.equal(resolveRedirect(pathname, ""), null, `${pathname} must reach the user route`);
  }
});

test("Web SSR and browser API traffic have one Go upstream", () => {
  assert.match(webProxy, /const API = new URL\(USER_API_BASE_URL\)/);
  assert.match(webProxy, /forward\(req, res, set/);
  assert.doesNotMatch(webProxy, /import \{ API_BASE_URL \}|proxyStoredSource|proxySourceUpdate|legacy|127\.0\.0\.1:3001/);
  assert.match(ssrApi, /process\.env\.GO_API_BASE_URL/);
  assert.doesNotMatch(ssrApi, /apiBase\(|API_BASE_URL=http|127\.0\.0\.1:3001/);
  assert.match(adminApi, /fetch\(`\$\{API_BASE_URL\}\$\{path\}`/);
  assert.doesNotMatch(adminApi, /GO_API_BASE_URL|apps\/api|legacy/);
});

test("Go owns user, admin, public, media and operational HTTP surfaces", () => {
  for (const route of [
    "POST /api/ingest/items", "GET /api/site/meta", "GET /api/site/changelog", "POST /api/site/feedback",
    "GET /api/site/hot", "GET /api/site/timeline", "GET /api/site/pool", "GET /api/site/topics",
    "GET /api/site/reports/{kind}", "GET /api/site/items/{id}", "GET /api/site/stories/{publicId}",
    "GET /api/v1/items", "GET /api/v1/hot-topics", "GET /api/admin/content", "GET /api/admin/sources",
    "POST /api/admin/content/{id}/visibility", "POST /api/admin/content/{id}/override",
    "POST /api/admin/content/{id}/rerun", "POST /api/admin/content/{id}/detach",
    "POST /api/admin/stories/merge",
    "GET /api/me", "PUT /api/me/model", "PUT /api/me/interests", "GET /api/me/recommended-articles",
    "GET /api/me/markets", "GET /api/me/markets/candles", "GET /api/ws/markets",
    "GET /api/me/sources", "GET /api/me/sources/recommendations", "POST /api/me/sources", "GET /api/me/sources/{id}",
    "PUT /api/me/sources/{id}", "DELETE /api/me/sources/{id}", "POST /api/me/sources/{id}/fetch",
  ]) assert.ok(goServer.includes(`mux.HandleFunc("${route}`) || goServer.includes(`mux.Handle("${route}`), `Go route missing: ${route}`);
  for (const route of ["POST /api/admin/sources", "PATCH /api/admin/sources/{id}", "POST /api/admin/sources/{id}/fetch"]) {
    assert.equal(goServer.includes(`mux.HandleFunc("${route}`), false, `retired admin source write route remains: ${route}`);
  }
  assert.match(goServer, /mux\.Handle\("\/api\/img-proxy"/);
  assert.match(goServer, /mux\.HandleFunc\("\/api\/"/);
  assert.match(goServer, /mux\.HandleFunc\("\/feed\.xml"/);
  assert.match(goServer, /mux\.Handle\("\/api\/mcp"/);
  assert.match(goServer, /mux\.HandleFunc\("GET \/sitemap\.xml"/);
});

test("Compose has no TypeScript API or Worker runtime and keeps safety valves closed by default", () => {
  assert.doesNotMatch(compose, /^  api:/m);
  assert.doesNotMatch(compose, /^  worker:/m);
  assert.match(compose, /go-api:/);
  assert.match(compose, /go-worker:/);
  assert.doesNotMatch(compose, /API_BASE_URL: http:\/\/api:3001/);
  assert.match(siteModules, /SERVER_MODULES: readonly ServerModule\[\] = \[\]/);
});

test("all share images render in Web", () => {
  for (const pathname of ["/og/site.png", "/og/pages/about.png", "/og/topics/openai.png", "/og/items/article_1.png", "/og/posters/article_1.png", "/og/reports/daily/2026-10-08.png", "/og/stories/story_fixture.png"]) {
    assert.equal(isApiOwned(pathname), false, `${pathname} should be rendered by Web`);
  }
  for (const pathname of ["/og/unknown/path", "/og/pages/one/two.png"]) {
    assert.equal(isApiOwned(pathname), true, `${pathname} remains an API-owned image path`);
  }
});
