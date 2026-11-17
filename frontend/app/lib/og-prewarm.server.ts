import { createHmac, timingSafeEqual } from "node:crypto";
import type { OgCard } from "@aihot/og-renderer/og";
import { webOgContentCard } from "./og-assets.server.ts";

export interface OgPrewarmRequest {
  id: string;
  card: OgCard;
}

/** Verify the private worker request before rendering or writing into the shared OG cache. */
export function parseOgPrewarmRequest(body: Buffer, signature: string | undefined, secret: string | undefined): OgPrewarmRequest | null {
  if (!secret || Buffer.byteLength(secret) < 32 || !signature || !/^[0-9a-f]{64}$/i.test(signature)) return null;
  const expected = createHmac("sha256", secret).update(body).digest();
  const given = Buffer.from(signature, "hex");
  if (given.length !== expected.length || !timingSafeEqual(given, expected)) return null;

  let value: unknown;
  try {
    value = JSON.parse(body.toString("utf8"));
  } catch {
    return null;
  }
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const input = value as Record<string, unknown>;
  if (typeof input.id !== "string" || !/^[A-Za-z0-9_-]{1,80}$/.test(input.id)) return null;
  if (!input.item || typeof input.item !== "object" || Array.isArray(input.item)) return null;
  const item = input.item as Record<string, unknown>;
  if (item.id !== input.id || typeof item.title !== "string" || !item.title.trim() || item.title.length > 1000) return null;
  if (typeof item.timelineAt !== "string" || item.timelineAt.length > 64 || !Number.isFinite(Date.parse(item.timelineAt))) return null;
  if (item.summary !== null && item.summary !== undefined && (typeof item.summary !== "string" || item.summary.length > 5000)) return null;
  if (item.category !== null && item.category !== undefined && (typeof item.category !== "string" || item.category.length > 120)) return null;
  if (item.categoryLabel !== undefined && (typeof item.categoryLabel !== "string" || item.categoryLabel.length > 300)) return null;
  if (item.score !== null && item.score !== undefined && (typeof item.score !== "number" || !Number.isFinite(item.score))) return null;
  if (typeof item.selected !== "boolean" || !item.source || typeof item.source !== "object" || Array.isArray(item.source)) return null;
  const source = item.source as Record<string, unknown>;
  if (typeof source.name !== "string" || !source.name.trim() || source.name.length > 300) return null;
  const catalog = {
    subject: "",
    categories: typeof item.category === "string" && typeof item.categoryLabel === "string" ? [{ key: item.category, label: item.categoryLabel, publicKey: item.category, feedLabel: item.categoryLabel }] : [],
    topicGroups: [],
    topics: [],
  };
  const card: OgCard | null = webOgContentCard({ kind: "item", id: input.id, apiPath: `/api/site/items/${encodeURIComponent(input.id)}` }, item, catalog);
  if (!card) return null;

  return { id: input.id, card };
}
