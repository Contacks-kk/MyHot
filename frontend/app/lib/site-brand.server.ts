import { createHash } from "node:crypto";
import { readFile, stat } from "node:fs/promises";
import path from "node:path";
import { EDITION_TIMES, SITE } from "@aihot/site";
import { PUBLIC_INTERFACE_VERSION } from "@aihot/contracts/http-policy";
import { PUBLIC_API_CATEGORY_KEYS } from "@aihot/contracts/taxonomy";

const BRAND_DIR = path.resolve(import.meta.dirname, "../../../shared/site/brand");
const PUBLIC_DIR = path.resolve(import.meta.dirname, "../../../shared/site/public");
const BRAND_FILES = new Set([
  "favicon.ico", "icon.png", "icon-192.png", "apple-icon.png", "logo.svg",
  ...SITE.rootIcons.filter((name) => name !== "." && name !== ".." && !name.includes("/") && !name.includes("\\")),
]);
const TYPES: Record<string, string> = {
  ".svg": "image/svg+xml",
  ".png": "image/png",
  ".jpg": "image/jpeg",
  ".jpeg": "image/jpeg",
  ".webp": "image/webp",
  ".ico": "image/x-icon",
};
const CACHE_CONTROL = "public, max-age=2592000, stale-while-revalidate=604800";
const PUBLIC_FILES = new Map([
  ["/robots.txt", { relative: "robots.txt", type: "text/plain; charset=UTF-8", cacheControl: "public, max-age=3600", json: false }],
  ["/manifest.webmanifest", { relative: "manifest.webmanifest", type: "application/manifest+json", cacheControl: "public, max-age=86400, stale-while-revalidate=604800", json: true }],
  ["/.well-known/security.txt", { relative: ".well-known/security.txt", type: "text/plain; charset=UTF-8", cacheControl: "public, max-age=86400", json: false }],
]);
const publicFileCache = new Map<string, { body: Buffer; etag: string; mtime: number }>();

function fillPlaceholders(text: string, json: boolean): string {
  const values: Record<string, string> = {
    siteName: SITE.name,
    siteUrl: (process.env.SITE_URL || SITE.defaultUrl).replace(/\/+$/, ""),
    description: SITE.description,
    tagline: SITE.tagline,
    locale: SITE.locale,
    dailyTime: EDITION_TIMES.daily,
    weeklyTime: EDITION_TIMES.weekly,
    monthlyTime: EDITION_TIMES.monthly,
    version: PUBLIC_INTERFACE_VERSION,
    categoryList: PUBLIC_API_CATEGORY_KEYS.join(", "),
  };
  const filled = text.replace(/\{\{(\w+)\}\}/g, (all, key: string) => {
    const value = values[key];
    if (value === undefined) return all;
    return json ? JSON.stringify(value).slice(1, -1) : value;
  });
  if (!json || !filled.includes('"{{categories}}"')) return filled;
  const document: unknown = JSON.parse(filled);
  const walk = (node: unknown) => {
    if (!node || typeof node !== "object") return;
    const object = node as Record<string, unknown>;
    for (const key of ["enum", "examples"]) {
      if (Array.isArray(object[key]) && (object[key] as unknown[]).includes("{{categories}}")) object[key] = [...PUBLIC_API_CATEGORY_KEYS];
    }
    for (const value of Object.values(object)) walk(value);
  };
  walk(document);
  return JSON.stringify(document, null, 2);
}

/** Whether the path names one of the root-level files configured as site branding. */
export function isSiteBrandPath(pathname: string): boolean {
  let decoded: string;
  try {
    decoded = decodeURIComponent(pathname);
  } catch {
    return false;
  }
  if (!decoded.startsWith("/") || decoded.slice(1).includes("/") || decoded.includes("\\")) return false;
  return BRAND_FILES.has(decoded.slice(1));
}

/** Root-level site documents allowed anonymously by the HTTP contract; OpenAPI stays behind its API session gate. */
export function isSitePublicPath(pathname: string): boolean {
  return PUBLIC_FILES.has(pathname);
}

/** Serves the same bytes, validator, MIME type, and cache lifetime as the former API static route. */
export async function serveSiteBrand(pathname: string, method: string, ifNoneMatch: string | undefined, res: import("node:http").ServerResponse): Promise<boolean> {
  if (!isSiteBrandPath(pathname)) return false;
  const name = decodeURIComponent(pathname).slice(1);
  const body = await readFile(path.join(BRAND_DIR, name)).catch(() => null);
  if (!body) {
    res.writeHead(404, { "Content-Type": "text/plain; charset=utf-8", "Content-Length": Buffer.byteLength("Not found") });
    res.end(method === "HEAD" ? undefined : "Not found");
    return true;
  }

  const etag = `W/"${createHash("sha256").update(body).digest("hex").slice(0, 16)}"`;
  res.setHeader("Content-Type", TYPES[path.extname(name)] ?? "application/octet-stream");
  res.setHeader("Cache-Control", CACHE_CONTROL);
  res.setHeader("ETag", etag);
  if (ifNoneMatch === etag) {
    res.writeHead(304);
    res.end();
    return true;
  }
  res.setHeader("Content-Length", body.length);
  res.writeHead(200);
  res.end(method === "HEAD" ? undefined : body);
  return true;
}

/** The anonymous static site files keep their placeholder expansion, validators and cache lifetime. */
export async function serveSitePublic(pathname: string, method: string, ifNoneMatch: string | undefined, res: import("node:http").ServerResponse): Promise<boolean> {
  const file = PUBLIC_FILES.get(pathname);
  if (!file) return false;
  const target = path.join(PUBLIC_DIR, file.relative);
  const info = await stat(target).catch(() => null);
  if (!info?.isFile()) {
    res.writeHead(404, { "Content-Type": "text/plain; charset=utf-8", "Content-Length": Buffer.byteLength("Not found") });
    res.end(method === "HEAD" ? undefined : "Not found");
    return true;
  }
  let entry = publicFileCache.get(pathname);
  if (!entry || entry.mtime !== info.mtimeMs) {
    let body = await readFile(target);
    if (body.includes("{{")) body = Buffer.from(fillPlaceholders(body.toString("utf8"), file.json));
    entry = { body, etag: `W/\"${createHash("sha256").update(body).digest("hex").slice(0, 16)}\"`, mtime: info.mtimeMs };
    publicFileCache.set(pathname, entry);
  }

  res.setHeader("Content-Type", file.type);
  res.setHeader("Cache-Control", file.cacheControl);
  res.setHeader("ETag", entry.etag);
  if (ifNoneMatch === entry.etag) {
    res.writeHead(304);
    res.end();
    return true;
  }
  res.setHeader("Content-Length", entry.body.length);
  res.writeHead(200);
  res.end(method === "HEAD" ? undefined : entry.body);
  return true;
}
