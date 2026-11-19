import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import path from "node:path";
import { test } from "node:test";

const root = path.resolve(import.meta.dirname, "../..");
const compose = readFileSync(path.join(root, "deploy/compose.yaml"), "utf8");
const webDockerfile = readFileSync(path.join(root, "deploy/Dockerfile.web"), "utf8");
const setupDockerfile = readFileSync(path.join(root, "deploy/Dockerfile.setup"), "utf8");
const deploymentScript = readFileSync(path.join(root, "deploy/deploy.sh"), "utf8");
const serverOverlay = readFileSync(path.join(root, "deploy/server-overlay.yaml"), "utf8");
const goDockerfile = readFileSync(path.join(root, "backend-go/Dockerfile"), "utf8");
const dockerignore = readFileSync(path.join(root, ".dockerignore"), "utf8");
const envExample = readFileSync(path.join(root, ".env.example"), "utf8");
const localLaunch = readFileSync(path.join(root, ".claude/launch.json"), "utf8");
const notice = readFileSync(path.join(root, "NOTICE"), "utf8");
const rootPackage = readFileSync(path.join(root, "package.json"), "utf8");
const webModuleRegistry = readFileSync(path.join(root, "frontend/packages/contracts/src/module-registry.ts"), "utf8");
const siteModuleRegistry = readFileSync(path.join(root, "frontend/app/site-modules.ts"), "utf8");
const frontendSiteConfig = readFileSync(path.join(root, "frontend/packages/site/site.ts"), "utf8");
const siteServerModules = readFileSync(path.join(root, "frontend/packages/site/modules/server.ts"), "utf8");

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
  assert.equal(existsSync(path.join(root, "legacy")), false, "legacy implementations and tests must be retired from the workspace");
  assert.equal(existsSync(path.join(root, "shared")), false, "shared configuration must be split between frontend and backend-go");
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
  assert.match(dockerignore, /^legacy\/$/m);
  assert.match(dockerignore, /^shared\/$/m);
  assert.doesNotMatch(webDockerfile, /legacy\/ts-compat/);
  assert.doesNotMatch(setupDockerfile, /legacy\/ts-compat/);
  assert.doesNotMatch(deploymentScript, /legacy\/ts-compat/);

  const webRuntime = webDockerfile.split(/^FROM base\s*$/m).at(-1);
  assert.ok(webRuntime, "missing Web runtime stage");
  assert.match(webRuntime, /\/app\/frontend/);
  assert.match(webRuntime, /\/app\/frontend \/app\/frontend/);
  assert.doesNotMatch(webRuntime, /\/app\/shared/);
  assert.doesNotMatch(webRuntime, /\/app\/shared\/contracts/);
  assert.doesNotMatch(webRuntime, /\/app\/legacy\/ts-compat/);
  assert.match(webRuntime, /CMD \["node", "frontend\/server\.ts"\]/);

  const setupRuntime = setupDockerfile.split(/^FROM base\s*$/m).at(-1);
  assert.ok(setupRuntime, "missing setup runtime stage");
  assert.match(setupRuntime, /\/app\/tooling/);
  assert.match(setupRuntime, /\/app\/database/);
  assert.match(setupRuntime, /\/app\/frontend\/packages\/contracts/);
  assert.match(setupRuntime, /\/app\/backend-go\/modules/);
  assert.doesNotMatch(setupRuntime, /\/app\/shared/);
  assert.doesNotMatch(setupRuntime, /\/app\/legacy\/ts-compat/);
  assert.match(setupRuntime, /CMD \["node", "tooling\/scripts\/migrate\.ts"\]/);
});

test("frontend modules register in Web while backend extensions stay in Go", () => {
  assert.match(webModuleRegistry, /export const MODULES/);
  assert.match(siteModuleRegistry, /export function webModules/);
  assert.doesNotMatch(siteServerModules, /@aihot\/backend/);
  assert.doesNotMatch(rootPackage, /legacy\/ts-compat/);
  assert.doesNotMatch(rootPackage, /shared\/(?:site|industry|modules)/);
  assert.match(goDockerfile, /COPY backend-go\/ \.\//);
});

test("frontend site configuration does not read Go runtime configuration", () => {
  assert.doesNotMatch(frontendSiteConfig, /from ["'][^"']*backend-go\/config/);
  assert.doesNotMatch(webDockerfile, /backend-go\/config\/publication\.json/);
});

test("tracked environment and launch templates use the split runtime paths", () => {
  assert.match(envExample, /INDUSTRY_ENTITIES_FILE=backend-go\/config\/entities\.json/);
  assert.match(envExample, /MODEL_CATALOG_FILE=backend-go\/config\/model-catalog\.json/);
  assert.doesNotMatch(envExample, /shared\/(?:site|industry)\//);
  assert.match(localLaunch, /dev:local/);
  assert.doesNotMatch(localLaunch, /legacy\/ts-compat/);
  assert.match(notice, /backend-go\/config\/sources\.example\.json/);
  assert.doesNotMatch(notice, /shared\/industry/);
});

test("one-click deployment pins one branch commit and preserves migration recovery order", () => {
  const script = deploymentScript.replace(/\r\n/g, "\n");
  assert.match(script, /DEPLOY_REPOSITORY=.*DEPLOY_BRANCH=/s);
  assert.match(script, /run_git ls-remote --exit-code --heads \"\$DEPLOY_REPOSITORY\" \"refs\/heads\/\$DEPLOY_BRANCH\"/);
  assert.ok(script.includes('[[ "$REVISION" =~ ^[0-9a-f]{40}$ && "$ref" =='));
  assert.match(script, /codeload\.github\.com\/\$owner\/\$repository\/tar\.gz\/\$REVISION/);
  assert.match(script, /docker compose -p aihot --project-directory \"\$RELEASE\" --env-file \"\$ENV_FILE\"/);
  assert.match(script, /OVERLAY=\"\$ROOT\/compose\.server\.yaml\"/);
  assert.match(script, /retired_path_settings/);
  assert.match(script, /Remove the retired shared\/ path from server \.env setting/);
  assert.match(serverOverlay, /5432:5432/);
  assert.match(script, /ln -s \"\$ENV_FILE\" \"\$RELEASE\/\.env\"/);
  assert.match(script, /pg_dump -U aihot -Fc aihot_v2_test/);
  assert.match(script, /trap failed ERR/);

  const orderedSteps = [
    "\nvalidate_config\n",
    "\ncompose build setup go-api web\n",
    "\ncompose stop go-worker\n",
    "\nbackup_database\n",
    "\ncompose stop web go-api\n",
    "\ncompose run --rm --no-deps setup\n",
    "\ncompose up -d --no-deps go-api\n",
    "\ncheck_health\n",
  ].map((step) => script.indexOf(step));
  assert.ok(orderedSteps.every((position) => position >= 0), `missing deployment step: ${orderedSteps}`);
  assert.deepEqual(orderedSteps, [...orderedSteps].sort((left, right) => left - right), "deployment must build, back up, migrate, start, then check health");
});

test("Go runtime contains the complete prompt pack", () => {
  assert.match(goDockerfile, /COPY backend-go\/config\/prompts\/ \/app\/backend-go\/config\/prompts\//);
  assert.match(goDockerfile, /COPY --from=build \/app\/backend-go\/config\/prompts\/ \/app\/backend-go\/config\/prompts\//);
});
