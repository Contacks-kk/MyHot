// Failure cases: an origin-form // path is read as a URL authority and throws a 500; an unknown
// double-slash path becomes a different host's root; HEAD or single-fetch data follows another rule.
import assert from "node:assert/strict";
import { spawn, type ChildProcess } from "node:child_process";
import { once } from "node:events";
import { createServer, request } from "node:http";
import type { AddressInfo } from "node:net";
import { after, before, test } from "node:test";
import { fileURLToPath } from "node:url";

let web: ChildProcess;
let port: number;
let logs = "";
const upstreamPaths: string[] = [];
const userApiPaths: string[] = [];
const userApiMethods: string[] = [];
const userApiCookies: string[] = [];
const api = createServer((req, res) => {
  upstreamPaths.push(req.url ?? "");
  if ((req.url ?? "").split("?")[0] === "/api/img-proxy") {
    res.setHeader("Cache-Control", "public, max-age=60");
    if (req.method === "HEAD") return res.end();
    return res.end("legacy-image-proxy");
  }
  res.setHeader("Content-Type", "application/json");
  if (req.url === "/openapi-v1.json") return res.end("{}");
  if (req.url === "/api/me") { res.statusCode = 401; return res.end("{}"); }
  if (req.url === "/api/auth/registration-status") return res.end(JSON.stringify({ enabled: false }));
  if (req.url === "/api/site/meta") return res.end(JSON.stringify({ changelogVersion: null }));
  if ((req.url ?? "").startsWith("/api/admin/deliveries/")) return res.end(JSON.stringify({ owner: "legacy-deliveries" }));
  if (req.url?.startsWith("/api/site/timeline")) return res.end(JSON.stringify({
    cards: [], nextCursor: null, dayCounts: {}, hot: [], filters: { category: null, channel: null, tag: null },
  }));
  res.writeHead(404);
  res.end("{}");
});
const goApi = createServer((req, res) => {
  userApiPaths.push(req.url ?? "");
  userApiMethods.push(`${req.method} ${req.url ?? ""}`);
  userApiCookies.push(String(req.headers.cookie ?? ""));
  res.setHeader("Content-Type", "application/json");
  if ((req.url ?? "").split("?")[0] === "/api/img-proxy") {
    res.setHeader("Cache-Control", "public, max-age=60");
    if (req.method === "HEAD") return res.end();
    return res.end("go-image-proxy");
  }
  if (req.url === "/0123456789abcdef0123456789abcdef.txt") {
    res.setHeader("Content-Type", "text/plain; charset=UTF-8");
    res.setHeader("Cache-Control", "public, max-age=3600");
    res.setHeader("ETag", 'W/"indexnow-fixture"');
    if (req.headers["if-none-match"] === 'W/"indexnow-fixture"') { res.statusCode = 304; return res.end(); }
    if (req.method === "HEAD") return res.end();
    return res.end("0123456789abcdef0123456789abcdef");
  }
  if ((req.url ?? "").split("?")[0] === "/llms.txt") {
    if (req.headers.cookie !== "myhot_session=reader") {
      res.statusCode = 401;
      res.setHeader("Content-Type", "application/problem+json");
      res.setHeader("Cache-Control", "private, no-store");
      res.setHeader("Surrogate-Control", "no-store");
      return res.end(JSON.stringify({ status: 401 }));
    }
    res.setHeader("Content-Type", "text/plain; charset=utf-8");
    res.setHeader("Cache-Control", "private, no-store");
    res.setHeader("Surrogate-Control", "no-store");
    res.setHeader("ETag", 'W/"llms-fixture"');
    if (req.headers["if-none-match"] === 'W/"llms-fixture"' || req.headers["if-none-match"] === '"llms-fixture"') { res.statusCode = 304; return res.end(); }
    if (req.method === "HEAD") return res.end();
    return res.end("# Example\n");
  }
  if ((req.url ?? "").split("?")[0]!.startsWith("/feed/" ) || req.url === "/feed.xml") {
    res.setHeader("Content-Type", "application/rss+xml; charset=utf-8");
    if (req.method === "OPTIONS") { res.statusCode = 204; res.setHeader("Allow", "GET, HEAD, OPTIONS"); return res.end(); }
    return res.end("<rss />");
  }
  if ((req.url ?? "").split("?")[0] === "/sitemap.xml") {
    if (req.headers.cookie !== "myhot_session=reader") {
      res.statusCode = 401;
      res.setHeader("Content-Type", "application/problem+json");
      res.setHeader("Cache-Control", "private, no-store");
      res.setHeader("Surrogate-Control", "no-store");
      return res.end(JSON.stringify({ status: 401 }));
    }
    res.setHeader("Content-Type", "application/xml");
    res.setHeader("Cache-Control", "private, no-store");
    res.setHeader("Surrogate-Control", "no-store");
    res.setHeader("ETag", 'W/"sitemap-fixture"');
    if (req.headers["if-none-match"] === 'W/"sitemap-fixture"' || req.headers["if-none-match"] === '"sitemap-fixture"') { res.statusCode = 304; return res.end(); }
    if (req.method === "HEAD") return res.end();
    return res.end('<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9" />');
  }
  if ((req.url ?? "").split("?")[0] === "/api/site/hot") {
    if (req.headers.cookie !== "myhot_session=reader") {
      res.statusCode = 401;
      res.setHeader("Content-Type", "application/problem+json");
      res.setHeader("Cache-Control", "private, no-store");
      res.setHeader("Surrogate-Control", "no-store");
      return res.end(JSON.stringify({ status: 401 }));
    }
    res.setHeader("Cache-Control", "private, no-store");
    res.setHeader("Surrogate-Control", "no-store");
    res.setHeader("ETag", 'W/"hot-fixture"');
    if (req.headers["if-none-match"] === 'W/"hot-fixture"') { res.statusCode = 304; return res.end(); }
    if (req.method === "HEAD") return res.end();
    return res.end(JSON.stringify({ computedAt: null, windowHours: 48, entries: [] }));
  }
  if ((req.url ?? "").split("?")[0] === "/api/site/timeline") {
    if (req.headers.cookie !== "myhot_session=reader") {
      res.statusCode = 401;
      res.setHeader("Content-Type", "application/problem+json");
      res.setHeader("Cache-Control", "private, no-store");
      res.setHeader("Surrogate-Control", "no-store");
      return res.end(JSON.stringify({ status: 401 }));
    }
    res.setHeader("Cache-Control", "private, no-store");
    res.setHeader("Surrogate-Control", "no-store");
    res.setHeader("ETag", 'W/"tl-fixture"');
    if (req.headers["if-none-match"] === 'W/"tl-fixture"') { res.statusCode = 304; return res.end(); }
    if (req.method === "HEAD") return res.end();
    return res.end(JSON.stringify({ filters: { channel: "all", category: null, tag: null }, cards: [], nextCursor: null, hot: null, dayCounts: {} }));
  }
  if (/^\/items\/[a-zA-Z0-9_-]{1,80}\/markdown$/.test((req.url ?? "").split("?")[0]!)) {
    if (req.headers.cookie !== "myhot_session=reader") {
      res.statusCode = 401;
      res.setHeader("Content-Type", "application/problem+json");
      res.setHeader("Cache-Control", "private, no-store");
      res.setHeader("Surrogate-Control", "no-store");
      return res.end(JSON.stringify({ status: 401 }));
    }
    res.setHeader("Content-Type", "text/markdown; charset=utf-8");
    res.setHeader("Content-Disposition", 'attachment; filename="myhot-article_1.md"');
    res.setHeader("Cache-Control", "public, max-age=300, s-maxage=300");
    res.setHeader("X-Robots-Tag", "noindex");
    if (req.method === "HEAD") return res.end();
    return res.end("# Article\n");
  }
  if (req.url === "/api/site/items/og_fixture") return res.end(JSON.stringify({ id: "og_fixture", title: "OG fixture", summary: "An article summary", source: { name: "Fixture source" }, timelineAt: "2026-10-08T00:00:00.000Z", category: "ai-models", score: 80, selected: true }));
  if (/^\/api\/site\/items\/(?!availability(?:\/|$))[a-zA-Z0-9_-]{1,80}(?:\/original)?$/.test((req.url ?? "").split("?")[0]!)) {
    if (req.headers.cookie !== "myhot_session=reader") {
      res.statusCode = 401;
      res.setHeader("Content-Type", "application/problem+json");
      res.setHeader("Cache-Control", "private, no-store");
      res.setHeader("Surrogate-Control", "no-store");
      return res.end(JSON.stringify({ status: 401 }));
    }
    res.setHeader("Cache-Control", "private, no-store");
    res.setHeader("Surrogate-Control", "no-store");
    res.setHeader("ETag", 'W/"item-fixture"');
    if (req.headers["if-none-match"] === 'W/"item-fixture"') { res.statusCode = 304; return res.end(); }
    if (req.method === "HEAD") return res.end();
    return res.end(JSON.stringify({ id: "article_1", readingMode: "full", body: null }));
  }
  if (req.url === "/api/site/stories/story_fixture") return res.end(JSON.stringify({ publicId: "story_fixture", title: "Story fixture", sourceCount: 3, reportCount: 4, latest: "Latest update", digest: "Story digest", whyHot: { rank: 2 }, timeline: [], heat: [] }));
  if (/^\/api\/site\/stories\/[^/]+$/.test((req.url ?? "").split("?")[0]!)) {
    if (req.headers.cookie !== "myhot_session=reader") {
      res.statusCode = 401;
      res.setHeader("Content-Type", "application/problem+json");
      res.setHeader("Cache-Control", "private, no-store");
      res.setHeader("Surrogate-Control", "no-store");
      return res.end(JSON.stringify({ status: 401 }));
    }
    res.setHeader("Cache-Control", "private, no-store");
    res.setHeader("Surrogate-Control", "no-store");
    res.setHeader("ETag", 'W/"story-fixture"');
    if (req.headers["if-none-match"] === 'W/"story-fixture"') { res.statusCode = 304; return res.end(); }
    if (req.method === "HEAD") return res.end();
    return res.end(JSON.stringify({ publicId: "12345678-1234-4234-8234-123456789abc", title: "Story", timeline: [], heat: [] }));
  }
  if ((req.url ?? "").split("?")[0] === "/api/site/pool") {
    if (req.headers.cookie !== "myhot_session=reader") {
      res.statusCode = 401;
      res.setHeader("Content-Type", "application/problem+json");
      res.setHeader("Cache-Control", "private, no-store");
      res.setHeader("Surrogate-Control", "no-store");
      return res.end(JSON.stringify({ status: 401 }));
    }
    res.setHeader("Cache-Control", "private, no-store");
    res.setHeader("Surrogate-Control", "no-store");
    res.setHeader("ETag", 'W/"pool-fixture"');
    if (req.headers["if-none-match"] === 'W/"pool-fixture"') { res.statusCode = 304; return res.end(); }
    if (req.method === "HEAD") return res.end();
    return res.end(JSON.stringify({ filters: { channel: "all", category: null, tag: null, q: null, tab: "time" }, items: [], page: 1, pageCount: 1, total: 0, todayCount: 0, freshness: "2026-10-08T00:00:00.000Z" }));
  }
  if ((req.url ?? "").split("?")[0] === "/api/mcp") return res.end(JSON.stringify({ owner: "go-mcp" }));
  if (["/api/site/reports/daily", "/api/site/reports/weekly", "/api/site/reports/monthly"].includes((req.url ?? "").split("?")[0]!)) return res.end(JSON.stringify({ kind: (req.url ?? "").split("?")[0]!.split("/").at(-1), items: [] }));
  if (/^\/api\/site\/reports\/(daily|weekly|monthly)\/latest-page$/.test((req.url ?? "").split("?")[0]!)) return res.end(JSON.stringify({ index: [], report: null }));
  if (req.url === "/api/site/reports/daily/2026-10-08") return res.end(JSON.stringify({ kind: "daily", key: "2026-10-08", title: "Daily fixture", lead: { title: "Lead", leadParagraph: "A report lead" }, overview: "Report overview", sections: [{ items: [{}, {}] }], readingMinutes: 5 }));
  if (/^\/api\/site\/reports\/(daily|weekly|monthly)\/\d{4}-(?:\d{2}(?:-\d{2})?|W\d{2})$/.test((req.url ?? "").split("?")[0]!)) return res.end(JSON.stringify({ kind: "daily", key: "2026-10-07" }));
  if (/^\/api\/v1\/stories\/[^/]+$/.test((req.url ?? "").split("?")[0]!)) return res.end(JSON.stringify({ owner: "go-admin-auth" }));
  if ((req.url ?? "").split("?")[0] === "/api/v1/agent") return res.end(JSON.stringify({ owner: "go-agent-guide" }));
  if (/^\/api\/v1\/agent\/(?:daily(?:\/[^/]+)?|weekly(?:\/[^/]+)?|monthly(?:\/[^/]+)?)$/.test((req.url ?? "").split("?")[0]!)) return res.end(JSON.stringify({ owner: "go-agent-reports" }));
  if (/^\/api\/admin\/deliveries\/19\/resolve$/.test((req.url ?? "").split("?")[0]!)) return res.end(JSON.stringify({ owner: "go-admin-auth" }));
  if (/^\/api\/admin\/deliveries\/not-an-id\/resolve$/.test((req.url ?? "").split("?")[0]!)) return res.end(JSON.stringify({ owner: "go-admin-auth" }));
  if (req.url === "/api/admin/sources/rss-preview-demo") return res.end(JSON.stringify({ source: { kind: "rss" } }));
    if (req.url === "/api/admin/sources/x-preview-demo") return res.end(JSON.stringify({ source: { kind: "x_search" } }));
    if (["/api/auth/login", "/api/auth/options", "/api/auth/password", "/api/auth/feishu", "/api/auth/callback", "/api/auth/check", "/api/admin/auth/logout", "/api/admin/me", "/api/admin/feedback", "/api/admin/feedback/12", "/api/admin/feedback/12/erase", "/api/admin/feedback/12/screenshot", "/api/admin/feedback-bans", "/api/admin/feedback-bans/abc123", "/api/admin/nav-counts", "/api/admin/audit", "/api/admin/runs", "/api/admin/processing/requeue", "/api/admin/models", "/api/admin/models/prefilter", "/api/admin/settings", "/api/admin/settings/contact-qr", "/api/admin/notify-targets/weekly", "/api/admin/budgets/openai", "/api/admin/selectbench", "/api/admin/selectbench/sb-20261007-abc123", "/api/admin/selectbench/import", "/api/admin/sources", "/api/admin/sources/preview", "/api/admin/sources/rss-demo", "/api/admin/sources/rss-preview-demo/preview", "/api/admin/sources/x-preview-demo", "/api/admin/sources/x-preview-demo/preview", "/api/admin/content", "/api/admin/content/article-1", "/api/admin/content/article-1/seo", "/api/admin/deliveries/17/resolve", "/api/admin/deliveries/18/resolve", "/api/admin/receipts/17/release", "/api/site/meta", "/api/site/changelog", "/api/site/feedback", "/api/site/items/availability", "/api/site/topics", "/api/site/topics/openai", "/api/site/stories/12345678-1234-4234-8234-123456789abc/followups", "/api/site/groups/fact-1/reports", "/api/site/reports/daily/navigation/2026-10-07", "/api/site/reports/daily/months/2026-10", "/api/v1/items", "/api/v1/hot-topics", "/api/v1/agent/hot", "/api/v1/agent/latest", "/api/v1/agent/search", "/api/v1/agent/stories/12345678-1234-4234-8234-123456789abc", "/api/v1/selected/snapshot", "/api/v1/selected/changes", "/api/v1/dailies", "/api/v1/dailies/latest", "/api/v1/dailies/2026-10-07", "/api/v1/weeklies", "/api/v1/weeklies/latest", "/api/v1/weeklies/2026-W41", "/api/v1/monthlies", "/api/v1/monthlies/latest", "/api/v1/monthlies/2026-10", "/contact/qr-wechat-1234abcd.png"].includes((req.url ?? "").split("?")[0]!)) return res.end(JSON.stringify({ owner: "go-admin-auth" }));
  if (req.url === "/api/me") { if (req.headers.cookie === "myhot_session=reader") return res.end(JSON.stringify({ id: 1, email: "reader@example.invalid", csrfToken: "fixture" })); res.statusCode = 401; return res.end("{}"); }
  if (req.url === "/api/auth/registration-status") return res.end(JSON.stringify({ enabled: false }));
  if (req.url === "/api/ingest/items") return res.end(JSON.stringify({ ok: true, created: 1 }));
  if (req.url === "/api/health") return res.end(JSON.stringify({ ok: true, db: "ok", source: "go" }));
  if ((req.url ?? "").split("?")[0] === "/openapi-v1.json") return res.end("{}");
  res.writeHead(404);
  res.end("{}");
});

