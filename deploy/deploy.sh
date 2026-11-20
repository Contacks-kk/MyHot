#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

ROOT="$(cd -- "$(dirname -- "$0")" && pwd -P)"
CONFIG="$ROOT/deploy.conf"
ENV_FILE="$ROOT/.env"
OVERLAY="$ROOT/compose.server.yaml"
RELEASES="$ROOT/releases"
BACKUPS="$ROOT/backups"
LOGS="$ROOT/logs"
DEPLOY_REPOSITORY=""
DEPLOY_BRANCH=""
STAGE="config"
STAGE_STARTED=$SECONDS
DEPLOY_STARTED=$SECONDS
CHECK_ONLY=false

# Print the failed deployment stage and recovery guidance.
failed() {
  local status=$?
  trap - ERR
  if (( BASH_SUBSHELL > 0 )); then
    exit "$status"
  fi
  printf '\nDeployment failed at %s (exit %s).\n' "$STAGE" "$status" >&2
  printf 'Stage elapsed: %ss; total elapsed: %ss.\n' "$((SECONDS - STAGE_STARTED))" "$((SECONDS - DEPLOY_STARTED))" >&2
  printf 'Previous images, releases and backups have been retained.\n' >&2
  if [[ "$STAGE" == backup ]]; then
    printf 'The old worker may be stopped; restart it with its previous Compose configuration.\n' >&2
  fi
  if [[ "$STAGE" == migration || "$STAGE" == startup || "$STAGE" == health ]]; then
    printf 'Old services may be stopped. Database migrations are not rolled back automatically.\n' >&2
    printf 'Inspect the backup in %s and previous image details in %s/previous-images.txt.\n' "$BACKUPS" "${STAGING:-$RELEASES}" >&2
    printf 'Restore into a separate empty database for verification before switching back to prior images.\n' >&2
  fi
  exit "$status"
}
trap failed ERR

# Report an actionable error through the common failure handler.
abort() {
  printf '%s\n' "$*" >&2
  return 1
}

# Record the duration and beginning of each deployment stage.
stage() {
  printf 'Stage %s completed in %ss.\n' "$STAGE" "$((SECONDS - STAGE_STARTED))"
  STAGE="$1"
  STAGE_STARTED=$SECONDS
  printf 'Stage: %s\n' "$STAGE"
}

