import assert from "node:assert/strict";
import { createHmac } from "node:crypto";
import test from "node:test";
import { parseOgPrewarmRequest } from "../app/lib/og-prewarm.server.ts";

const secret = "a-private-prewarm-secret-with-at-least-32-bytes";
const payload = Buffer.from(JSON.stringify({
  id: "article-1",
  item: { id: "article-1", title: "标题", summary: "摘要", timelineAt: "2026-10-09T08:30:00+08:00", category: "models", score: 91, selected: true, source: { name: "来源" } },
}));
const signature = createHmac("sha256", secret).update(payload).digest("hex");

test("OG prewarm accepts a valid signed worker card", () => {
  const result = parseOgPrewarmRequest(payload, signature, secret);
  assert.equal(result?.id, "article-1");
  assert.equal(result?.card.title, "标题");
  assert.deepEqual(result?.card.badge, { value: "91", label: "精选评分" });
});

test("OG prewarm rejects invalid signatures and malformed cards", () => {
  assert.equal(parseOgPrewarmRequest(payload, "0".repeat(64), secret), null);
  assert.equal(parseOgPrewarmRequest(payload, signature, "short"), null);
  const badID = Buffer.from(JSON.stringify({ id: "../secret", item: { id: "../secret", title: "x", timelineAt: "2026-10-09", source: { name: "x" } } }));
  const badSignature = createHmac("sha256", secret).update(badID).digest("hex");
  assert.equal(parseOgPrewarmRequest(badID, badSignature, secret), null);
});