before(async () => {
  api.listen(0, "127.0.0.1");
  await once(api, "listening");
  goApi.listen(0, "127.0.0.1");
  await once(goApi, "listening");
  web = spawn(process.execPath, [fileURLToPath(new URL("../server.ts", import.meta.url))], {
    env: { ...process.env, WEB_PORT: "0", TRUST_PROXY: "false", GO_MEDIA_PREPARE_ENABLED: "false", INDEXNOW_KEY: "0123456789abcdef0123456789abcdef", GO_API_BASE_URL: `http://127.0.0.1:${(goApi.address() as AddressInfo).port}` },
    stdio: ["ignore", "pipe", "pipe"],
  });
  await new Promise<void>((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`web did not start: ${logs}`)), 15000);
    web.on("exit", () => { clearTimeout(timer); reject(new Error(`web exited: ${logs}`)); });
    web.stderr!.on("data", (chunk) => { logs += String(chunk); });
    web.stdout!.on("data", (chunk) => {
      const match = String(chunk).match(/"msg":"web started","port":(\d+)/);
      if (match) { port = Number(match[1]); clearTimeout(timer); resolve(); }
    });
  });
});

after(async () => {
  if (web && web.exitCode === null) { web.kill("SIGTERM"); await once(web, "exit"); }
  api.closeAllConnections();
  await new Promise<void>((resolve) => api.close(() => resolve()));
  goApi.closeAllConnections();
  await new Promise<void>((resolve) => goApi.close(() => resolve()));
});

