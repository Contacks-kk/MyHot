// The React Router Web process is the same-origin boundary for the Go business API.
// It forwards end-to-end headers and streams the response without reading account data.
import { type IncomingHttpHeaders, type IncomingMessage, type OutgoingHttpHeaders, type ServerResponse, request as httpRequest } from "node:http";
import type { Duplex } from "node:stream";
import { CONTACT_ALIASES } from "@aihot/contracts/http-policy";
import { USER_API_BASE_URL } from "./user-api.server.ts";
import { logError } from "./errors.server.ts";

const API = new URL(USER_API_BASE_URL);
const HOP_BY_HOP = ["connection", "keep-alive", "proxy-connection", "te", "transfer-encoding", "upgrade"];

export function contactAliasTarget(pathname: string, aliases: ReadonlyArray<(typeof CONTACT_ALIASES)[number]> = CONTACT_ALIASES): string | null {
  const alias = aliases.find((candidate) => pathname === `/${candidate.file}`);
  return alias ? `/api/site/contact-alias/${alias.slot}` : null;
}

/** The headers minus hop-by-hop fields, including any named by Connection. */
function endToEnd(headers: IncomingHttpHeaders): IncomingHttpHeaders {
  const drop = new Set([...HOP_BY_HOP, ...String(headers.connection ?? "").toLowerCase().split(",").map((name) => name.trim())]);
  return Object.fromEntries(Object.entries(headers).filter(([name]) => !drop.has(name)));
}

/** Forward every API-owned request to Go; static brand assets and SSR documents stay in Web. */
export function proxyToApi(req: IncomingMessage, res: ServerResponse, set: OutgoingHttpHeaders = {}): void {
  const pathname = (req.url ?? "/").split("?", 1)[0] ?? "/";
  const alias = contactAliasTarget(pathname);
  const rewrite = alias && (req.method === "GET" || req.method === "HEAD") ? alias : undefined;
  const query = rewrite && (req.url ?? "").includes("?") ? `?${(req.url ?? "").split("?").slice(1).join("?")}` : "";
  forward(req, res, set, rewrite ? `${rewrite}${query}` : undefined);
}

function forward(req: IncomingMessage, res: ServerResponse, set: OutgoingHttpHeaders, pathOverride?: string): void {
  const headers: OutgoingHttpHeaders = { ...endToEnd(req.headers), ...set };
  if (req.headers["transfer-encoding"] && req.headers["content-length"] === undefined) headers["transfer-encoding"] = "chunked";
  const upstream = httpRequest({
    hostname: API.hostname,
    port: API.port,
    path: pathOverride ?? req.url,
    method: req.method,
    headers,
  }, (response) => {
    res.writeHead(response.statusCode ?? 502, endToEnd(response.headers));
    response.pipe(res);
  });
  upstream.on("error", (error) => {
    logError(error, { msg: "Go API proxy request failed", method: req.method, path: req.url });
    if (res.headersSent) return res.destroy(error);
    res.statusCode = 502;
    res.end("api unavailable");
  });
  req.pipe(upstream);
}

/** Tunnel an authenticated same-origin WebSocket upgrade to Go API while preserving forwarding headers. */
export function proxyWsUpgrade(req: IncomingMessage, clientSocket: Duplex, head: Buffer, set: OutgoingHttpHeaders = {}): void {
  const headers: OutgoingHttpHeaders = { ...req.headers, ...set };
  const upstream = httpRequest({
    hostname: API.hostname,
    port: API.port,
    path: req.url,
    method: req.method ?? "GET",
    headers,
  });

  upstream.on("upgrade", (proxyRes, proxySocket, proxyHead) => {
    const status = proxyRes.statusCode ?? 101;
    const statusText = proxyRes.statusMessage ?? "Switching Protocols";
    const lines = [`HTTP/1.1 ${status} ${statusText}`];

    for (const [name, value] of Object.entries(proxyRes.headers)) {
      if (Array.isArray(value)) {
        for (const item of value) {
          lines.push(`${name}: ${item}`);
        }
      } else if (value !== undefined) {
        lines.push(`${name}: ${value}`);
      }
    }

    clientSocket.write(`${lines.join("\r\n")}\r\n\r\n`);
    if (proxyHead.length > 0) {
      clientSocket.write(proxyHead);
    }
    if (head.length > 0) {
      proxySocket.write(head);
    }

    proxySocket.on("error", () => clientSocket.destroy());
    clientSocket.on("error", () => proxySocket.destroy());
    proxySocket.pipe(clientSocket);
    clientSocket.pipe(proxySocket);
  });

  // Forward upstream HTTP rejections cleanly when upgrade is refused (e.g., 401/403).
  upstream.on("response", (proxyRes) => {
    const status = proxyRes.statusCode ?? 502;
    const statusText = proxyRes.statusMessage ?? "Bad Gateway";
    const lines = [`HTTP/1.1 ${status} ${statusText}`];

    for (const [name, value] of Object.entries(proxyRes.headers)) {
      if (Array.isArray(value)) {
        for (const item of value) {
          lines.push(`${name}: ${item}`);
        }
      } else if (value !== undefined) {
        lines.push(`${name}: ${value}`);
      }
    }

    clientSocket.write(`${lines.join("\r\n")}\r\n\r\n`);
    proxyRes.on("error", () => clientSocket.destroy());
    proxyRes.pipe(clientSocket);
  });

  upstream.on("error", (error) => {
    logError(error, { msg: "Go API WebSocket proxy failed", path: req.url });
    if (!clientSocket.destroyed) {
      clientSocket.write("HTTP/1.1 502 Bad Gateway\r\nConnection: close\r\n\r\n");
      clientSocket.destroy();
    }
  });

  clientSocket.on("error", () => {
    upstream.destroy();
  });

  upstream.end();
}
