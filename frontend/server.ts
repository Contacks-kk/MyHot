// Production web server: built client assets, the shared redirect table and SSR. Api-owned paths are
// proxied to the api process, so one port serves the whole site; a reverse proxy in front may also send
// them to the api directly.
import { createReadStream } from "node:fs";
import { stat } from "node:fs/promises";
import { createServer } from "node:http";
import { randomUUID } from "node:crypto";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { createRequestListener } from "@react-router/node";
import { isApiOwned, resolveRedirect } from "@aihot/contracts/http-policy";
import { proxyToApi } from "./app/lib/api-proxy.server.ts";
import { logError } from "./app/lib/errors.server.ts";
import { isSiteBrandPath, isSitePublicPath, serveSiteBrand, serveSitePublic } from "./app/lib/site-brand.server.ts";
import { parseOgPrewarmRequest } from "./app/lib/og-prewarm.server.ts";

const PORT = Number(process.env.WEB_PORT || 3000);
const HOST = process.env.WEB_HOST || "127.0.0.1";
/**
 * Whether a reverse proxy in front (Caddy, nginx) records the visitor in X-Forwarded-For. Without one
 * the header is never believed: a visitor could name any address and slip past the api's per-visitor
 * limits (sign-in attempts, feedback).
 */
const TRUST_PROXY = process.env.TRUST_PROXY === "true";
const USER_API_BASE_URL = process.env.GO_API_BASE_URL || "http://127.0.0.1:3002";
const CLIENT_DIR = path.resolve(import.meta.dirname, "build/client");
/** Browsers keep a page at most this long, so a withdrawal reaches them within minutes. */

import { isAnonymousPage, pagePath } from "./app/lib/access.ts";
import { isWebOgPath, webOgCard, webOgContentCard, webOgPoster, webOgTarget } from "./app/lib/og-assets.server.ts";
import { ogEtag, renderOg } from "@aihot/og-renderer/og";
import { posterEtag, renderPoster } from "@aihot/og-renderer/poster";

const TYPES: Record<string, string> = {
  ".js": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".svg": "image/svg+xml",
  ".png": "image/png",
  ".jpg": "image/jpeg",
  ".webp": "image/webp",
  ".woff2": "font/woff2",
  ".json": "application/json",
  ".txt": "text/plain; charset=utf-8",
  ".ico": "image/x-icon",
  ".map": "application/json",
};

// A file URL, not a path: on Windows import() reads "C:\..." as a URL with the scheme "c:".
const build = await import(pathToFileURL(path.resolve(import.meta.dirname, "build/server/index.js")).href);
const ssr = createRequestListener({ build, mode: "production" });

class BadRequest extends Error {}

/** Hashed build assets are immutable; anything else from the client build gets a short cache. */
async function serveStatic(pathname: string, res: import("node:http").ServerResponse): Promise<boolean> {
  if (pathname.includes("..") || pathname.endsWith("/")) return false;
  let decoded: string;
  try {
    decoded = decodeURIComponent(pathname);
  } catch {
    throw new BadRequest("malformed percent-encoding");
  }
  const file = path.join(CLIENT_DIR, decoded);
  if (!file.startsWith(CLIENT_DIR)) return false;
  const info = await stat(file).catch(() => null);
  if (!info?.isFile()) return false;
  const immutable = pathname.startsWith("/assets/");
  res.writeHead(200, {
    "Content-Type": TYPES[path.extname(file)] ?? "application/octet-stream",
    "Content-Length": info.size,
    "Cache-Control": immutable ? "public, max-age=31536000, immutable" : "public, max-age=3600",
    "X-Content-Type-Options": "nosniff",
  });
  createReadStream(file).pipe(res);
  return true;
}