function rawAt(targetPort: number, path: string, method: "GET" | "HEAD" | "POST" | "PATCH" | "PUT" | "DELETE" | "OPTIONS", headers: import("node:http").OutgoingHttpHeaders = {}, body?: string) {
  return new Promise<{ status: number; headers: import("node:http").IncomingHttpHeaders; body: string }>((resolve, reject) => {
    const req = request({ hostname: "127.0.0.1", port: targetPort, path, method, headers }, (res) => {
      let body = "";
      res.setEncoding("utf8");
      res.on("data", (chunk) => { body += chunk; });
      res.on("end", () => resolve({ status: res.statusCode!, headers: res.headers, body }));
      res.on("error", reject);
    });
    req.on("error", reject);
    req.end(body);
  });
}

function raw(path: string, method: "GET" | "HEAD" | "POST" | "PATCH" | "PUT" | "DELETE" | "OPTIONS", headers: import("node:http").OutgoingHttpHeaders = {}, body?: string) {
  return rawAt(port, path, method, headers, body);
}

test("origin-form slashes keep the router's homepage and unknown-route semantics", async () => {
  for (const method of ["GET", "HEAD"] as const) {
    const home = await raw("/", method);
    assert.equal(home.status, 200, logs);
    for (const path of ["//", "////", "//?category=ai-models"]) {
      const res = await raw(path, method);
      assert.equal(res.status, 200, `${method} ${path}: ${logs}`);
      assert.equal(res.headers["content-type"], home.headers["content-type"]);
      assert.equal(res.headers["cache-control"], "private, no-store");
      if (method === "HEAD") assert.equal(res.body, "");
    }
    for (const path of ["/unknown-page", "//unknown-page", "//evil.invalid/unknown-page", "//unknown-page.data?token=private"]) {
      const res = await raw(path, method);
      assert.equal(res.status, 302, `${method} ${path}: ${logs}`);
      assert.equal(res.headers["cache-control"], "private, no-store");
      if (method === "HEAD") assert.equal(res.body, "");
    }
  }
  assert.equal(logs, "");
  assert.ok(upstreamPaths.every((path) => path.startsWith("/api/")));
});

test("RSS feed paths are proxied to the Go API", async () => {
  for (const path of ["/feed.xml", "/feed/full.xml", "/feed/all.xml", "/feed/daily.xml", "/feed/category/ai-models.xml", "/feed/full/category/ai-models.xml"]) {
    const response = await raw(path, "GET");
    assert.equal(response.status, 200, `${path}: ${logs}`);
    assert.equal(response.headers["content-type"], "application/rss+xml; charset=utf-8");
    assert.ok(userApiPaths.includes(path), `${path} did not reach Go`);
    assert.ok(!upstreamPaths.includes(path), `${path} reached the legacy API`);
  }
  const options = await raw("/feed.xml", "OPTIONS");
  assert.equal(options.status, 204);
  assert.equal(options.headers.allow, "GET, HEAD, OPTIONS");
  assert.ok(userApiMethods.includes("OPTIONS /feed.xml"));
});

test("image proxy uses Go while animation preparation remains feature gated", async () => {
  const beforeLegacy = upstreamPaths.length;
  const beforeGo = userApiMethods.length;
  const path = "/api/img-proxy?u=https%3A%2F%2Fexample.invalid%2Fimage.png&mode=thumb&exp=2000000000&sig=fixture";
  const image = await raw(path, "GET");
  assert.equal(image.status, 200, logs);
  assert.equal(image.body, "go-image-proxy");
  const head = await raw(path, "HEAD");
  assert.equal(head.status, 200, logs);
  assert.equal(head.body, "");
  assert.deepEqual(upstreamPaths.slice(beforeLegacy), []);
  assert.deepEqual(userApiMethods.slice(beforeGo), [`GET ${path}`, `HEAD ${path}`]);
});

test("image proxy remains on Go when animation preparation is enabled", async () => {
  const logsForGoOwner: string[] = [];
  const child = spawn(process.execPath, [fileURLToPath(new URL("../server.ts", import.meta.url))], {
    env: { ...process.env, WEB_PORT: "0", TRUST_PROXY: "false", GO_MEDIA_PREPARE_ENABLED: "true", INDEXNOW_KEY: "", GO_API_BASE_URL: `http://127.0.0.1:${(goApi.address() as AddressInfo).port}` },
    stdio: ["ignore", "pipe", "pipe"],
  });
  let goOwnedPort = 0;
  child.stderr!.on("data", (chunk) => logsForGoOwner.push(String(chunk)));
  try {
    await new Promise<void>((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error(`Go-owned web did not start: ${logsForGoOwner.join("")}`)), 15000);
      child.on("exit", () => { clearTimeout(timer); reject(new Error(`Go-owned web exited: ${logsForGoOwner.join("")}`)); });
      child.stdout!.on("data", (chunk) => {
        const match = String(chunk).match(/"msg":"web started","port":(\d+)/);
        if (match) { goOwnedPort = Number(match[1]); clearTimeout(timer); resolve(); }
      });
    });
    const beforeGo = userApiMethods.length;
    const beforeLegacy = upstreamPaths.length;
    const path = "/api/img-proxy?u=https%3A%2F%2Fexample.invalid%2Fimage.png&mode=thumb&exp=2000000000&sig=fixture";
    const image = await rawAt(goOwnedPort, path, "GET");
    assert.equal(image.status, 200, logsForGoOwner.join(""));
    assert.equal(image.body, "go-image-proxy");
    const head = await rawAt(goOwnedPort, path, "HEAD");
    assert.equal(head.status, 200, logsForGoOwner.join(""));
    assert.equal(head.body, "");
    assert.deepEqual(userApiMethods.slice(beforeGo), [`GET ${path}`, `HEAD ${path}`]);
    assert.deepEqual(upstreamPaths.slice(beforeLegacy), []);
  } finally {
    if (child.exitCode === null) { child.kill("SIGTERM"); await once(child, "exit"); }
  }
});

