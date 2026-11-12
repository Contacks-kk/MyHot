// Manual browser acceptance against the real built Web and a local fake API/mailbox.
import { createServer } from "node:http";
import { spawn } from "node:child_process";
import { once } from "node:events";
import { fileURLToPath } from "node:url";

let expiry = 0, checks = 0, registrations = 0, failSession = false;
const api = createServer(async (req, res) => {
  const path = new URL(req.url, "http://localhost").pathname;
  res.setHeader("Content-Type", "application/json");
  res.setHeader("Cache-Control", "no-store");
  const send = (status, value) => { res.statusCode = status; res.end(JSON.stringify(value)); };
  const parts = []; for await (const part of req) parts.push(part);
  const body = parts.length ? JSON.parse(Buffer.concat(parts)) : {};
  if (path === "/api/site/fixture-state") {
    if (req.method === "POST") failSession = Boolean(body.failSession);
    return send(200, { checks, registrations });
  }
  if (path === "/api/auth/registration-status") return send(200, { enabled: true });
  if (path === "/api/auth/verify/resend") {
    expiry = Date.now() + 60_000;
    return send(202, { message: "本地假邮件已发送", serverTime: new Date().toISOString(), expiresAt: new Date(expiry).toISOString(), retryAt: new Date(expiry).toISOString() });
  }
  if (path === "/api/auth/register/code/check") {
    checks++;
    if (body.code !== "001234" || Date.now() >= expiry) return send(400, { error: "验证码错误" });
    return send(200, { verified: true, serverTime: new Date().toISOString(), expiresAt: new Date(expiry).toISOString() });
  }
  if (path === "/api/auth/register") {
    if (body.code !== "001234" || Date.now() >= expiry) return send(400, { error: "验证码错误" });
    registrations++; expiry = 0;
    res.setHeader("Set-Cookie", "myhot_session=browser-fixture; Path=/; HttpOnly; SameSite=Lax");
    return send(201, { user: { id: 1, email: "browser@example.invalid" }, csrfToken: "fixture-csrf" });
  }
  if (path === "/api/site/meta") return send(200, { changelogVersion: null });
  if (path === "/api/site/changelog") return send(200, { latestVersion: "fixture", releases: [] });
  if (failSession || !req.headers.cookie?.includes("myhot_session=browser-fixture")) return send(401, { error: "unauthorized" });
  if (path === "/api/me") return send(200, { id: 1, email: "browser@example.invalid", csrfToken: "fixture-csrf" });
  if (path === "/api/me/model") return send(200, { configured: false, baseUrl: "", model: "" });
  if (path === "/api/me/interests") return send(200, { keywords: [], entityIds: [] });
  if (path === "/api/me/daily") return send(200, { reports: [] });
  if (["/api/me/feed", "/api/me/hot"].includes(path)) return send(200, { items: [] });
  return send(404, {});
});
api.listen(0, "127.0.0.1"); await once(api, "listening");
const web = spawn(process.execPath, [fileURLToPath(new URL("../../server.ts", import.meta.url))], {
  env: { ...process.env, WEB_PORT: "0", TRUST_PROXY: "false", GO_API_BASE_URL: `http://127.0.0.1:${api.address().port}` }, stdio: ["ignore", "pipe", "pipe"],
});
web.stdout.on("data", chunk => {
  const port = String(chunk).match(/"msg":"web started","port":(\d+)/)?.[1];
  if (port) console.log(`Browser fixture: http://127.0.0.1:${port}/register ; fake code: 001234`);
});
web.stderr.on("data", chunk => process.stderr.write(chunk));
process.on("SIGTERM", () => { web.kill(); api.close(); });
web.on("exit", () => { api.close(); });