// One bad request must never take the process down: answer it and keep serving.
const server = createServer((req, res) => {
  handle(req, res).catch((error: unknown) => {
    const bad = error instanceof BadRequest || error instanceof URIError;
    if (!bad) logError(error, { msg: "web request failed", method: req.method, path: (req.url ?? "").split("?")[0]!.slice(0, 200) });
    if (res.headersSent) return res.destroy();
    res.writeHead(bad ? 400 : 500, { "Content-Type": "text/plain; charset=utf-8", "Cache-Control": "no-store" });
    res.end(bad ? "Bad request" : "Internal error");
  });
});

/** Every rendered document/data response can contain session identity. */
function pageResponse(req: import("node:http").IncomingMessage, res: import("node:http").ServerResponse) {
  const raw = req.url ?? "/";
  const url = new URL(raw.startsWith("/") ? `http://web.local${raw}` : raw, "http://web.local");
  const writeHead = res.writeHead.bind(res);
  res.writeHead = ((status: number, messageOrHeaders?: string | import("node:http").OutgoingHttpHeaders, headers?: import("node:http").OutgoingHttpHeaders) => {
    const outgoing = typeof messageOrHeaders === "string" ? headers : messageOrHeaders;
    for (const [name, value] of Object.entries(outgoing ?? {})) if (value !== undefined) res.setHeader(name, value);
    if (url.pathname.endsWith(".data") && res.getHeader("Content-Type") === "text/x-script") res.setHeader("Content-Type", "text/plain; charset=utf-8");
    res.removeHeader("Expires");
    res.setHeader("Cache-Control", "private, no-store");
    res.setHeader("X-Accel-Expires", "0");
    return typeof messageOrHeaders === "string" ? writeHead(status, messageOrHeaders) : writeHead(status);
  }) as typeof res.writeHead;
}

async function handle(req: import("node:http").IncomingMessage, res: import("node:http").ServerResponse) {
  const raw = req.url ?? "/";
  const qi = raw.indexOf("?");
  const pathname = qi >= 0 ? raw.slice(0, qi) : raw;
  const search = qi >= 0 ? raw.slice(qi) : "";

  const decision = resolveRedirect(pathname, search);
  if (decision) {
    for (const [k, v] of Object.entries(decision.headers)) res.setHeader(k, v);
    if (decision.location) res.setHeader("Location", decision.location);
    res.statusCode = decision.status;
    return res.end(decision.location ? undefined : decision.status === 410 ? "Gone" : "Not found");
  }

  if (pathname === "/_internal/og/prewarm") return prewarmOgImage(req, res);

  if ((req.method === "GET" || req.method === "HEAD") && isSitePublicPath(pathname) && await serveSitePublic(pathname, req.method, req.headers["if-none-match"], res)) return;
  if ((req.method === "GET" || req.method === "HEAD") && isSiteBrandPath(pathname) && await serveSiteBrand(pathname, req.method, req.headers["if-none-match"], res)) return;

  if (isWebOgPath(pathname)) return serveWebOg(req, res, pathname);

  if (isApiOwned(pathname) || isSiteBrandPath(pathname)) {
    // The visitor's address, decided here: the one the trusted proxy saw (the last X-Forwarded-For
    // entry), or this connection's own. Both headers carry only that.
    const forwarded = String(req.headers["x-forwarded-for"] ?? "").split(",").map((v) => v.trim()).filter(Boolean);
    const client = TRUST_PROXY && forwarded.length ? forwarded[forwarded.length - 1]! : (req.socket.remoteAddress ?? "");
    return proxyToApi(req, res, { "x-forwarded-for": client, "x-real-ip": client, "x-forwarded-host": req.headers.host ?? "" });
  }

  if ((req.method === "GET" || req.method === "HEAD") && pathname.includes(".") && (await serveStatic(pathname, res))) return;
  const routePath = pagePath(pathname);
  const publicPage = isAnonymousPage(pathname);
  if (!publicPage && !/^\/(?:admin|super)(?:\/|$)/i.test(routePath)) {
    const upstream = await fetch(`${USER_API_BASE_URL}/api/me`, {
      headers: { cookie: req.headers.cookie ?? "", accept: "application/json" },
      signal: AbortSignal.timeout(3000),
    }).catch(() => null);
    if (!upstream || (upstream.status !== 401 && !upstream.ok)) {
      res.writeHead(503, { "Content-Type": "text/plain; charset=utf-8", "Cache-Control": "private, no-store" });
      return res.end("Account service unavailable");
    }
    if (!upstream.ok) {
      const target = `${pathname}${search}`;
      res.writeHead(302, { Location: `/login?${new URLSearchParams({ return: target })}`, "Cache-Control": "private, no-store" });
      return res.end();
    }
  }
  pageResponse(req, res);
  return ssr(req, res);
}

