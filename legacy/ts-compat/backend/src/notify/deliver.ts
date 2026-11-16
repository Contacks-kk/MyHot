// Content-group deliveries (Feishu custom-bot webhooks), such as the cards of selected items.
// One row per target and dedupe key, so nothing is pushed twice; an outcome we cannot know
// ("unknown") is never retried automatically; content older than a target's enabled_at is never
// back-filled. FEISHU_CONTENT_PUSH_ENABLED is the safety valve: off, deliveries are recorded as
// skipped and nothing leaves the process.
import { config, credential } from "../config.ts";
import { sql } from "../db.ts";
import { postWebhook } from "./feishu.ts";
import { selectedContent } from "./selected-content.ts";

export interface DeliveryRequest {
  /** "selected" for the engine's cards; a module's deliveries carry its own kind. */
  subjectKind: string;
  subjectId: string;
  dedupeKey: string;
  /** When the underlying content appeared; older than a target's enabled_at means skip. */
  contentAt: Date;
  card: unknown;
  /** A follow-up may only belong to one of the content groups. */
  targetKey?: string;
  /**
   * Other subjects that stand for the same news (the other reports of the fact): a target that was
   * already sent one of them, or may have been, is not sent this one.
   */
  siblings?: string[];
}

interface Target {
  key: string;
  kind: "feishu_webhook" | "feishu_chat" | "log";
  enabled_at: Date | null;
  config_ref: string | null;
}

/** Default content targets; they start disabled and are switched on in production only. */
export async function ensureContentTargets() {
  await sql`
    INSERT INTO notify_targets (key, purpose, kind, enabled, config_ref, note) VALUES
      ('feishu-content-main', 'content', 'feishu_webhook', false, 'FEISHU_PUSH_WEBHOOK_URL', '飞书内容主群'),
      ('feishu-content-mirror', 'content', 'feishu_webhook', false, 'FEISHU_PUSH_MIRROR_WEBHOOK_URL', '飞书内容镜像群')
    ON CONFLICT (key) DO NOTHING`;
}

/** The acknowledgement and stored outcome follow the same rules for first sends and recovery. */
async function sendDelivery(id: number, url: string, card: unknown): Promise<{ status: string }> {
  let result: Awaited<ReturnType<typeof postWebhook>>;
  try {
    result = await postWebhook(url, card);
  } catch (error) {
    // A timeout or lost connection may still have delivered. Never retry it automatically.
    result = { status: "unknown", body: String(error).slice(0, 500) };
  }
  await sql`UPDATE deliveries SET status = ${result.status}, response = ${result.body},
    sent_at = ${result.status === "sent" ? new Date() : null}, updated_at = now() WHERE id = ${id}`;
  return { status: result.status };
}

export async function deliverContent(req: DeliveryRequest): Promise<Array<{ target: string; status: string }>> {
  // A selected article may acquire a different fact after correction. Its own previous delivery
  // still counts, even when the dedupe key changes. Other kinds of push keep their own keys.
  const sentSubjects = req.subjectKind === "selected" ? [req.subjectId, ...(req.siblings ?? [])] : (req.siblings ?? []);
  const targets = await sql<{ key: string }[]>`SELECT key FROM notify_targets WHERE purpose = 'content' AND enabled
    AND (${req.targetKey ?? null}::text IS NULL OR key = ${req.targetKey ?? null}) ORDER BY key`;
  const results: Array<{ target: string; status: string }> = [];
  for (const target of targets) {
    // The preceding target can take seconds to answer. Re-read before each send so a withdrawal,
    // silence or correction reaches mirrors as well as the first group.
    const current = req.subjectKind === "selected" ? await selectedContent(req.subjectId) : null;
    if (current && current.status !== "ready") break;
    const card = current?.card ?? req.card;
    const claim = await sql.begin(async (tx) => {
      // Reserve a target in a short transaction: concurrent siblings must see each other's pending
      // delivery, even before either network request starts. No lock is held while sending.
      const [t] = await tx<Target[]>`SELECT key, kind, enabled_at, config_ref FROM notify_targets
        WHERE key = ${target.key} AND purpose = 'content' AND enabled FOR UPDATE`;
      if (!t || (t.enabled_at && req.contentAt < t.enabled_at)) return null;
      if (sentSubjects.length) {
        const [told] = await tx`SELECT 1 FROM deliveries WHERE target_key = ${t.key} AND subject_kind = ${req.subjectKind}
          AND subject_id = ANY(${sentSubjects}::text[]) AND status IN ('pending', 'sent', 'unknown', 'sending') LIMIT 1`;
        if (told) return null;
      }
      const [row] = await tx<{ id: number }[]>`
        INSERT INTO deliveries (target_key, subject_kind, subject_id, dedupe_key, status, payload)
        VALUES (${t.key}, ${req.subjectKind}, ${req.subjectId}, ${req.dedupeKey}, 'pending', ${tx.json(card as never)})
        ON CONFLICT (target_key, dedupe_key) DO NOTHING RETURNING id`;
      return row ? { id: row.id, target: t } : null;
    });
    if (!claim) continue;
    const { id, target: t } = claim;
    if (!config.feishuContentPushEnabled || t.kind !== "feishu_webhook") {
      await sql`UPDATE deliveries SET status = 'skipped', response = 'content push disabled', updated_at = now() WHERE id = ${id}`;
      results.push({ target: t.key, status: "skipped" });
      continue;
    }
    const url = t.config_ref ? credential("integrations", t.config_ref) : undefined;
    if (!url) {
      await sql`UPDATE deliveries SET status = 'failed', response = 'webhook not configured', updated_at = now() WHERE id = ${id}`;
      results.push({ target: t.key, status: "failed" });
      continue;
    }
    await sql`UPDATE deliveries SET status = 'sending', attempts = attempts + 1, updated_at = now() WHERE id = ${id}`;
    results.push({ target: t.key, ...(await sendDelivery(id, url, card)) });
  }
  return results;
}