# Read the two supported settings without executing the configuration file.
read_config() {
  local key value
  while IFS='=' read -r key value || [[ -n "$key" ]]; do
    value="${value%$'\r'}"
    case "$key" in
      ''|'#'*) continue ;;
      DEPLOY_REPOSITORY)
        [[ -z "$DEPLOY_REPOSITORY" ]] || abort 'Duplicate DEPLOY_REPOSITORY setting.'
        DEPLOY_REPOSITORY="$value"
        ;;
      DEPLOY_BRANCH)
        [[ -z "$DEPLOY_BRANCH" ]] || abort 'Duplicate DEPLOY_BRANCH setting.'
        DEPLOY_BRANCH="$value"
        ;;
      *) abort 'Unsupported deploy.conf key (only DEPLOY_REPOSITORY and DEPLOY_BRANCH are supported).' ;;
    esac
  done < "$CONFIG"
  [[ "$DEPLOY_REPOSITORY" =~ ^https://[A-Za-z0-9._/-]+$ ]] || abort 'Invalid HTTPS repository URL (credentials are not supported).'
  [[ -n "$DEPLOY_BRANCH" && "$DEPLOY_BRANCH" != -* ]] || abort 'Invalid or missing DEPLOY_BRANCH.'
}

# Fetch one HTTPS resource with bounded retries and without logging response bodies.
fetch_http() {
  python3 - "$1" "$2" "$3" <<'PY'
import email.utils
import os
import subprocess
import sys
import time

url, destination, budget = sys.argv[1], sys.argv[2], int(sys.argv[3])
partial, headers = destination + '.part', destination + '.headers'
deadline = time.monotonic() + budget
retry_codes = {5, 6, 7, 18, 28, 35, 52, 55, 56, 92}
try:
    for attempt in range(1, 4):
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise SystemExit('HTTPS request exhausted its time budget: ' + url)
        attempt_budget = min(20 if budget == 60 else 600, remaining)
        if os.path.exists(headers):
            os.unlink(headers)
        command = ['curl', '--disable', '--silent', '--show-error', '--location',
                   '--proto', '=https', '--proto-redir', '=https',
                   '--connect-timeout', str(min(10, attempt_budget)),
                   '--max-time', str(attempt_budget), '--output', partial,
                   '--dump-header', headers, '--write-out', '%{http_code}', url]
        try:
            result = subprocess.run(command, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                    universal_newlines=True, timeout=min(attempt_budget + 1, remaining))
            status = result.stdout.strip()
            code = result.returncode
        except subprocess.TimeoutExpired:
            status, code = '000', 28
        if code == 0 and status == '200':
            os.replace(partial, destination)
            break
        retryable = code in retry_codes or (code == 0 and status in ('408', '429', '500', '502', '503', '504'))
        response_headers = {}
        if os.path.exists(headers):
            with open(headers, encoding='utf-8', errors='replace') as response:
                for line in response:
                    if line.startswith('HTTP/'):
                        response_headers = {}
                    elif ':' in line:
                        key, value = line.split(':', 1)
                        response_headers[key.lower()] = value.strip()
        detail = 'HTTP ' + status if code == 0 else 'curl exit ' + str(code)
        print('HTTPS attempt %s/3 failed (%s): %s' % (attempt, detail, url), file=sys.stderr)
        if status == '404':
            print('Repository, branch or workflow was not found or is not publicly readable.', file=sys.stderr)
        elif status in ('401', '403'):
            print('GitHub denied this anonymous request; check public access and rate limits.', file=sys.stderr)
        if status == '403' and response_headers.get('x-ratelimit-remaining') == '0':
            print('GitHub API rate limit exhausted; reset=' + response_headers.get('x-ratelimit-reset', 'unknown'), file=sys.stderr)
        if not retryable or attempt == 3:
            raise SystemExit('HTTPS request failed; no source or CI fallback is allowed.')
        delay = 2 ** attempt
        retry_after = response_headers.get('retry-after', '')
        if retry_after:
            try:
                delay = max(delay, int(retry_after))
            except ValueError:
                try:
                    delay = max(delay, email.utils.parsedate_to_datetime(retry_after).timestamp() - time.time())
                except (ValueError, TypeError, OverflowError):
                    pass
        if delay >= deadline - time.monotonic():
            raise SystemExit('Retry-After exceeds the remaining request budget; try again later.')
        time.sleep(delay)
finally:
    for path in (partial, headers):
        if os.path.exists(path):
            os.unlink(path)
PY
}

# Parse GitHub metadata and reject unrelated commits, branches, and incomplete CI checks.
github_metadata() {
  python3 - "$@" <<'PY'
import json
import re
import sys

kind, filename, branch = sys.argv[1:4]
try:
    with open(filename, encoding='utf-8') as source:
        data = json.load(source)
    if kind == 'branch':
        revision = data['commit']['sha']
        if data['name'] != branch or not re.fullmatch('[0-9a-f]{40}', revision):
            raise ValueError('branch name or commit SHA does not match')
        print(revision)
    elif kind == 'runs':
        revision, repository = sys.argv[4:6]
        runs = data['workflow_runs']
        if not runs:
            raise ValueError('no push CI run exists for this commit; wait for Check or push the workflow update')
        run = max(runs, key=lambda item: item['id'])
        print('CI: status=%s conclusion=%s url=%s' % (run['status'], run.get('conclusion'), run['html_url']), file=sys.stderr)
        if (run['head_sha'] != revision or run['head_branch'] != branch or
                run['event'] != 'push' or run['path'] != '.github/workflows/check.yml' or
                run['head_repository']['full_name'].lower() != repository.lower()):
            raise ValueError('CI run does not belong to the configured repository, branch, workflow and commit')
        if run['status'] != 'completed' or run.get('conclusion') != 'success':
            raise ValueError('CI must finish successfully before deployment; rerun deploy.sh when it is green')
        if not isinstance(run['id'], int) or run['id'] <= 0:
            raise ValueError('invalid CI run ID')
        print(run['id'])
    elif kind == 'jobs':
        jobs = data['jobs']
        if data['total_count'] > len(jobs):
            raise ValueError('CI jobs response is incomplete')
        for name in ('check', 'docker'):
            matching = [job for job in jobs if job['name'] == name]
            if len(matching) != 1 or matching[0]['status'] != 'completed' or matching[0]['conclusion'] != 'success':
                raise ValueError('required CI job is missing or not successful: ' + name)
        print('Required CI jobs passed: check, docker.')
except (ValueError, KeyError, TypeError, IndexError) as error:
    raise SystemExit('Invalid or unacceptable GitHub ' + kind + ' response: ' + str(error))
PY
}

# Run Git on the host or in the installed Git helper image.
run_git() {
  if command -v git >/dev/null 2>&1; then
    GIT_TERMINAL_PROMPT=0 GIT_HTTP_LOW_SPEED_LIMIT=1024 GIT_HTTP_LOW_SPEED_TIME=60 \
      timeout --kill-after=5s 120s git "$@"
  else
    local container="myhot-deploy-git-$$"
    timeout --kill-after=5s 120s docker run --rm --name "$container" \
      -e GIT_TERMINAL_PROMPT=0 -e GIT_HTTP_LOW_SPEED_LIMIT=1024 \
      -e GIT_HTTP_LOW_SPEED_TIME=60 -v "$RELEASES:$RELEASES" \
      -w "$RELEASES" myhot-deploy-git:local "$@" || {
        docker rm -f "$container" > /dev/null 2>&1 || true
        return 1
      }
  fi
}

# Resolve exactly one source commit without contacting github.com for GitHub repositories.
resolve_source() {
  local reference='' ref owner repository attempt
  if [[ "$DEPLOY_REPOSITORY" =~ ^https://github\.com/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)$ ]]; then
    owner="${BASH_REMATCH[1]}"
    repository="${BASH_REMATCH[2]%.git}"
    [[ "$owner" != '.' && "$owner" != '..' && -n "$repository" && "$repository" != '.' && "$repository" != '..' ]] || abort 'Invalid GitHub repository.'
    GITHUB_API="https://api.github.com/repos/$owner/$repository"
    GITHUB_REPOSITORY="$owner/$repository"
    GITHUB_BRANCH="$(python3 -c 'import sys; from urllib.parse import quote; print(quote(sys.argv[1], safe=""))' "$DEPLOY_BRANCH")"
    fetch_http "$GITHUB_API/branches/$GITHUB_BRANCH" "$STAGING/branch.json" 60
    REVISION="$(github_metadata branch "$STAGING/branch.json" "$DEPLOY_BRANCH")"
    return
  fi
  run_git check-ref-format --branch "$DEPLOY_BRANCH" > /dev/null
  for attempt in 1 2 3; do
    if reference="$(run_git ls-remote --exit-code --heads "$DEPLOY_REPOSITORY" "refs/heads/$DEPLOY_BRANCH")"; then
      break
    fi
    printf 'Branch lookup failed (attempt %s of 3).\n' "$attempt" >&2
    if [[ "$attempt" != 3 ]]; then
      sleep 2
    fi
  done
  [[ -n "$reference" ]] || abort 'Unable to resolve the configured Git branch.'
  read -r REVISION ref <<< "$reference"
  [[ "$REVISION" =~ ^[0-9a-f]{40}$ && "$ref" == "refs/heads/$DEPLOY_BRANCH" ]] || abort 'Invalid Git branch reference.'
}

# Require the latest push CI run for the pinned GitHub commit to have passed both jobs.
require_ci() {
  local run_id query
  if [[ -z "${GITHUB_API:-}" ]]; then
    printf 'CI gate is only supported for GitHub; keeping the existing Git-server deployment behavior.\n'
    return
  fi
  query="branch=$GITHUB_BRANCH&head_sha=$REVISION&event=push&per_page=1"
  printf 'CI checks: https://github.com/%s/actions/workflows/check.yml\n' "$GITHUB_REPOSITORY"
  fetch_http "$GITHUB_API/actions/workflows/check.yml/runs?$query" "$STAGING/ci-run.json" 60
  run_id="$(github_metadata runs "$STAGING/ci-run.json" "$DEPLOY_BRANCH" "$REVISION" "$GITHUB_REPOSITORY")"
  fetch_http "$GITHUB_API/actions/runs/$run_id/jobs?filter=latest&per_page=100" "$STAGING/ci-jobs.json" 60
  github_metadata jobs "$STAGING/ci-jobs.json" "$DEPLOY_BRANCH"
}

# Download the pinned source without substituting another branch or a previous release.
fetch_source() {
  if [[ -n "${GITHUB_API:-}" ]]; then
    fetch_http "https://codeload.github.com/$GITHUB_REPOSITORY/tar.gz/$REVISION" "$STAGING/source.tar.gz.download" 1200
  else
    run_git clone --quiet --single-branch --depth 1 --branch "$DEPLOY_BRANCH" \
      "$DEPLOY_REPOSITORY" "$STAGING/source"
    [[ "$(run_git -C "$STAGING/source" rev-parse HEAD)" == "$REVISION" ]] || abort 'Git branch moved during clone; refusing a different commit.'
  fi
}

# Check archive integrity before promoting and extracting the downloaded source.
unpack_source() {
  if [[ -n "${GITHUB_API:-}" ]]; then
    tar -tzf "$STAGING/source.tar.gz.download" > /dev/null
    mv "$STAGING/source.tar.gz.download" "$STAGING/source.tar.gz"
    mkdir "$STAGING/source"
    tar -xzf "$STAGING/source.tar.gz" --strip-components 1 -C "$STAGING/source"
  fi
}

# Run Compose against the selected release and the server-owned configuration.
compose() {
  docker compose -p aihot --project-directory "$RELEASE" --env-file "$ENV_FILE" \
    -f "$RELEASE/deploy/compose.yaml" -f "$OVERLAY" --profile https "$@"
}

# Validate the database, ports, volumes and HTTPS settings before stopping services.
validate_config() {
  python3 - "$RELEASE" "$ENV_FILE" "$OVERLAY" <<'PY'
import json
import subprocess
import sys
from urllib.parse import urlsplit

release, env_file, overlay = sys.argv[1:]
command = ['docker', 'compose', '-p', 'aihot', '--project-directory', release,
           '--env-file', env_file, '-f', release + '/deploy/compose.yaml',
           '-f', overlay, '--profile', 'https', 'config', '--format', 'json']
config = json.loads(subprocess.check_output(command))
services = config['services']
retired_path_settings = (
    'INDUSTRY_ENTITIES_FILE', 'INDUSTRY_CATEGORIES_FILE', 'MODEL_CATALOG_FILE',
    'INDUSTRY_PROMPT_DIR', 'INDUSTRY_SELECTION_FILE', 'INDUSTRY_VOCABULARY_FILE',
    'INDUSTRY_REPORTING_FILE', 'INDUSTRY_TOPICS_FILE', 'SITE_PUBLICATION_FILE',
    'SITE_PUBLIC_DIR', 'SITE_CONTACT_DIR', 'SITE_CHANGELOG_PATH',
)
for setting in retired_path_settings:
    value = str(services['go-api']['environment'].get(setting, '')).replace('\\', '/')
    if 'shared/' in value:
        raise SystemExit('Remove the retired shared/ path from server .env setting ' + setting)
running = json.loads(subprocess.check_output(['docker', 'inspect', 'aihot-go-api-1']))[0]
environment = dict(item.split('=', 1) for item in running['Config']['Env'] if '=' in item)
expected = environment.get('DATABASE_URL', '')
for name in ('setup', 'go-api', 'go-worker'):
    actual = services[name]['environment'].get('DATABASE_URL', '')
    parsed = urlsplit(actual)
    if actual != expected or parsed.hostname != 'db' or parsed.path != '/aihot_v2_test':
        raise SystemExit('Database configuration differs from the running test database')
if config['volumes']['db']['name'] != 'aihot_db' or config['volumes']['data']['name'] != 'aihot_data':
    raise SystemExit('Unexpected database or data volume')
ports = services['db'].get('ports', [])
if not any(str(port.get('published')) == '5432' and port.get('target') == 5432 for port in ports):
    raise SystemExit('PostgreSQL port mapping is missing')
domain = services['caddy']['environment'].get('SITE_DOMAIN', '')
site = urlsplit(services['web']['environment'].get('SITE_URL', ''))
if not domain or site.scheme != 'https' or site.netloc != domain:
    raise SystemExit('HTTPS site domain configuration is inconsistent')
if services['web']['environment'].get('TRUST_PROXY') != 'true':
    raise SystemExit('TRUST_PROXY must be enabled for Caddy')
if services['web']['environment'].get('SITE_URL') != 'https://' + domain:
    raise SystemExit('SITE_URL must be the canonical HTTPS origin')
caddy = json.loads(subprocess.check_output(['docker', 'inspect', 'aihot-caddy-1']))[0]
caddy_env = dict(item.split('=', 1) for item in caddy['Config']['Env'] if '=' in item)
if caddy_env.get('SITE_DOMAIN') != domain:
    raise SystemExit('SITE_DOMAIN differs from the running Caddy configuration')
print('Configuration validated: database=aihot_v2_test, site=' + domain)
PY
}

# Back up and validate the existing PostgreSQL database before migration.
backup_database() {
  local path
  path="$(mktemp "$BACKUPS/aihot_v2_test-${AIHOT_DEPLOY_REV}.XXXXXXXX")"
  docker exec aihot-db-1 pg_dump -U aihot -Fc aihot_v2_test > "$path"
  [[ -s "$path" ]]
  docker exec -i aihot-db-1 pg_restore -l < "$path" > /dev/null
  printf 'Database backup: %s\n' "$path"
}

# Wait for the API container and the HTTPS entry point to become healthy.
check_health() {
  local attempt state
  for attempt in {1..60}; do
    state="$(docker inspect -f '{{.State.Health.Status}}' aihot-go-api-1 2>/dev/null || true)"
    if [[ "$state" == healthy ]]; then
      break
    fi
    sleep 2
  done
  [[ "$state" == healthy ]]
  compose up -d --no-deps web go-worker caddy
  for attempt in {1..30}; do
    if curl -fsS --max-time 5 --resolve "$SITE_DOMAIN:443:127.0.0.1" \
      "https://$SITE_DOMAIN/api/health" > /dev/null; then
      [[ "$(docker inspect -f '{{.State.Running}}' aihot-go-worker-1)" == true ]]
      return
    fi
    sleep 2
  done
  return 1
}

# Record built image IDs or verify that all running application containers use those images.
application_images() {
  python3 - "$1" "$STAGING/expected-images.json" "$RELEASE" "$ENV_FILE" "$OVERLAY" <<'PY'
import json
import subprocess
import sys

mode, filename, release, env_file, overlay = sys.argv[1:]
if mode == 'record':
    command = ['docker', 'compose', '-p', 'aihot', '--project-directory', release,
               '--env-file', env_file, '-f', release + '/deploy/compose.yaml',
               '-f', overlay, '--profile', 'https', 'config', '--format', 'json']
    services = json.loads(subprocess.check_output(command))['services']
    images = {}
    for name in ('go-api', 'go-worker', 'web'):
        image = services[name]['image']
        image_id = subprocess.check_output(['docker', 'image', 'inspect', '-f', '{{.Id}}', image]).decode().strip()
        images[name] = {'image': image, 'id': image_id}
    with open(filename, 'w') as output:
        json.dump(images, output)
else:
    with open(filename) as source:
        images = json.load(source)
    for name, expected in images.items():
        actual = subprocess.check_output(['docker', 'inspect', '-f', '{{.Image}}', 'aihot-' + name + '-1']).decode().strip()
        if actual != expected['id']:
            raise SystemExit('Running image differs from this deployment: ' + name)
        print('Verified image: %s %s %s' % (name, expected['image'], actual))
PY
}

mkdir -p "$LOGS"
LOG_FILE="$(mktemp "$LOGS/deploy-$(date +%Y%m%d%H%M%S).XXXXXXXX")"
exec > >(tee -a "$LOG_FILE") 2>&1
printf 'Deployment log: %s\n' "$LOG_FILE"
printf 'Configuration: %s\nScript SHA256: %s\n' "$CONFIG" "$(sha256sum "$ROOT/deploy.sh" | cut -d ' ' -f 1)"
case "${1:-}" in
  '') [[ $# == 0 ]] || abort 'Usage: ./deploy.sh [--check]' ;;
  --check) [[ $# == 1 ]] || abort 'Usage: ./deploy.sh [--check]'; CHECK_ONLY=true ;;
  *) abort 'Usage: ./deploy.sh [--check]' ;;
esac
for required in "$CONFIG" "$ENV_FILE" "$OVERLAY"; do
  [[ -f "$required" ]] || abort "Required server configuration is missing: $required"
done
for required in docker python3 curl flock tar timeout sha256sum; do
  command -v "$required" >/dev/null || abort "Required command is missing: $required"
done
read_config
printf 'Repository: %s\nBranch: %s\n' "$DEPLOY_REPOSITORY" "$DEPLOY_BRANCH"
mkdir -p "$RELEASES" "$BACKUPS"
exec 9> "$ROOT/.deploy.lock"
flock -n 9 || abort 'Another deployment is running.'

stage resolve
STAGING="$(mktemp -d "$RELEASES/pending.XXXXXXXX")"
resolve_source
printf 'Pinned commit: %s\n' "$REVISION"
stage ci
require_ci
stage download
fetch_source
stage source
unpack_source
export AIHOT_DEPLOY_REV="${REVISION}-$(date +%Y%m%d%H%M%S)-$$"
RELEASE="$STAGING/source"
[[ ! -e "$RELEASE/.env" && ! -L "$RELEASE/.env" ]] || abort 'Source archive must not contain .env.'
for required in .dockerignore package.json package-lock.json deploy/compose.yaml \
  deploy/deploy.sh deploy/Dockerfile.setup deploy/Dockerfile.web backend-go/Dockerfile backend-go/go.mod backend-go/go.sum \
  backend-go/config/prompts/selection-score.md backend-go/config/prompts/story-digest.md \
  backend-go/config/prompts/report-period.md backend-go/config/prompts/translate-body.md \
  frontend/brand/Logo.tsx frontend/brand/logo.svg \
  backend-go/config/model-catalog.json backend-go/config/publication.json backend-go/config/changelog.json backend-go/config/public/openapi-v1.json \
  backend-go/config/topics.json backend-go/config/selection.json backend-go/config/reporting.json backend-go/config/entities.json backend-go/config/vocabulary.json backend-go/config/categories.json \
  frontend/package.json frontend/packages/og-renderer/package.json frontend/packages/site/package.json frontend/packages/contracts/package.json tooling/package.json; do
  [[ -f "$RELEASE/$required" ]] || abort "Missing build file: $required"
done
if ! cmp -s "$ROOT/deploy.sh" "$RELEASE/deploy/deploy.sh"; then
  printf 'Deployment entry differs from this commit. Review and install: %s\nThe running script will not update itself.\n' "$RELEASE/deploy/deploy.sh"
fi
ln -s "$ENV_FILE" "$RELEASE/.env"
printf 'Deploying branch %s at commit %s\n' "$DEPLOY_BRANCH" "$REVISION"

stage preflight
validate_config
SITE_DOMAIN="$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' aihot-caddy-1 | sed -n 's/^SITE_DOMAIN=//p')"
[[ "$SITE_DOMAIN" =~ ^[A-Za-z0-9.-]+$ ]]
printf 'Current application images:\n'
docker inspect -f '{{.Name}} {{.Config.Image}} {{.Image}} {{index .Config.Labels "com.docker.compose.project.working_dir"}} {{index .Config.Labels "com.docker.compose.project.config_files"}}' \
  aihot-go-api-1 aihot-go-worker-1 aihot-web-1 | tee "$STAGING/previous-images.txt"
if [[ "$CHECK_ONLY" == true ]]; then
  printf 'Preflight passed for %s. No images built, services stopped or migrations executed. Total elapsed: %ss.\n' "$REVISION" "$((SECONDS - DEPLOY_STARTED))"
  exit 0
fi

stage build
compose build setup go-api web
application_images record

stage backup
compose stop go-worker
backup_database

stage migration
compose stop web go-api
compose run --rm --no-deps setup

stage startup
compose up -d --no-deps go-api

stage health
check_health
application_images verify
printf 'Stage health completed in %ss.\n' "$((SECONDS - STAGE_STARTED))"
printf 'Deployment completed: %s\nRelease: %s\nTotal elapsed: %ss.\n' "$REVISION" "$RELEASE" "$((SECONDS - DEPLOY_STARTED))"