async function prewarmOgImage(req: import("node:http").IncomingMessage, res: import("node:http").ServerResponse): Promise<void> {
  if (req.method !== "POST" || !process.env.WEB_OG_PREWARM_SECRET) {
    res.writeHead(404, { "Cache-Control": "no-store" });
    res.end();
    return;
  }
  const chunks: Buffer[] = [];
  let size = 0;
  for await (const chunk of req) {
    const data = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk);
    size += data.length;
    if (size > 16 * 1024) {
      res.writeHead(413, { "Cache-Control": "no-store" });
      res.end();
      return;
    }
    chunks.push(data);
  }
  const signature = req.headers["x-og-prewarm-signature"];
  const parsed = parseOgPrewarmRequest(Buffer.concat(chunks), Array.isArray(signature) ? signature[0] : signature, process.env.WEB_OG_PREWARM_SECRET);
  if (!parsed) {
    res.writeHead(404, { "Cache-Control": "no-store" });
    res.end();
    return;
  }
  try {
    await renderOg(parsed.card);
    res.writeHead(204, { "Cache-Control": "no-store" });
    res.end();
  } catch (error) {
    logError(error, { msg: "share image prewarm failed", path: "/_internal/og/prewarm" });
    res.writeHead(503, { "Cache-Control": "no-store" });
    res.end();
  }
}

