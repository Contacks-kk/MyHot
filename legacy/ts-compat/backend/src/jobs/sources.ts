// Collection jobs: per-source fetch runs and body extraction before analysis.
import type { PgBoss } from "pg-boss";
import { dajialaConfigured } from "../providers/dajiala.ts";
import { collectSource } from "../sources/collect.ts";
import { checkMpAccount } from "../sources/mp.ts";
import { QUEUES, work } from "./queue.ts";
import { registerExtractionJobs } from "./content.ts";
import { config } from "../config.ts";

export async function registerSourceJobs(boss: PgBoss) {
  // When Go owns the pipeline, its startup importer transfers pending manual work to River and its
  // database-backed scanners reconstruct scheduled fetches. Leave these queues unclaimed by pg-boss.
  if (!config.goSharedPipelineEnabled) {
    await work(boss, QUEUES.fetchSource, { localConcurrency: 8, pollingIntervalSeconds: 2 }, ({ sourceId, force }) =>
      force ? collectSource(sourceId, { force }) : Promise.resolve({ sourceId, status: "retired" as const, found: 0, created: 0, revised: 0 }),
    );
    await work(boss, QUEUES.fetchXShard, { localConcurrency: 2, pollingIntervalSeconds: 2 }, ({ key }) =>
      Promise.resolve({ key, status: "retired" as const, accounts: 0, found: 0, created: 0 }),
    );
    if (dajialaConfigured()) await work(boss, QUEUES.mpCheck, { localConcurrency: 2, pollingIntervalSeconds: 2 }, ({ sourceId, reason }) =>
      reason === "manual" ? checkMpAccount(sourceId, "manual") : Promise.resolve({ sourceId, status: "retired" as const }),
    );
  }
  await registerExtractionJobs(boss);
}
