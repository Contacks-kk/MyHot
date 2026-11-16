// Batch-pause archived source rows that have no active user subscriptions.
// Dry-run is the default; the database URL must be provided explicitly.
import postgres from "postgres";
import { parseArgs } from "node:util";

const { values } = parseArgs({ options: {
  apply: { type: "boolean", default: false },
  "batch-size": { type: "string", default: "500" },
} });
const rawURL = process.env.DATABASE_URL;
if (!rawURL) throw new Error("Set DATABASE_URL explicitly; no environment file is loaded by this command.");
const databaseURL = new URL(rawURL);
const batchSize = Number(values["batch-size"]);
if (!Number.isInteger(batchSize) || batchSize < 1 || batchSize > 500) throw new Error("--batch-size must be an integer from 1 to 500");

const sql = postgres(rawURL, { max: 1, connect_timeout: 10, onnotice: () => {}, connection: { jit: "off" } });
try {
  const [count] = await sql<{ count: number }[]>`
    SELECT count(*)::int AS count FROM sources s
    WHERE s.enabled AND NOT EXISTS (
      SELECT 1 FROM user_source_subscriptions sub WHERE sub.source_id=s.id AND sub.enabled
    )`;
  console.log(`${count!.count} enabled source(s) have no active subscription on ${databaseURL.hostname}/${databaseURL.pathname.slice(1)}.`);
  if (!values.apply) {
    console.log("Dry run only. Add --apply to pause these sources in bounded batches.");
  } else {
    let total = 0;
    for (;;) {
      const changed = await sql.begin(async (tx) => {
        const rows = await tx<{ id: string }[]>`
          SELECT s.id FROM sources s
          WHERE s.enabled AND NOT EXISTS (
            SELECT 1 FROM user_source_subscriptions sub WHERE sub.source_id=s.id AND sub.enabled
          )
          ORDER BY s.id LIMIT ${batchSize} FOR UPDATE`;
        if (rows.length === 0) return 0;
        const ids = rows.map((row) => row.id);
        const result = await tx`
          UPDATE sources SET enabled=false, health='paused', next_fetch_at=NULL, updated_at=now()
          WHERE id = ANY(${ids}) AND NOT EXISTS (
            SELECT 1 FROM user_source_subscriptions sub WHERE sub.source_id=sources.id AND sub.enabled
          )`;
        return result.count;
      });
      if (changed === 0) break;
      total += changed;
      console.log(`paused ${total} source(s)`);
    }
    console.log(`completed: ${total} source(s) paused; articles and fetch history were retained.`);
  }
} finally {
  await sql.end({ timeout: 5 });
}
