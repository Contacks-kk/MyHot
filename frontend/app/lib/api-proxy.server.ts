// The React Router Web process is the same-origin boundary for the Go business API.
// It forwards end-to-end headers and streams the response without reading account data.
import { type IncomingHttpHeaders, type IncomingMessage, type OutgoingHttpHeaders, type ServerResponse, request as httpRequest } from "node:http";
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