test("share cards and posters render in Web behind the Go session gate", async () => {
  const legacyBefore = upstreamPaths.length;
  const denied = await raw("/og/site.png", "GET");
  assert.equal(denied.status, 401, logs);
  assert.equal(denied.headers["content-type"], "application/problem+json");
  assert.equal(denied.headers["cache-control"], "private, no-store");
  assert.equal(JSON.parse(denied.body).code, "unauthorized");

  const site = await raw("/og/site.png", "GET", { cookie: "myhot_session=reader" });
  assert.equal(site.status, 200, logs);
  assert.equal(site.headers["content-type"], "image/png");
  assert.equal(site.headers["cache-control"], "private, no-store");
  assert.equal(site.headers["surrogate-control"], "no-store");
  assert.match(String(site.headers.etag), /^"og-[0-9a-f]{24}"$/);
  assert.ok(Number(site.headers["content-length"]) > 0);

  const head = await raw("/og/pages/about.png", "HEAD", { cookie: "myhot_session=reader" });
  assert.equal(head.status, 200, logs);
  assert.equal(head.headers["content-type"], "image/png");
  assert.equal(head.body, "");
  assert.ok(head.headers.etag);

  const cached = await raw("/og/site.png", "GET", {
    cookie: "myhot_session=reader",
    "if-none-match": `W/${site.headers.etag}`,
  });
  assert.equal(cached.status, 304, logs);
  assert.equal(cached.body, "");
  assert.equal(cached.headers["cache-control"], "private, no-store");

  const topic = await raw("/og/topics/openai.png", "GET", { cookie: "myhot_session=reader" });
  assert.equal(topic.status, 200, logs);
  assert.equal(topic.headers["content-type"], "image/png");

  const itemCard = await raw("/og/items/og_fixture.png", "GET", { cookie: "myhot_session=reader" });
  assert.equal(itemCard.status, 200, logs);
  assert.equal(itemCard.headers["content-type"], "image/png");
  assert.ok(userApiPaths.includes("/api/site/items/og_fixture"));

  const reportCard = await raw("/og/reports/daily/2026-10-08.png", "GET", { cookie: "myhot_session=reader" });
  assert.equal(reportCard.status, 200, logs);
  assert.equal(reportCard.headers["content-type"], "image/png");
  assert.ok(userApiPaths.includes("/api/site/reports/daily/2026-10-08"));

  const storyCard = await raw("/og/stories/story_fixture.png", "GET", { cookie: "myhot_session=reader" });
  assert.equal(storyCard.status, 200, logs);
  assert.equal(storyCard.headers["content-type"], "image/png");
  assert.ok(userApiPaths.includes("/api/site/stories/story_fixture"));

  const poster = await raw("/og/posters/og_fixture.png", "GET", { cookie: "myhot_session=reader" });
  assert.equal(poster.status, 200, logs);
  assert.equal(poster.headers["content-type"], "image/png");
  assert.equal(poster.headers["cache-control"], "private, no-store");
  assert.match(String(poster.headers.etag), /^"poster-[0-9a-f]{24}"$/);
  assert.ok(userApiPaths.includes("/api/site/items/og_fixture"));
  const posterHead = await raw("/og/posters/og_fixture.png", "HEAD", { cookie: "myhot_session=reader" });
  assert.equal(posterHead.status, 200, logs);
  assert.equal(posterHead.headers.etag, poster.headers.etag);
  assert.equal(posterHead.body, "");
  const cachedPoster = await raw("/og/posters/og_fixture.png", "GET", {
    cookie: "myhot_session=reader",
    "if-none-match": `W/${poster.headers.etag}`,
  });
  assert.equal(cachedPoster.status, 304, logs);
  assert.equal(cachedPoster.body, "");

  const missing = await raw("/og/pages/not-a-card.png", "GET", { cookie: "myhot_session=reader" });
  assert.equal(missing.status, 404, logs);
  assert.equal(missing.headers["cache-control"], "private, no-store");
  assert.equal(upstreamPaths.length, legacyBefore, "Web-owned share cards must not reach the API");
  assert.ok(userApiPaths.filter((path) => path === "/api/me").length >= 8, "the Go session gate should run for each card request");
});

test("external item ingest is proxied to Go without a user session", async () => {
	const beforeGo = userApiMethods.length;
	const beforeLegacy = upstreamPaths.length;
	const body = JSON.stringify({ sourceId: "feedbot", items: [{ title: "New item", url: "https://source.example/item" }] });
	const response = await raw("/api/ingest/items", "POST", {
		"content-type": "application/json", authorization: "Bearer fixture-ingest-token-123",
	}, body);
	assert.equal(response.status, 200, logs);
	assert.deepEqual(JSON.parse(response.body), { ok: true, created: 1 });
	assert.deepEqual(userApiMethods.slice(beforeGo), ["POST /api/ingest/items"]);
	assert.deepEqual(upstreamPaths.slice(beforeLegacy), []);
});

test("health, site metadata and content reads use Go", async () => {
  const response = await raw("/api/health", "GET");
  assert.equal(response.status, 200, logs);
  assert.deepEqual(JSON.parse(response.body), { ok: true, db: "ok", source: "go" });
  assert.ok(userApiPaths.includes("/api/health"));
  assert.ok(!upstreamPaths.includes("/api/health"));
  for (const path of ["/api/site/meta", "/api/site/changelog"]) {
    const siteInfo = await raw(path, "GET");
    assert.equal(siteInfo.status, 200, `${path}: ${logs}`);
    assert.ok(userApiPaths.includes(path));
    assert.ok(!upstreamPaths.includes(path));
  }
  const timeline = await raw("/api/site/timeline?channel=news&tag=Agent", "GET", { cookie: "myhot_session=reader" });
  assert.equal(timeline.status, 200, logs);
  assert.ok(userApiPaths.includes("/api/site/timeline?channel=news&tag=Agent"));
  assert.ok(!upstreamPaths.includes("/api/site/timeline?channel=news&tag=Agent"));
  const followupsPath = "/api/site/stories/12345678-1234-4234-8234-123456789abc/followups";
  const followups = await raw(followupsPath, "GET");
  assert.equal(followups.status, 200, logs);
  assert.ok(userApiPaths.includes(followupsPath));
  assert.ok(!upstreamPaths.includes(followupsPath));
  const groupPath = "/api/site/groups/fact-1/reports?channel=news&tag=Agent";
  const groupReports = await raw(groupPath, "GET");
  assert.equal(groupReports.status, 200, logs);
  assert.ok(userApiPaths.includes(groupPath));
  assert.ok(!upstreamPaths.includes(groupPath));
  for (const path of ["/api/site/reports/daily", "/api/site/reports/weekly?from=archive", "/api/site/reports/monthly"]) {
    const reportIndex = await raw(path, "GET", { cookie: "myhot_session=reader" });
    assert.equal(reportIndex.status, 200, `${path}: ${logs}`);
    assert.ok(userApiPaths.includes(path), `${path} did not reach Go`);
    assert.ok(!upstreamPaths.includes(path), `${path} reached the legacy API`);
  }
  const reportHead = await raw("/api/site/reports/daily", "HEAD", { cookie: "myhot_session=reader" });
  assert.equal(reportHead.status, 200, logs);
  assert.ok(userApiMethods.includes("HEAD /api/site/reports/daily"));
  assert.equal(reportHead.body, "");
  for (const path of ["/api/site/reports/daily/navigation/2026-10-07", "/api/site/reports/daily/months/2026-10"]) {
    const reportNavigation = await raw(path, "GET", { cookie: "myhot_session=reader" });
    assert.equal(reportNavigation.status, 200, `${path}: ${logs}`);
    assert.ok(userApiPaths.includes(path), `${path} did not reach Go`);
    assert.ok(!upstreamPaths.includes(path), `${path} reached the legacy API`);
  }
  for (const path of ["/api/site/reports/daily/latest-page", "/api/site/reports/daily/2026-10-07"]) {
    const response = await raw(path, "GET", { cookie: "myhot_session=reader" });
    assert.equal(response.status, 200, `${path}: ${logs}`);
    assert.ok(userApiPaths.includes(path), `${path} did not reach Go`);
    assert.ok(!upstreamPaths.includes(path), `${path} reached the legacy API`);
  }
  const reportDetailHead = await raw("/api/site/reports/daily/2026-10-07", "HEAD", { cookie: "myhot_session=reader" });
  assert.equal(reportDetailHead.status, 200, logs);
  assert.ok(userApiMethods.includes("HEAD /api/site/reports/daily/2026-10-07"));
  assert.equal(reportDetailHead.body, "");
  const dailyArchive = await raw("/daily/archive", "GET", { cookie: "myhot_session=reader" });
  assert.equal(dailyArchive.status, 200, logs);
  assert.ok(userApiPaths.includes("/api/site/reports/daily"), "SSR archive index request uses Go");
  assert.ok(!upstreamPaths.includes("/api/site/reports/daily"), "SSR archive index request skips legacy API");
});

