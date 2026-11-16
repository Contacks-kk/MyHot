// Content pushes: newly selected items, and the images they need prepared first.
import type { PgBoss } from "pg-boss";
import { pushSelected } from "../notify/selected.ts";
import { prepareArticleMedia, warmShareImage } from "../media/prepare.ts";
import { convertAnimated } from "../media/images.ts";
import { enqueue, QUEUES, work } from "./queue.ts";
import { config } from "../config.ts";

const MAX_RETRIES = 6;

export async function registerNotifyJobs(boss: PgBoss) {
  // Keep legacy queue rows intact for an explicit cutover import. Once the Go shared pipeline owns
  // publication effects, acknowledging these here would lose jobs that Go cannot reconstruct from
  // the publication row alone.
  if (config.goSharedPipelineEnabled) return;

  await work(boss, QUEUES.notifySelected, { localConcurrency: 1, pollingIntervalSeconds: 5 }, async ({ articleId, attempt = 0 }) => {
    // The push makes chat apps unfurl the link: have its share image ready (first attempt only).
    if (!attempt) await warmShareImage(articleId);
    const outcome = await pushSelected(articleId);
    if (outcome.status === "retry" && attempt < MAX_RETRIES) {
      await enqueue(QUEUES.notifySelected, { articleId, attempt: attempt + 1 }, { startAfter: outcome.after });
    }
    return outcome;
  });

  await work(boss, QUEUES.prepareMedia, { localConcurrency: 1, pollingIntervalSeconds: 5 }, async (data) => {
    if ("articleId" in data) {
      return prepareArticleMedia(data.articleId);
    }
    // Once Go owns URL renditions, acknowledge old pg-boss copies without letting both workers
    // write the same shared rendition cache. Article-level preparation remains TypeScript-owned.
    if (config.goMediaPrepareEnabled) return { animatedSaved: false, delegated: true };
    return { animatedSaved: await convertAnimated(data.url, data.mode) };
  });
}