async function serveWebOg(req: import("node:http").IncomingMessage, res: import("node:http").ServerResponse, pathname: string): Promise<void> {
  const method = req.method ?? "GET";
  if (method === "OPTIONS") {
    const body = "Not found";
    res.writeHead(404, { "Content-Type": "text/plain; charset=utf-8", "Content-Length": Buffer.byteLength(body), "Cache-Control": "public, max-age=60" });
    res.end(body);
    return;
  }

  const requestId = randomUUID();
  const auth = await fetch(`${USER_API_BASE_URL}/api/me`, {
    headers: { cookie: req.headers.cookie ?? "", accept: "application/json" },
    signal: AbortSignal.timeout(3000),
  }).catch(() => null);
  if (!auth || (auth.status !== 401 && !auth.ok)) {
    sendOgProblem(req, res, 503, "temporarily_unavailable", "Temporarily unavailable.", requestId, 30);
    return;
  }
  if (auth.status === 401) {
    sendOgProblem(req, res, 401, "unauthorized", "Sign in to view content.", requestId);
    return;
  }
  if (method !== "GET" && method !== "HEAD") {
    sendOgNotFound(res, method);
    return;
  }

  let card: ReturnType<typeof webOgCard> = null;
  let poster = null as ReturnType<typeof webOgPoster>;
  let target: ReturnType<typeof webOgTarget>;
  try {
    card = webOgCard(pathname);
    target = webOgTarget(pathname);
  } catch {
    sendOgProblem(req, res, 400, "invalid_request", "The request could not be processed.", requestId);
    return;
  }
  if (!card && target) {
    const content = await fetch(`${USER_API_BASE_URL}${target.apiPath}`, {
      headers: { cookie: req.headers.cookie ?? "", accept: "application/json" },
      cache: "no-store",
      redirect: "manual",
      signal: AbortSignal.timeout(10_000),
    }).catch(() => null);
    if (!content) {
      sendOgProblem(req, res, 503, "temporarily_unavailable", "Temporarily unavailable.", requestId, 30);
      return;
    }
    if (content.status === 401) {
      sendOgProblem(req, res, 401, "unauthorized", "Sign in to view content.", requestId);
      return;
    }
    if (!content.ok) {
      if ([308, 400, 404, 410].includes(content.status)) {
        sendOgNotFound(res, method);
        return;
      }
      sendOgProblem(req, res, 503, "temporarily_unavailable", "Temporarily unavailable.", requestId, 30);
      return;
    }
    try {
      const data: unknown = await content.json();
      if (target.kind === "poster") poster = webOgPoster(target.id, data);
      else card = webOgContentCard(target, data);
    } catch {
      card = null;
      poster = null;
    }
  }
  if (!card && !poster) {
    sendOgNotFound(res, method);
    return;
  }

  const etag = poster ? `"poster-${posterEtag(poster)}"` : `"og-${ogEtag(card!)}"`;
  const headers = { ETag: etag, "Cache-Control": "private, no-store", "Surrogate-Control": "no-store" };
  const matches = String(req.headers["if-none-match"] ?? "").split(",").some((value) => {
    const candidate = value.trim();
    return candidate === "*" || candidate.replace(/^W\//, "") === etag;
  });
  if (matches) {
    res.writeHead(304, headers);
    res.end();
    return;
  }

  try {
    const result = poster ? await renderPoster(poster) : await renderOg(card!);
    res.writeHead(200, {
      ...headers,
      "Content-Type": "image/png",
      "Content-Length": result.png.length,
    });
    res.end(method === "HEAD" ? undefined : result.png);
  } catch (error) {
    logError(error, { msg: "web share card render failed", path: pathname });
    sendOgProblem(req, res, 503, "temporarily_unavailable", "Temporarily unavailable.", requestId, 30);
  }
}

function sendOgNotFound(res: import("node:http").ServerResponse, method: string): void {
  const body = "Not found";
  res.writeHead(404, {
    "Content-Type": "text/plain; charset=utf-8",
    "Content-Length": Buffer.byteLength(body),
    "Cache-Control": "private, no-store",
    "Surrogate-Control": "no-store",
  });
  res.end(method === "HEAD" ? undefined : body);
}

function sendOgProblem(
  req: import("node:http").IncomingMessage,
  res: import("node:http").ServerResponse,
  status: number,
  code: string,
  detail: string,
  requestId: string,
  retryAfter?: number,
): void {
  const titles: Record<number, string> = { 400: "Bad request", 503: "Temporarily unavailable" };
  const body = Buffer.from(JSON.stringify({
    type: `/problems/${code.replace(/_/g, "-")}`,
    title: titles[status] ?? "Error",
    status,
    detail,
    code,
    requestId,
    ...(retryAfter === undefined ? {} : { retryAfter }),
  }));
  res.writeHead(status, {
    "Content-Type": "application/problem+json",
    "Content-Length": body.length,
    "Cache-Control": "private, no-store",
    "Surrogate-Control": "no-store",
    "X-Request-Id": requestId,
    ...(retryAfter === undefined ? {} : { "Retry-After": String(retryAfter) }),
  });
  res.end((req.method ?? "GET") === "HEAD" ? undefined : body);
}

process.on("unhandledRejection", (reason) => {
  logError(reason, { msg: "unhandled rejection" });
});

server.keepAliveTimeout = 65_000;
server.listen(PORT, HOST, () => console.log(JSON.stringify({ level: "info", msg: "web started", port: (server.address() as import("node:net").AddressInfo).port, pid: process.pid })));

const shutdown = () => server.close(() => process.exit(0));
process.on("SIGTERM", shutdown);
process.on("SIGINT", shutdown);
