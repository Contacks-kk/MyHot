// Run before the reset-only token migrations; old API processes must be stopped.
import postgres from "postgres";
import { parseArgs } from "node:util";

const { values } = parseArgs({ options: { apply: { type: "boolean", default: false } } });
const rawURL = process.env.DATABASE_URL;
if (!rawURL) throw new Error("Set DATABASE_URL explicitly.");
const sql = postgres(rawURL, { max: 1, connect_timeout: 10, onnotice: () => {} });
try {
  const [row] = await sql<{ count: number }[]>`SELECT count(*)::int AS count FROM user_email_tokens WHERE purpose='verify_email'`;
  console.log(`${row!.count} obsolete registration token(s).`);
  if (!values.apply) {
    console.log("Dry run only; add --apply to delete registration tokens. Password reset tokens remain.");
  } else {
    let total = 0;
    for (;;) {
      const count = await sql.begin(async tx => {
        await tx`SET LOCAL lock_timeout='2s'`;
        await tx`SET LOCAL statement_timeout='10s'`;
        const removed = await tx`DELETE FROM user_email_tokens WHERE id IN (
          SELECT id FROM user_email_tokens WHERE purpose='verify_email' ORDER BY id LIMIT 500 FOR UPDATE
        )`;
        return removed.count;
      });
      total += count;
      if (count === 0) break;
    }
    console.log(`Deleted ${total} obsolete registration token(s).`);
  }
} finally {
  await sql.end({ timeout: 5 });
}
