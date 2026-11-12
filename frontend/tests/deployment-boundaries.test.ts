import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import path from "node:path";
import { test } from "node:test";

const root = path.resolve(import.meta.dirname, "../..");
const compose = readFileSync(path.join(root, "deploy/compose.yaml"), "utf8");
const webDockerfile = readFileSync(path.join(root, "deploy/Dockerfile.web"), "utf8");
const setupDockerfile = readFileSync(path.join(root, "deploy/Dockerfile.setup"), "utf8");
const workflow = readFileSync(path.join(root, ".github/workflows/check.yml"), "utf8");
const rootPackage = readFileSync(path.join(root, "package.json"), "utf8");

function section(name: string): string {
  const lines = compose.split(/\r?\n/);
  const indent = name.startsWith("x-") ? "" : "  ";
  const start = lines.findIndex((line) => line.startsWith(`${indent}${name}:`));
  assert.notEqual(start, -1, `missing Compose build anchor or service ${name}`);
  const nextSection = lines.findIndex((line, index) => index > start && line.startsWith(indent) &&
    (indent === "" ? !line.startsWith(" ") : !line.startsWith("   ")) && line.slice(indent.length).match(/^[\w-]+:/));
  return lines.slice(start + 1, nextSection === -1 ? lines.length : nextSection).join("\n");
}

test("Compose runs only React Web and Go business runtimes", () => {
  for (const obsoleteEntry of [
    "legacy/ts-compat/api/src/main.ts",
    "apps/worker/package.json",
    "apps/worker/src/main.ts",
    "tooling/scripts/collect.ts",
  ]) assert.equal(existsSync(path.join(root, obsoleteEntry)), false, `${obsoleteEntry} must not remain an executable legacy backend entrypoint`);
  assert.doesNotMatch(rootPackage, /(?:^|["'])start:(?:api|worker)["']\s*:/m);

  assert.doesNotMatch(compose, /^  api:/m);
  assert.doesNotMatch(compose, /^  worker:/m);
  assert.match(section("x-web"), /dockerfile:\s*deploy\/Dockerfile\.web/);
  assert.match(section("x-go-backend"), /dockerfile:\s*backend-go\/Dockerfile\s*\n\s*image:\s*myhot-go-backend/);
  assert.match(section("web"), /GO_API_BASE_URL:\s*http:\/\/go-api:3002/);
  assert.doesNotMatch(section("web"), /(?:^|\n)\s+API_BASE_URL:|depends_on:[\s\S]*?\n\s+api:/);
  assert.match(section("go-api"), /healthcheck:[\s\S]*\/api\/health/);
  assert.match(section("web"), /go-api:\s*\n\s+condition:\s*service_healthy/);
  assert.match(section("go-api"), /<<:\s*\*go-backend/);
  assert.match(section("go-worker"), /<<:\s*\*go-backend/);
  assert.match(section("x-setup"), /dockerfile:\s*deploy\/Dockerfile\.setup/);
  assert.match(section("setup"), /tooling\/scripts\/migrate\.ts/);
  assert.doesNotMatch(section("setup"), /seed\.ts/);
});

test("Web runtime has SSR assets; setup image only has migration tooling", () => {
  const webRuntime = webDockerfile.split(/^FROM base\s*$/m).at(-1);
  assert.ok(webRuntime, "missing Web runtime stage");
  assert.match(webRuntime, /\/app\/frontend/);
  assert.match(webRuntime, /\/app\/shared\/contracts/);
  assert.match(webRuntime, /\/app\/frontend \/app\/frontend/);
  assert.doesNotMatch(webRuntime, /\/app\/legacy\/ts-compat/);
  assert.match(webRuntime, /CMD \["node", "frontend\/server\.ts"\]/);

  const setupRuntime = setupDockerfile.split(/^FROM base\s*$/m).at(-1);
  assert.ok(setupRuntime, "missing setup runtime stage");
  assert.match(setupRuntime, /\/app\/tooling/);
  assert.match(setupRuntime, /\/app\/database/);
  assert.doesNotMatch(setupRuntime, /\/app\/legacy\/ts-compat/);
  assert.match(setupRuntime, /CMD \["node", "tooling\/scripts\/migrate\.ts"\]/);
});

test("CI runs Go database integration and authenticated SSR smoke checks", () => {
  assert.match(workflow, /createdb[^\n]+aihot_test/);
  assert.match(workflow, /MYHOT_TEST_DATABASE_URL:[^\n]+aihot_test/);
  assert.match(workflow, /INSERT INTO user_sessions/);
  assert.match(workflow, /tooling\/scripts\/smoke\.ts[^\n]+--cookie-file/);
});