test("admin authentication and all admin API paths use Go", async () => {
  const cases = [
    ["GET", "/api/auth/login"], ["GET", "/api/auth/options"], ["POST", "/api/auth/password"],
    ["GET", "/api/auth/feishu"], ["GET", "/api/auth/callback"], ["GET", "/api/auth/check"],
    ["POST", "/api/admin/auth/logout"], ["GET", "/api/admin/me"],
    ["GET", "/api/admin/feedback"], ["PATCH", "/api/admin/feedback/12"],
    ["POST", "/api/admin/feedback/12/erase"], ["GET", "/api/admin/feedback/12/screenshot"],
    ["POST", "/api/site/feedback"],
    ["POST", "/api/admin/feedback-bans"], ["DELETE", "/api/admin/feedback-bans/abc123"],
    ["GET", "/api/admin/nav-counts"], ["GET", "/api/admin/audit"], ["GET", "/api/admin/runs"], ["POST", "/api/admin/processing/requeue"], ["POST", "/api/admin/receipts/17/release"],
    ["GET", "/api/admin/settings"], ["POST", "/api/admin/settings/contact-qr"], ["POST", "/api/admin/notify-targets/weekly"], ["PUT", "/api/admin/budgets/openai"],
    ["GET", "/api/admin/selectbench"], ["GET", "/api/admin/selectbench/sb-20261007-abc123"], ["POST", "/api/admin/selectbench/import"],
    ["GET", "/api/admin/sources"], ["POST", "/api/admin/sources"], ["GET", "/api/admin/sources/rss-demo"],
    ["GET", "/api/admin/content"],
  ] as const;
  for (const [method, path] of cases) {
    const response = await raw(path, method);
    assert.equal(response.status, 200, `${method} ${path}: ${logs}`);
    assert.deepEqual(JSON.parse(response.body), { owner: "go-admin-auth" });
    assert.ok(userApiPaths.includes(path), `${path} did not reach Go`);
    assert.ok(!upstreamPaths.includes(path), `${path} reached the legacy API`);
  }
  const adminData = await raw("/api/admin/models", "GET");
  assert.equal(adminData.status, 200);
  assert.ok(userApiPaths.includes("/api/admin/models"));
  const modelSwitch = await raw("/api/admin/models/prefilter", "POST");
  assert.equal(modelSwitch.status, 200);
  assert.ok(userApiPaths.includes("/api/admin/models/prefilter"));
  const sourcePreview = await raw("/api/admin/sources/preview", "POST");
  assert.equal(sourcePreview.status, 200);
  assert.ok(userApiPaths.includes("/api/admin/sources/preview"));
  const rssPreview = await raw("/api/admin/sources/preview", "POST", { "content-type": "application/json" }, JSON.stringify({ id: "draft", kind: "rss", config: {} }));
  assert.equal(rssPreview.status, 200, logs);
  assert.ok(userApiPaths.includes("/api/admin/sources/preview"));
  assert.equal(upstreamPaths.filter((path) => path === "/api/admin/sources/preview").length, 0);
  const xPreview = await raw("/api/admin/sources/preview", "POST", { "content-type": "application/json" }, JSON.stringify({ id: "draft", kind: "x_search", config: {} }));
  assert.equal(xPreview.status, 200, logs);
  assert.ok(userApiPaths.includes("/api/admin/sources/preview"));
  assert.equal(upstreamPaths.filter((path) => path === "/api/admin/sources/preview").length, 0);
  const storedPreviewGo = await raw("/api/admin/sources/rss-preview-demo/preview", "POST");
  assert.equal(storedPreviewGo.status, 200, logs);
  assert.ok(userApiPaths.includes("/api/admin/sources/rss-preview-demo/preview"), "stored preview uses Go");
  assert.ok(!upstreamPaths.includes("/api/admin/sources/rss-preview-demo/preview"));
  const storedXPreview = await raw("/api/admin/sources/x-preview-demo/preview", "POST");
  assert.equal(storedXPreview.status, 200, logs);
  assert.ok(userApiPaths.includes("/api/admin/sources/x-preview-demo/preview"), "stored X previews use Go");
  assert.ok(!upstreamPaths.includes("/api/admin/sources/x-preview-demo/preview"));
  const storedFetch = await raw("/api/admin/sources/rss-fetch-demo/fetch", "POST");
  assert.equal(storedFetch.status, 404);
  assert.ok(userApiPaths.includes("/api/admin/sources/rss-fetch-demo/fetch"), "manual fetch uses Go");
  const contentDetail = await raw("/api/admin/content/article-1", "GET");
  assert.equal(contentDetail.status, 200);
  assert.ok(userApiPaths.includes("/api/admin/content/article-1"));
  const contentSEO = await raw("/api/admin/content/article-1/seo", "POST", { "content-type": "application/json" }, JSON.stringify({ indexed: false, reason: "reviewed" }));
  assert.equal(contentSEO.status, 200, logs);
  assert.deepEqual(JSON.parse(contentSEO.body), { owner: "go-admin-auth" });
  assert.ok(userApiPaths.includes("/api/admin/content/article-1/seo"));
  assert.ok(userApiMethods.includes("POST /api/admin/content/article-1/seo"));
  assert.ok(!upstreamPaths.includes("/api/admin/content/article-1/seo"));
  const deliveryDrop = await raw("/api/admin/deliveries/17/resolve", "POST", { "content-type": "application/json", cookie: "myhot_admin_session=operator" }, JSON.stringify({ outcome: "drop", note: "confirmed" }));
  assert.equal(deliveryDrop.status, 200, logs);
  assert.ok(userApiMethods.includes("POST /api/admin/deliveries/17/resolve"));
  assert.ok(!upstreamPaths.includes("/api/admin/deliveries/17/resolve"));
  const deliveryResend = await raw("/api/admin/deliveries/18/resolve", "POST", { "content-type": "application/json", cookie: "myhot_admin_session=operator" }, JSON.stringify({ outcome: "resend", note: "confirmed" }));
  assert.deepEqual(JSON.parse(deliveryResend.body), { owner: "go-admin-auth" });
  assert.ok(userApiPaths.includes("/api/admin/deliveries/18/resolve"));
  assert.ok(!upstreamPaths.includes("/api/admin/deliveries/18/resolve"));
  const deliveryWrongContentType = await raw("/api/admin/deliveries/19/resolve", "POST", { "content-type": "text/plain" }, JSON.stringify({ outcome: "drop", note: "confirmed" }));
  assert.deepEqual(JSON.parse(deliveryWrongContentType.body), { owner: "go-admin-auth" });
  assert.ok(userApiPaths.includes("/api/admin/deliveries/19/resolve"));
  assert.ok(!upstreamPaths.includes("/api/admin/deliveries/19/resolve"));
  const deliveryInvalidId = await raw("/api/admin/deliveries/not-an-id/resolve", "POST", { "content-type": "application/json" }, JSON.stringify({ outcome: "drop", note: "confirmed" }));
  assert.deepEqual(JSON.parse(deliveryInvalidId.body), { owner: "go-admin-auth" });
  assert.ok(userApiPaths.includes("/api/admin/deliveries/not-an-id/resolve"));
  assert.ok(!upstreamPaths.includes("/api/admin/deliveries/not-an-id/resolve"));
  assert.ok(userApiMethods.includes("POST /api/admin/receipts/17/release"));
  assert.ok(!upstreamPaths.includes("/api/admin/receipts/17/release"));
  assert.ok(!upstreamPaths.includes("/api/admin/content/article-1"));
  const safeSourceUpdate = await raw("/api/admin/sources/rss-demo", "PATCH", { "content-type": "application/json" }, JSON.stringify({
    patch: { enabled: false, interval_minutes: 45, tags: ["ai"] }, version: "2026-10-07T12:30:00.123Z", reason: "pause",
  }));
  assert.equal(safeSourceUpdate.status, 200, logs);
  assert.ok(userApiPaths.includes("/api/admin/sources/rss-demo"), "non-public source settings use Go");
  assert.ok(!upstreamPaths.includes("/api/admin/sources/rss-demo"));
  const publicationSourceUpdate = await raw("/api/admin/sources/rss-demo", "PATCH", { "content-type": "application/json" }, JSON.stringify({
    patch: { tier: "T1" }, version: "2026-10-07T12:30:00.123Z", reason: "republish required",
  }));
  assert.equal(publicationSourceUpdate.status, 200);
  assert.ok(userApiPaths.includes("/api/admin/sources/rss-demo"), "publication-affecting source settings use Go");
  const itemAvailability = await raw("/api/site/items/availability", "GET");
  assert.equal(itemAvailability.status, 200);
  assert.ok(userApiPaths.includes("/api/site/items/availability"));
  assert.ok(!upstreamPaths.includes("/api/site/items/availability"));
  const topics = await raw("/api/site/topics", "GET");
  assert.equal(topics.status, 200);
  assert.ok(userApiPaths.includes("/api/site/topics"));
  assert.ok(!upstreamPaths.includes("/api/site/topics"));
  const topicDetail = await raw("/api/site/topics/openai", "GET");
  assert.equal(topicDetail.status, 200);
  assert.ok(userApiPaths.includes("/api/site/topics/openai"));
  assert.ok(!upstreamPaths.includes("/api/site/topics/openai"));
  const hotTopics = await raw("/api/v1/hot-topics", "GET");
  assert.equal(hotTopics.status, 200);
  assert.ok(userApiPaths.includes("/api/v1/hot-topics"));
  assert.ok(!upstreamPaths.includes("/api/v1/hot-topics"));
  const agentHot = await raw("/api/v1/agent/hot?limit=4", "GET");
  assert.equal(agentHot.status, 200);
  assert.ok(userApiPaths.includes("/api/v1/agent/hot?limit=4"));
  assert.ok(!upstreamPaths.includes("/api/v1/agent/hot?limit=4"));
  const agentGuide = await raw("/api/v1/agent", "GET");
  assert.equal(agentGuide.status, 200, logs);
  assert.ok(userApiPaths.includes("/api/v1/agent"));
  assert.ok(!upstreamPaths.includes("/api/v1/agent"));
  for (const path of ["/api/v1/agent/latest?window=7d&mode=all&limit=5", "/api/v1/agent/search?q=OpenAI&window=24h"]) {
    const response = await raw(path, "GET");
    assert.equal(response.status, 200, `${path}: ${logs}`);
    assert.ok(userApiPaths.includes(path), `${path} did not reach Go`);
    assert.ok(!upstreamPaths.includes(path), `${path} reached the legacy API`);
  }
  const agentLatestHead = await raw("/api/v1/agent/latest?limit=5", "HEAD");
  assert.equal(agentLatestHead.status, 200, logs);
  assert.ok(userApiMethods.includes("HEAD /api/v1/agent/latest?limit=5"));
  assert.equal(agentLatestHead.body, "");
  const agentStory = await raw("/api/v1/agent/stories/12345678-1234-4234-8234-123456789abc?limit=5", "GET");
  assert.equal(agentStory.status, 200, logs);
  assert.ok(userApiPaths.includes("/api/v1/agent/stories/12345678-1234-4234-8234-123456789abc?limit=5"));
  assert.ok(!upstreamPaths.includes("/api/v1/agent/stories/12345678-1234-4234-8234-123456789abc?limit=5"));
  for (const path of ["/api/v1/agent/daily", "/api/v1/agent/daily/2026-10-07", "/api/v1/agent/weekly", "/api/v1/agent/weekly/2026-W41", "/api/v1/agent/monthly", "/api/v1/agent/monthly/2026-10"]) {
    const response = await raw(path, "GET");
    assert.equal(response.status, 200, `${path}: ${logs}`);
    assert.ok(userApiPaths.includes(path), `${path} did not reach Go`);
    assert.ok(!upstreamPaths.includes(path), `${path} reached the legacy API`);
  }
  const items = await raw("/api/v1/items?limit=4", "GET");
  assert.equal(items.status, 200);
  assert.ok(userApiPaths.includes("/api/v1/items?limit=4"));
  assert.ok(!upstreamPaths.includes("/api/v1/items?limit=4"));
  for (const path of ["/api/v1/selected/snapshot?fields=minimal", "/api/v1/selected/changes?cursor=ax1.abc"]) {
    const response = await raw(path, "GET");
    assert.equal(response.status, 200, `${path}: ${logs}`);
    assert.ok(userApiPaths.includes(path), `${path} did not reach Go`);
    assert.ok(!upstreamPaths.includes(path), `${path} reached the legacy API`);
  }
  const storyPath = "/api/v1/stories/12345678-1234-4234-8234-123456789abc";
  const story = await raw(storyPath, "GET");
  assert.equal(story.status, 200, logs);
  assert.ok(userApiPaths.includes(storyPath));
  assert.ok(!upstreamPaths.includes(storyPath));
  for (const path of ["/api/v1/dailies?limit=6", "/api/v1/dailies/latest", "/api/v1/dailies/2026-10-07", "/api/v1/weeklies?limit=3", "/api/v1/weeklies/latest", "/api/v1/weeklies/2026-W41", "/api/v1/monthlies?limit=3", "/api/v1/monthlies/latest", "/api/v1/monthlies/2026-10"]) {
    const response = await raw(path, "GET");
    assert.equal(response.status, 200, `${path}: ${logs}`);
    assert.ok(userApiPaths.includes(path), `${path} did not reach Go`);
    assert.ok(!upstreamPaths.includes(path), `${path} reached the legacy API`);
  }
});

