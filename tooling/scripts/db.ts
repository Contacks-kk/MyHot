import postgres from "postgres";

export const sql = postgres(process.env.DATABASE_URL || "postgres://127.0.0.1:5432/aihot", {
  max: 1,
  connect_timeout: 10,
  onnotice: () => {},
  connection: { jit: "off" },
});

export async function closeDb(): Promise<void> {
  await sql.end({ timeout: 5 });
}
