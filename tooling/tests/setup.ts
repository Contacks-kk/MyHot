const database = new URL(process.env.DATABASE_URL ?? "postgres://unset/unset").pathname.slice(1);
if (!/_(test|ci)$/.test(database)) {
  throw new Error(`Migration tests require a disposable database named *_test or *_ci (got "${database}")`);
}