test("the configured IndexNow proof file is proxied to Go", async () => {
  const path = "/0123456789abcdef0123456789abcdef.txt";
  const beforeGo = userApiMethods.length;
  const response = await raw(path, "GET");
  assert.equal(response.status, 200, logs);
  assert.equal(response.body, "0123456789abcdef0123456789abcdef");
  assert.equal(response.headers["cache-control"], "public, max-age=3600");
  const unchanged = await raw(path, "GET", { "If-None-Match": response.headers.etag });
  assert.equal(unchanged.status, 304);
  const head = await raw(path, "HEAD");
  assert.equal(head.status, 200);
  assert.equal(head.body, "");
  assert.deepEqual(userApiMethods.slice(beforeGo), [`GET ${path}`, `GET ${path}`, `HEAD ${path}`]);
  assert.equal(upstreamPaths.includes(path), false);
});

test("root brand files are served by Web without an API request", async () => {
  const response = await raw("/favicon.ico", "GET");
  assert.equal(response.status, 200, logs);
  assert.equal(response.headers["content-type"], "image/x-icon");
  assert.equal(response.headers["cache-control"], "public, max-age=2592000, stale-while-revalidate=604800");
  assert.match(response.headers.etag ?? "", /^W\/"[0-9a-f]{16}"$/);
  const unchanged = await raw("/favicon.ico", "GET", { "If-None-Match": response.headers.etag });
  assert.equal(unchanged.status, 304);
  assert.equal(unchanged.body, "");
  const head = await raw("/favicon.ico", "HEAD");
  assert.equal(head.status, 200);
  assert.equal(head.body, "");
  assert.ok(!upstreamPaths.includes("/favicon.ico"));
  assert.ok(!userApiPaths.includes("/favicon.ico"));
});

