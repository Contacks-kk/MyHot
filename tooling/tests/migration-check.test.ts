// A pull request cannot change deployed history or hide an unsafe migration by naming it as history.
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { after, test } from "node:test";
import { checkMigrations } from "../scripts/check-migrations.ts";

const root = mkdtempSync(path.join(tmpdir(), "migration-git-"));
after(() => rmSync(root, { recursive: true, force: true }));
const write = (file: string, text: string) => {
  const target = path.join(root, file);
  mkdirSync(path.dirname(target), { recursive: true });
  writeFileSync(target, text);
};
const git = (...args: string[]) => execFileSync("git", args, { cwd: root, encoding: "utf8" });

/** TestTestDatabaseURLRejectsRemoteAndSharedDatabases protects the automated test boundary. */
test("automated database tests only accept local disposable databases", async () => {
	const previous = process.env.DATABASE_URL;
	process.env.DATABASE_URL = "postgres://tester:unused@127.0.0.1:5432/aihot_ci";
	try {
		const { validateTestDatabaseURL } = await import("./setup.ts");
		assert.equal(validateTestDatabaseURL("postgres://tester:unused@localhost:5432/aihot_ci"), "aihot_ci");
		assert.equal(validateTestDatabaseURL("postgres://tester:unused@[::1]:5432/aihot_test"), "aihot_test");
		assert.throws(() => validateTestDatabaseURL("postgres://tester:unused@db.example.invalid/aihot_test"), /only connect to PostgreSQL on this machine/);
		assert.throws(() => validateTestDatabaseURL("postgres://tester:unused@127.0.0.1/aihot_v2_test"), /shared aihot_v2_test/);
		assert.throws(() => validateTestDatabaseURL("postgres://tester:unused@127.0.0.1/aihot"), /disposable database/);
	} finally {
		if (previous === undefined) {
			delete process.env.DATABASE_URL;
		} else {
			process.env.DATABASE_URL = previous;
		}
	}
});

git("init", "-q");
git("config", "user.email", "test@example.invalid");
git("config", "user.name", "Test");
write("database/migrations/0001_history.sql", "UPDATE old_table SET done = true;\n");
git("add", ".");
git("commit", "-qm", "history");

test("unchanged history is accepted, but edits and deletions fail", () => {
  assert.deepEqual(checkMigrations(root, "HEAD"), []);
  write("database/migrations/0001_history.sql", "UPDATE old_table SET done = true;\r\n");
  assert.deepEqual(checkMigrations(root, "HEAD"), []);
  write("database/migrations/0001_history.sql", "SELECT 1;");
  assert.match(checkMigrations(root, "HEAD").join("\n"), /immutable/);
  rmSync(path.join(root, "database/migrations/0001_history.sql"));
  assert.match(checkMigrations(root, "HEAD").join("\n"), /immutable/);
  git("restore", ".");
});

test("new module migrations are checked even when untracked or named below the cutoff", () => {
  const file = "backend-go/modules/example/migrations/0002_hidden.sql";
  write(file, "CREATE TABLE example (id int);");
  assert.match(checkMigrations(root, "HEAD").join("\n"), /0055/);
  rmSync(path.join(root, file));
  write("backend-go/modules/example/migrations/0055_unsafe.sql", "UPDATE old_table SET done = false;");
  assert.match(checkMigrations(root, "HEAD").join("\n"), /0055_unsafe/);
  rmSync(path.join(root, "backend-go/modules/example/migrations/0055_unsafe.sql"));
  write("backend-go/modules/example/migrations/0055_safe.sql", "CREATE INDEX CONCURRENTLY IF NOT EXISTS example_idx ON example (id);");
  assert.deepEqual(checkMigrations(root, "HEAD"), []);
});

test("relocated module migrations preserve their published contents", () => {
  const migrationRoot = mkdtempSync(path.join(tmpdir(), "module-history-"));
  try {
    const oldFile = path.join(migrationRoot, "shared/modules/example/migrations/0055_example.sql");
    const newFile = path.join(migrationRoot, "backend-go/modules/example/migrations/0055_example.sql");
    const history = "CREATE INDEX CONCURRENTLY IF NOT EXISTS example_idx ON example (id);\n";
    const gitInFixture = (...args: string[]) => execFileSync("git", args, { cwd: migrationRoot, encoding: "utf8" });
    gitInFixture("init", "-q");
    gitInFixture("config", "user.email", "test@example.invalid");
    gitInFixture("config", "user.name", "Test");
    mkdirSync(path.dirname(oldFile), { recursive: true });
    writeFileSync(oldFile, history);
    gitInFixture("add", ".");
    gitInFixture("commit", "-qm", "history");
    assert.match(checkMigrations(migrationRoot, "HEAD").join("\n"), /move module migrations/);
    mkdirSync(path.dirname(newFile), { recursive: true });
    writeFileSync(newFile, history);
    rmSync(oldFile);
    assert.deepEqual(checkMigrations(migrationRoot, "HEAD"), []);
    writeFileSync(newFile, "SELECT 1;\n");
    assert.match(checkMigrations(migrationRoot, "HEAD").join("\n"), /immutable/);
  } finally {
    rmSync(migrationRoot, { recursive: true, force: true });
  }
});
