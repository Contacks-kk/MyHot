import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
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

test("tracked runtime and development commands use the split configuration paths", () => {
  assert.match(goDockerfile, /COPY backend-go\/config\/model-catalog\.json/);
  assert.match(goDockerfile, /COPY backend-go\/config\/entities\.json/);
  assert.match(rootPackage, /"dev:local":\s*"node --env-file=\.env\.development tooling\/scripts\/dev-local\.mjs"/);
  assert.doesNotMatch(rootPackage, /shared\/(?:site|industry)\//);
});

test("one-click deployment pins one branch commit and preserves migration recovery order", () => {
  const script = deploymentScript.replace(/\r\n/g, "\n");
  assert.match(script, /DEPLOY_REPOSITORY=.*DEPLOY_BRANCH=/s);
  assert.match(script, /run_git ls-remote --exit-code --heads \"\$DEPLOY_REPOSITORY\" \"refs\/heads\/\$DEPLOY_BRANCH\"/);
  assert.ok(script.includes('[[ "$REVISION" =~ ^[0-9a-f]{40}$ && "$ref" =='));
  assert.match(script, /codeload\.github\.com\/\$GITHUB_REPOSITORY\/tar\.gz\/\$REVISION/);
  assert.match(script, /head_sha=\$REVISION&event=push/);
  assert.match(script, /branches\/\$GITHUB_BRANCH/);
  assert.match(script, /application_images verify/);
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

test("push checks include every branch without removing PR or manual checks", () => {
  const workflow = readFileSync(path.join(root, ".github/workflows/check.yml"), "utf8");
  assert.match(workflow, /push:\s*\n\s+branches: \['\*\*'\]/);
  assert.match(workflow, /pull_request:/);
  assert.match(workflow, /workflow_dispatch:/);
  assert.doesNotMatch(workflow, /node tooling\/scripts\/init-env\.ts/);
  assert.match(workflow, /mcp-check\.ts[^\n]+--cookie-file/);
});

test("MCP CI authentication rejects invalid cookie input without disclosing it", () => {
  const directory = mkdtempSync(path.join(tmpdir(), "myhot-mcp-cookie-"));
  try {
    const cookiePath = path.join(directory, "cookie");
    const privateValue = "fixture-cookie-must-not-be-printed";
    writeFileSync(cookiePath, `myhot_session=${privateValue}\ninjected-header`);
    for (const args of [["--cookie-file"], ["--cookie-file", cookiePath]]) {
      const result = spawnSync(process.execPath, [path.join(root, "tooling/scripts/mcp-check.ts"), ...args], { encoding: "utf8", timeout: 10_000 });
      assert.notEqual(result.status, 0);
      assert.match(result.stderr, /requires a path|Cookie file must contain one line/);
      assert.ok(!result.stderr.includes(privateValue));
    }
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
});

// Run the real entrypoint against disposable commands; no Docker daemon or external service is used.
const deploymentHarness = String.raw`
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile

script, options = sys.argv[1], json.loads(sys.argv[2])
stub = r'''#!/usr/bin/env python3
import hashlib
import json
import os
from pathlib import Path
import shutil
import sys
from urllib.parse import parse_qs, unquote, urlsplit

root = Path(os.environ['DEPLOY_FIXTURE'])
options = json.loads((root / 'options.json').read_text())
state = json.loads((root / 'state.json').read_text())
name, args = Path(sys.argv[0]).name, sys.argv[1:]
with (root / 'calls.jsonl').open('a') as output:
    output.write(json.dumps([name] + args) + '\n')
revision = 'a' * 40
database = 'postgres://fixture:fixture@db:5432/aihot_v2_test'

# Persist command state between isolated stub invocations.
def save():
    (root / 'state.json').write_text(json.dumps(state))

# Return a stable ID for each unique deployment image tag.
def image_id(image):
    return 'sha256:' + hashlib.sha256(image.encode()).hexdigest()

if name == 'sleep':
    sys.exit(0)
if name == 'git':
    raise SystemExit('Git must not be called for a GitHub deployment')
if name == 'curl':
    url = args[-1]
    status, body = 200, ''
    headers = ''
    if '/branches/' in url:
        state['branch_queries'] = state.get('branch_queries', 0) + 1
        save()
        if options.get('network'):
            sys.exit(28)
        status = options.get('http', 200)
        if options.get('transient') and state['branch_queries'] == 1:
            status = options['transient']
        if options.get('retry_after'):
            status, headers = 429, 'Retry-After: 120\r\n'
        if options.get('http') == 403:
            headers = 'X-RateLimit-Remaining: 0\r\nX-RateLimit-Reset: 12345\r\n'
        branch = unquote(url.rsplit('/', 1)[1])
        body = {'name': options.get('branch_name', branch), 'commit': {'sha': options.get('sha', revision if state['branch_queries'] == 1 else 'b' * 40)}}
        if options.get('transient'):
            body['commit']['sha'] = revision
        if options.get('invalid_json'):
            body = 'not-json'
    elif '/actions/workflows/check.yml/runs?' in url:
        query = parse_qs(urlsplit(url).query)
        if query != {'branch': ['fix/ui'], 'head_sha': [revision], 'event': ['push'], 'per_page': ['1']}:
            raise SystemExit('Wrong CI query: ' + url)
        run = {
            'id': 42,
            'head_sha': options.get('ci_sha', revision),
            'head_branch': options.get('ci_branch', 'fix/ui'),
            'event': options.get('ci_event', 'push'),
            'path': '.github/workflows/check.yml',
            'head_repository': {'full_name': options.get('ci_repository', 'Contacks-kk/MyHot')},
            'status': options.get('ci_status', 'completed'),
            'conclusion': options.get('ci_conclusion', 'success'),
            'html_url': 'https://github.com/Contacks-kk/MyHot/actions/runs/42',
        }
        body = {'workflow_runs': [] if options.get('no_ci') else [run]}
    elif '/actions/runs/42/jobs?' in url:
        jobs = [{'name': job, 'status': 'completed', 'conclusion': options.get('job_conclusion', 'success')} for job in ('check', 'docker')]
        if options.get('missing_job'):
            jobs.pop()
        body = {'jobs': jobs, 'total_count': len(jobs)}
    elif url.startswith('https://codeload.github.com/'):
        if not url.endswith('/' + revision):
            raise SystemExit('Downloaded a different commit')
        shutil.copyfile(root / 'archive.tar.gz', args[args.index('--output') + 1])
        body = None
    else:
        body = {'ok': True}
    if '--dump-header' in args:
        Path(args[args.index('--dump-header') + 1]).write_text('HTTP/1.1 ' + str(status) + '\r\n' + headers)
    if body is not None:
        content = body if isinstance(body, str) else json.dumps(body)
        if '--output' in args:
            Path(args[args.index('--output') + 1]).write_text(content)
        else:
            print(content)
    if '--write-out' in args:
        print(status)
    sys.exit(0)
if name == 'docker':
    if args[0] == 'compose':
        command = next(arg for arg in args[1:] if arg in ('config', 'build', 'stop', 'run', 'up'))
        if options.get('fail_stage') == command:
            sys.exit(1)
        if command == 'config':
            services = {}
            for service in ('setup', 'go-api', 'go-worker', 'web'):
                image = 'myhot-' + ('go-backend' if service.startswith('go-') else service) + ':deploy-' + os.environ['AIHOT_DEPLOY_REV']
                services[service] = {'image': image, 'environment': {'DATABASE_URL': database}}
            services['web']['environment'].update({'SITE_URL': 'https://fixture.invalid', 'TRUST_PROXY': 'true'})
            if options.get('bad_config'):
                services['web']['environment']['TRUST_PROXY'] = 'false'
            services['caddy'] = {'environment': {'SITE_DOMAIN': 'fixture.invalid'}}
            services['db'] = {'ports': [{'published': '5432', 'target': 5432}]}
            print(json.dumps({'services': services, 'volumes': {'db': {'name': 'aihot_db'}, 'data': {'name': 'aihot_data'}}}))
        if command == 'up':
            state['started'] = True
            save()
    elif args[:2] == ['image', 'inspect']:
        print(image_id(args[-1]))
    elif args[0] == 'inspect':
        if '-f' not in args:
            print(json.dumps([{'Config': {'Env': ['DATABASE_URL=' + database, 'SITE_DOMAIN=fixture.invalid']}}]))
        else:
            template = args[args.index('-f') + 1]
            if template == '{{.State.Health.Status}}':
                print('healthy')
            elif template == '{{.State.Running}}':
                print('true')
            elif template == '{{.Image}}':
                service = args[-1][len('aihot-'):-2]
                image = 'myhot-' + ('go-backend' if service.startswith('go-') else service) + ':deploy-' + os.environ['AIHOT_DEPLOY_REV']
                print('old-image' if options.get('wrong_image') else image_id(image))
            elif 'range .Config.Env' in template:
                print('SITE_DOMAIN=fixture.invalid')
            else:
                print('old-application-images')
    elif args[0] == 'exec':
        if 'pg_dump' in args:
            if options.get('backup_failure'):
                sys.exit(1)
            print('fixture-backup')
        elif 'pg_restore' in args:
            assert sys.stdin.read()
    else:
        raise SystemExit('Unexpected Docker call: ' + repr(args))
    sys.exit(0)
raise SystemExit('Unexpected command: ' + name)
'''
with tempfile.TemporaryDirectory(prefix='myhot-deploy-fixture-') as temporary:
    root = Path(temporary)
    (root / 'bin').mkdir()
    (root / 'deploy.sh').write_text(script)
    (root / 'deploy.conf').write_text('DEPLOY_REPOSITORY=https://github.com/Contacks-kk/MyHot.git\nDEPLOY_BRANCH=fix/ui\n' if not options.get('bad_config_file') else 'SECRET_SETTING=never-print-this-secret\n')
    (root / '.env').write_text('DATABASE_URL=fixture-only\n')
    (root / 'compose.server.yaml').write_text('fixture-only\n')
    (root / 'options.json').write_text(json.dumps(options))
    (root / 'state.json').write_text('{}')
    (root / 'calls.jsonl').touch()
    for name in ('curl', 'docker', 'git', 'sleep'):
        command = root / 'bin' / name
        command.write_text(stub)
        command.chmod(0o755)
    files = '''.dockerignore package.json package-lock.json deploy/compose.yaml
deploy/deploy.sh deploy/Dockerfile.setup deploy/Dockerfile.web backend-go/Dockerfile backend-go/go.mod backend-go/go.sum
backend-go/config/prompts/selection-score.md backend-go/config/prompts/story-digest.md
backend-go/config/prompts/report-period.md backend-go/config/prompts/translate-body.md
frontend/brand/Logo.tsx frontend/brand/logo.svg
backend-go/config/model-catalog.json backend-go/config/publication.json backend-go/config/changelog.json backend-go/config/public/openapi-v1.json
backend-go/config/topics.json backend-go/config/selection.json backend-go/config/reporting.json backend-go/config/entities.json backend-go/config/vocabulary.json backend-go/config/categories.json
frontend/package.json frontend/packages/og-renderer/package.json frontend/packages/site/package.json frontend/packages/contracts/package.json tooling/package.json'''.split()
    with tarfile.open(str(root / 'archive.tar.gz'), 'w:gz') as archive:
        for name in files:
            if options.get('missing_file') == name:
                continue
            content = script if name == 'deploy/deploy.sh' and not options.get('old_entry') else 'fixture'
            data = content.encode()
            entry = tarfile.TarInfo('MyHot-' + 'a' * 40 + '/' + name)
            entry.size = len(data)
            archive.addfile(entry, io.BytesIO(data))
    if options.get('corrupt_archive'):
        (root / 'archive.tar.gz').write_bytes(b'not-a-gzip-archive')
    environment = dict(os.environ, PATH=str(root / 'bin') + os.pathsep + os.environ['PATH'], DEPLOY_FIXTURE=str(root))
    command = ['bash', str(root / 'deploy.sh')] + ([] if options.get('publish') else ['--check'])
    result = subprocess.run(command, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, universal_newlines=True, env=environment, timeout=40)
    calls = [json.loads(line) for line in (root / 'calls.jsonl').read_text().splitlines()]
    logs = list((root / 'logs').glob('*'))
    print(json.dumps({'status': result.returncode, 'output': result.stdout, 'calls': calls, 'logged': bool(logs), 'state': json.loads((root / 'state.json').read_text())}))
`;

/** Execute the deployment in a disposable POSIX fixture with all external commands stubbed. */
function runDeploymentFixture(options: Record<string, unknown>) {
  const result = spawnSync("python3", ["-c", deploymentHarness, deploymentScript, JSON.stringify(options)], {
    encoding: "utf8",
    timeout: 60_000,
    maxBuffer: 2 * 1024 * 1024,
  });
  assert.equal(result.status, 0, result.stderr || result.stdout);
  return JSON.parse(result.stdout) as {
    status: number;
    output: string;
    calls: string[][];
    logged: boolean;
    state: { branch_queries?: number };
  };
}

test("deployment preflight locks a slash branch, gates CI and never changes live services", { skip: process.platform === "win32" }, () => {
  const result = runDeploymentFixture({ old_entry: true });
  assert.equal(result.status, 0, result.output);
  assert.match(result.output, /Preflight passed/);
  assert.match(result.output, /entry differs/);
  assert.equal(result.state.branch_queries, 1);
  assert.ok(result.calls.some((call) => call.at(-1)?.includes("/branches/fix%2Fui")));
  assert.ok(result.calls.some((call) => call.at(-1)?.endsWith(`/tar.gz/${"a".repeat(40)}`)));
  assert.ok(!result.calls.some((call) => call[0] === "git"));
  assert.ok(!result.calls.some((call) => call[0] === "docker" && call.some((arg) => ["build", "stop", "run", "up", "exec"].includes(arg))));
});

test("failed source or CI checks cannot reach build, backup or migration", { skip: process.platform === "win32" }, async (t) => {
  const scenarios = [
    { bad_config_file: true }, { branch_name: "ui/fix" }, { sha: "bad-sha" }, { invalid_json: true },
    { http: 404 }, { http: 403 }, { network: true }, { retry_after: true },
    { no_ci: true }, { ci_status: "in_progress", ci_conclusion: null },
    { ci_conclusion: "failure" }, { ci_conclusion: "cancelled" },
    { ci_sha: "b".repeat(40) }, { ci_branch: "main" }, { ci_event: "pull_request" },
    { ci_repository: "another/repository" }, { job_conclusion: "skipped" }, { missing_job: true },
    { corrupt_archive: true }, { missing_file: ".dockerignore" }, { bad_config: true },
  ];
  for (const scenario of scenarios) {
    await t.test(JSON.stringify(scenario), () => {
      const result = runDeploymentFixture(scenario);
      assert.notEqual(result.status, 0, result.output);
      assert.ok(result.logged);
      assert.match(result.output, /Deployment failed at/);
      assert.doesNotMatch(result.output, /never-print-this-secret/);
      assert.ok(!result.calls.some((call) => call[0] === "docker" && call.some((arg) => ["build", "stop", "run", "up", "exec"].includes(arg))), result.output);
      if (scenario.network) {
        assert.equal(result.state.branch_queries, 3);
      }
      if (scenario.http) {
        assert.equal(result.state.branch_queries, 1);
      }
    });
  }
});

test("transient HTTP failures retry without changing the selected commit", { skip: process.platform === "win32" }, () => {
  for (const transient of [429, 502]) {
    const result = runDeploymentFixture({ transient });
    assert.equal(result.status, 0, result.output);
    assert.equal(result.state.branch_queries, 2);
    assert.match(result.output, /attempt 1\/3 failed/);
  }
});

test("publication builds before stopping services and verifies running image IDs", { skip: process.platform === "win32" }, () => {
  const result = runDeploymentFixture({ publish: true });
  assert.equal(result.status, 0, result.output);
  assert.match(result.output, /Deployment completed/);
  assert.equal((result.output.match(/Verified image:/g) ?? []).length, 3);
  assert.equal(result.state.branch_queries, 1);
  const calls = result.calls.map((call) => call.join(" "));
  const ordered = ["build setup go-api web", "stop go-worker", "pg_dump", "pg_restore", "stop web go-api", "run --rm --no-deps setup", "up -d --no-deps go-api", "up -d --no-deps web go-worker caddy"]
    .map((step) => calls.findIndex((call) => call.includes(step)));
  assert.ok(ordered.every((position) => position >= 0), calls.join("\n"));
  assert.deepEqual(ordered, [...ordered].sort((left, right) => left - right));
});

test("build and backup failures stop safely, and an old running image cannot pass health", { skip: process.platform === "win32" }, () => {
  for (const scenario of [{ fail_stage: "build" }, { backup_failure: true }, { wrong_image: true }]) {
    const result = runDeploymentFixture({ ...scenario, publish: true });
    assert.notEqual(result.status, 0, result.output);
    assert.doesNotMatch(result.output, /Deployment completed/);
    if (scenario.fail_stage) {
      assert.ok(!result.calls.some((call) => call.includes("stop")));
    }
    if (scenario.backup_failure) {
      assert.ok(!result.calls.some((call) => call.includes("run")));
      assert.match(result.output, /old worker may be stopped/);
    }
    if (scenario.wrong_image) {
      assert.match(result.output, /Running image differs/);
    }
  }
});