test("anonymous root public documents are served by Web", async () => {
  const before = upstreamPaths.length;
  const robots = await raw("/robots.txt", "GET");
  assert.equal(robots.status, 200, logs);
  assert.match(robots.body, /Sitemap: /);
  assert.ok(!robots.body.includes("{{siteUrl}}"));
  assert.equal(robots.headers["cache-control"], "public, max-age=3600");
  assert.match(robots.headers.etag ?? "", /^W\/"[0-9a-f]{16}"$/);
  const unchanged = await raw("/robots.txt", "GET", { "If-None-Match": robots.headers.etag });
  assert.equal(unchanged.status, 304);
  assert.equal(unchanged.body, "");
  const head = await raw("/robots.txt", "HEAD");
  assert.equal(head.status, 200);
  assert.equal(head.body, "");
  const manifest = await raw("/manifest.webmanifest", "GET");
  assert.equal(manifest.status, 200, logs);
  assert.equal(manifest.headers["content-type"], "application/manifest+json");
  assert.equal(manifest.headers["cache-control"], "public, max-age=86400, stale-while-revalidate=604800");
  const manifestBody = JSON.parse(manifest.body) as { name: string; short_name: string; description: string };
  assert.ok(manifestBody.name && manifestBody.short_name && manifestBody.description);
  assert.ok(!manifest.body.includes("{{"));
  assert.deepEqual(upstreamPaths.slice(before), []);
  assert.ok(!userApiPaths.includes("/robots.txt"));
  assert.ok(!userApiPaths.includes("/manifest.webmanifest"));
});

test("authenticated sitemap requests are proxied to Go with private headers, HEAD, and ETag", async () => {
  const beforeGo = userApiPaths.length;
  const beforeLegacy = upstreamPaths.length;
  const anonymous = await raw("/sitemap.xml", "GET");
  assert.equal(anonymous.status, 401, logs);
  assert.equal(anonymous.headers["cache-control"], "private, no-store");
  const sitemap = await raw("/sitemap.xml", "GET", { cookie: "myhot_session=reader" });
  assert.equal(sitemap.status, 200, logs);
  assert.equal(sitemap.headers["content-type"], "application/xml");
  assert.equal(sitemap.headers["cache-control"], "private, no-store");
  assert.equal(sitemap.headers["surrogate-control"], "no-store");
  assert.equal(sitemap.headers.etag, 'W/"sitemap-fixture"');
  assert.match(sitemap.body, /^<urlset/);

  const unchanged = await raw("/sitemap.xml", "GET", { cookie: "myhot_session=reader", "if-none-match": '"sitemap-fixture"' });
  assert.equal(unchanged.status, 304);
  assert.equal(unchanged.body, "");
  const head = await raw("/sitemap.xml", "HEAD", { cookie: "myhot_session=reader" });
  assert.equal(head.status, 200);
  assert.equal(head.body, "");
  assert.deepEqual(userApiMethods.slice(beforeGo), ["GET /sitemap.xml", "GET /sitemap.xml", "GET /sitemap.xml", "HEAD /sitemap.xml"]);
  assert.deepEqual(userApiCookies.slice(beforeGo), ["", "myhot_session=reader", "myhot_session=reader", "myhot_session=reader"]);
  assert.deepEqual(upstreamPaths.slice(beforeLegacy), []);
});

test("authenticated llms document is proxied to Go with private headers, HEAD, and ETag", async () => {
  const beforeGo = userApiPaths.length;
  const beforeLegacy = upstreamPaths.length;
  const anonymous = await raw("/llms.txt", "GET");
  assert.equal(anonymous.status, 401, logs);
  assert.equal(anonymous.headers["cache-control"], "private, no-store");
  const document = await raw("/llms.txt", "GET", { cookie: "myhot_session=reader" });
  assert.equal(document.status, 200, logs);
  assert.equal(document.body, "# Example\n");
  assert.equal(document.headers["content-type"], "text/plain; charset=utf-8");
  assert.equal(document.headers["cache-control"], "private, no-store");
  assert.equal(document.headers["surrogate-control"], "no-store");
  const unchanged = await raw("/llms.txt", "GET", { cookie: "myhot_session=reader", "if-none-match": '"llms-fixture"' });
  assert.equal(unchanged.status, 304);
  const head = await raw("/llms.txt", "HEAD", { cookie: "myhot_session=reader" });
  assert.equal(head.status, 200);
  assert.equal(head.body, "");
  assert.deepEqual(userApiMethods.slice(beforeGo), ["GET /llms.txt", "GET /llms.txt", "GET /llms.txt", "HEAD /llms.txt"]);
  assert.deepEqual(userApiCookies.slice(beforeGo), ["", "myhot_session=reader", "myhot_session=reader", "myhot_session=reader"]);
  assert.deepEqual(upstreamPaths.slice(beforeLegacy), []);
});

test("site hot list is proxied to Go for GET and HEAD with verified-session cache rules", async () => {
  const beforeGo = userApiPaths.length;
  const beforeLegacy = upstreamPaths.length;
  const anonymous = await raw("/api/site/hot", "GET");
  assert.equal(anonymous.status, 401, logs);
  assert.equal(anonymous.headers["cache-control"], "private, no-store");
  const hot = await raw("/api/site/hot", "GET", { cookie: "myhot_session=reader" });
  assert.equal(hot.status, 200, logs);
  assert.deepEqual(JSON.parse(hot.body), { computedAt: null, windowHours: 48, entries: [] });
  assert.equal(hot.headers["cache-control"], "private, no-store");
  assert.equal(hot.headers["surrogate-control"], "no-store");
  const unchanged = await raw("/api/site/hot", "GET", { cookie: "myhot_session=reader", "if-none-match": 'W/"hot-fixture"' });
  assert.equal(unchanged.status, 304);
  const head = await raw("/api/site/hot", "HEAD", { cookie: "myhot_session=reader" });
  assert.equal(head.status, 200);
  assert.equal(head.body, "");
  assert.deepEqual(userApiMethods.slice(beforeGo), ["GET /api/site/hot", "GET /api/site/hot", "GET /api/site/hot", "HEAD /api/site/hot"]);
  assert.deepEqual(upstreamPaths.slice(beforeLegacy), []);
});

test("site timeline is proxied to Go with verified-session cache and conditional request behavior", async () => {
  const beforeGo = userApiMethods.length;
  const beforeLegacy = upstreamPaths.length;
  const anonymous = await raw("/api/site/timeline", "GET");
  assert.equal(anonymous.status, 401, logs);
  assert.equal(anonymous.headers["cache-control"], "private, no-store");
  const timeline = await raw("/api/site/timeline?channel=all", "GET", { cookie: "myhot_session=reader" });
  assert.equal(timeline.status, 200, logs);
  assert.equal(timeline.headers["cache-control"], "private, no-store");
  assert.equal(timeline.headers["surrogate-control"], "no-store");
  assert.deepEqual(JSON.parse(timeline.body), { filters: { channel: "all", category: null, tag: null }, cards: [], nextCursor: null, hot: null, dayCounts: {} });
  const unchanged = await raw("/api/site/timeline", "GET", { cookie: "myhot_session=reader", "if-none-match": 'W/"tl-fixture"' });
  assert.equal(unchanged.status, 304);
  const head = await raw("/api/site/timeline", "HEAD", { cookie: "myhot_session=reader" });
  assert.equal(head.status, 200);
  assert.equal(head.body, "");
  assert.deepEqual(userApiMethods.slice(beforeGo), ["GET /api/site/timeline", "GET /api/site/timeline?channel=all", "GET /api/site/timeline", "HEAD /api/site/timeline"]);
  assert.deepEqual(upstreamPaths.slice(beforeLegacy), []);
});

