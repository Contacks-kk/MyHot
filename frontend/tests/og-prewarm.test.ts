import assert from "node:assert/strict";
import { createHmac } from "node:crypto";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import test from "node:test";
import { parseOgPrewarmRequest } from "../app/lib/og-prewarm.server.ts";

const secret = "a-private-prewarm-secret-with-at-least-32-bytes";
const payload = Buffer.from(JSON.stringify({
  id: "article-1",
  item: { id: "article-1", title: "标题", summary: "摘要", timelineAt: "2026-10-09T08:30:00+08:00", category: "models", categoryLabel: "模型", score: 91, selected: true, source: { name: "来源" } },
}));
const signature = createHmac("sha256", secret).update(payload).digest("hex");

test("OG prewarm accepts a valid signed worker card", () => {
  const result = parseOgPrewarmRequest(payload, signature, secret);
  assert.equal(result?.id, "article-1");
  assert.equal(result?.card.title, "标题");
  assert.equal(result?.card.kicker, "模型");
  assert.deepEqual(result?.card.badge, { value: "91", label: "精选评分" });
});

test("OG prewarm rejects invalid signatures and malformed cards", () => {
  assert.equal(parseOgPrewarmRequest(payload, "0".repeat(64), secret), null);
  assert.equal(parseOgPrewarmRequest(payload, signature, "short"), null);
  const badID = Buffer.from(JSON.stringify({ id: "../secret", item: { id: "../secret", title: "x", timelineAt: "2026-10-09", source: { name: "x" } } }));
  const badSignature = createHmac("sha256", secret).update(badID).digest("hex");
  assert.equal(parseOgPrewarmRequest(badID, badSignature, secret), null);
});

/** Verifies concurrent cold share renders return stable bytes from the isolated disk cache. */
test("concurrent OG and poster renders share stable cached bytes", async () => {
  const dataDir = await mkdtemp(path.join(tmpdir(), "aihot-og-render-test-"));
  const previousDataDir = process.env.AIHOT_DATA_DIR;
  process.env.AIHOT_DATA_DIR = dataDir;
  try {
    const [{ renderOg }, { renderPoster }] = await Promise.all([
      import("../packages/og-renderer/src/og.ts"),
      import("../packages/og-renderer/src/poster.ts"),
    ]);
    const card = { kicker: "MyHOT", title: "并发渲染验证", subtitle: "同一图片只生成一次" };
    const images = await Promise.all(Array.from({ length: 6 }, () => renderOg(card)));
    for (const image of images) {
      assert.deepEqual(image.png, images[0]!.png);
      assert.equal(image.etag, images[0]!.etag);
    }
    const poster = {
      url: "https://example.test/items/article-1",
      kicker: "模型观察",
      title: "海报并发验证",
      summary: "本地渲染器复用已写入的海报缓存。",
      source: "测试信源",
      date: "2026-11-18",
      score: 91,
    };
    const posters = await Promise.all(Array.from({ length: 4 }, () => renderPoster(poster)));
    for (const image of posters) {
      assert.deepEqual(image.png, posters[0]!.png);
      assert.equal(image.etag, posters[0]!.etag);
    }
    assert.deepEqual((await renderOg(card)).png, images[0]!.png);
    assert.deepEqual((await renderPoster(poster)).png, posters[0]!.png);
  } finally {
    if (previousDataDir === undefined) {
      delete process.env.AIHOT_DATA_DIR;
    } else {
      process.env.AIHOT_DATA_DIR = previousDataDir;
    }
    await rm(dataDir, { recursive: true, force: true });
  }
});