test("site pool is proxied to Go for GET and HEAD with verified-session cache rules", async () => {
  const beforeGo = userApiMethods.length;
  const beforeLegacy = upstreamPaths.length;
  const anonymous = await raw("/api/site/pool", "GET");
  assert.equal(anonymous.status, 401, logs);
  assert.equal(anonymous.headers["cache-control"], "private, no-store");
  const pool = await raw("/api/site/pool?channel=news&q=OpenAI&tab=relevance&page=2", "GET", { cookie: "myhot_session=reader" });
  assert.equal(pool.status, 200, logs);
  assert.deepEqual(JSON.parse(pool.body), { filters: { channel: "all", category: null, tag: null, q: null, tab: "time" }, items: [], page: 1, pageCount: 1, total: 0, todayCount: 0, freshness: "2026-10-08T00:00:00.000Z" });
  assert.equal(pool.headers["cache-control"], "private, no-store");
  assert.equal(pool.headers["surrogate-control"], "no-store");
  const unchanged = await raw("/api/site/pool", "GET", { cookie: "myhot_session=reader", "if-none-match": 'W/"pool-fixture"' });
  assert.equal(unchanged.status, 304);
  const head = await raw("/api/site/pool", "HEAD", { cookie: "myhot_session=reader" });
  assert.equal(head.status, 200);
  assert.equal(head.body, "");
  assert.deepEqual(userApiMethods.slice(beforeGo), ["GET /api/site/pool", "GET /api/site/pool?channel=news&q=OpenAI&tab=relevance&page=2", "GET /api/site/pool", "HEAD /api/site/pool"]);
  assert.ok(userApiCookies.slice(beforeGo).every((cookie) => cookie === "" || cookie === "myhot_session=reader"));
  assert.deepEqual(upstreamPaths.slice(beforeLegacy), []);
});

test("article detail and original text use the Go API behind the verified-session boundary", async () => {
  const beforeGo = userApiMethods.length;
  const beforeLegacy = upstreamPaths.length;
  const anonymous = await raw("/api/site/items/article_1", "GET");
  assert.equal(anonymous.status, 401, logs);
  assert.equal(anonymous.headers["cache-control"], "private, no-store");
  const detail = await raw("/api/site/items/article_1", "GET", { cookie: "myhot_session=reader" });
  assert.equal(detail.status, 200, logs);
  assert.deepEqual(JSON.parse(detail.body), { id: "article_1", readingMode: "full", body: null });
  assert.equal(detail.headers["cache-control"], "private, no-store");
  assert.equal(detail.headers["surrogate-control"], "no-store");
  const unchanged = await raw("/api/site/items/article_1", "GET", { cookie: "myhot_session=reader", "if-none-match": 'W/"item-fixture"' });
  assert.equal(unchanged.status, 304);
  const original = await raw("/api/site/items/article_1/original", "HEAD", { cookie: "myhot_session=reader" });
  assert.equal(original.status, 200);
  assert.equal(original.body, "");
  assert.deepEqual(userApiMethods.slice(beforeGo), ["GET /api/site/items/article_1", "GET /api/site/items/article_1", "GET /api/site/items/article_1", "HEAD /api/site/items/article_1/original"]);
  assert.deepEqual(upstreamPaths.slice(beforeLegacy), []);
});

test("Markdown download keeps its URL, forwards the session, and uses Go", async () => {
  const before = userApiMethods.length;
  const anonymous = await raw("/items/article_1/markdown", "GET");
  assert.equal(anonymous.status, 401);
  const download = await raw("/items/article_1/markdown", "GET", { cookie: "myhot_session=reader" });
  assert.equal(download.status, 200);
  assert.equal(download.headers["content-type"], "text/markdown; charset=utf-8");
  assert.equal(download.headers["content-disposition"], 'attachment; filename="myhot-article_1.md"');
  assert.equal(download.headers["cache-control"], "public, max-age=300, s-maxage=300");
  assert.equal(download.body, "# Article\n");
  assert.deepEqual(userApiMethods.slice(before), ["GET /items/article_1/markdown", "GET /items/article_1/markdown"]);
  assert.deepEqual(userApiCookies.slice(-2), ["", "myhot_session=reader"]);
  assert.equal(upstreamPaths.includes("/items/article_1/markdown"), false);
});

test("story detail is proxied to Go with session, ETag, and HEAD handling", async () => {
  const beforeGo = userApiMethods.length;
  const beforeLegacy = upstreamPaths.length;
  const path = "/api/site/stories/12345678-1234-4234-8234-123456789abc";
  const anonymous = await raw(path, "GET");
  assert.equal(anonymous.status, 401, logs);
  const detail = await raw(path, "GET", { cookie: "myhot_session=reader" });
  assert.equal(detail.status, 200, logs);
  assert.deepEqual(JSON.parse(detail.body), { publicId: "12345678-1234-4234-8234-123456789abc", title: "Story", timeline: [], heat: [] });
  assert.equal(detail.headers["cache-control"], "private, no-store");
  const unchanged = await raw(path, "GET", { cookie: "myhot_session=reader", "if-none-match": 'W/"story-fixture"' });
  assert.equal(unchanged.status, 304);
  const head = await raw(path, "HEAD", { cookie: "myhot_session=reader" });
  assert.equal(head.status, 200);
  assert.equal(head.body, "");
  assert.deepEqual(userApiMethods.slice(beforeGo), [`GET ${path}`, `GET ${path}`, `GET ${path}`, `HEAD ${path}`]);
  assert.deepEqual(upstreamPaths.slice(beforeLegacy), []);
});

test("OpenAPI is served by Go behind the verified session boundary", async () => {
  const openapi = await raw("/openapi-v1.json", "GET", { cookie: "myhot_session=reader" });
  assert.equal(openapi.status, 200, logs);
  assert.ok(userApiPaths.includes("/openapi-v1.json"));
  assert.ok(!upstreamPaths.includes("/openapi-v1.json"));
});

test("MCP requests and preflight use Go while forwarding the verified session", async () => {
  const body = JSON.stringify({ jsonrpc: "2.0", id: 1, method: "tools/list" });
  const response = await raw("/api/mcp", "POST", {
    cookie: "myhot_session=reader", "content-type": "application/json", accept: "application/json",
  }, body);
  assert.equal(response.status, 200, logs);
  assert.deepEqual(JSON.parse(response.body), { owner: "go-mcp" });
  assert.ok(userApiMethods.includes("POST /api/mcp"));
  assert.ok(userApiCookies.includes("myhot_session=reader"));
  assert.ok(!upstreamPaths.includes("/api/mcp"));

  const preflight = await raw("/api/mcp", "OPTIONS", {
    origin: "http://localhost:3000", "access-control-request-method": "POST",
  });
  assert.equal(preflight.status, 200, logs);
  assert.ok(userApiMethods.includes("OPTIONS /api/mcp"));
  assert.ok(!upstreamPaths.includes("/api/mcp"));
});

test("uploaded contact images are served by Go", async () => {
  const response = await raw("/contact/qr-wechat-1234abcd.png", "GET");
  assert.equal(response.status, 200, logs);
  assert.ok(userApiPaths.includes("/contact/qr-wechat-1234abcd.png"));
  assert.ok(!upstreamPaths.includes("/contact/qr-wechat-1234abcd.png"));
});
