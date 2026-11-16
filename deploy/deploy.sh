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
STAGE="preflight"

# Print the failed deployment stage and recovery guidance.
failed() {
  local status=$?
  printf '\nDeployment failed at %s (exit %s).\n' "$STAGE" "$status" >&2
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

# Read the two supported settings without executing the configuration file.
read_config() {
  local key value
  while IFS='=' read -r key value || [[ -n "$key" ]]; do
    value="${value%$'\r'}"
    case "$key" in
      ''|'#'*) continue ;;
      DEPLOY_REPOSITORY)
        [[ -z "$DEPLOY_REPOSITORY" ]] || return 1
        DEPLOY_REPOSITORY="$value"
        ;;
      DEPLOY_BRANCH)
        [[ -z "$DEPLOY_BRANCH" ]] || return 1
        DEPLOY_BRANCH="$value"
        ;;
      *) printf 'Unsupported deploy.conf key: %s\n' "$key" >&2; return 1 ;;
    esac
  done < "$CONFIG"
  [[ "$DEPLOY_REPOSITORY" =~ ^https://[A-Za-z0-9._/-]+$ ]]
  [[ -n "$DEPLOY_BRANCH" && "$DEPLOY_BRANCH" != -* ]]
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

# Download the resolved commit from GitHub or check out the same commit from another Git server.
fetch_source() {
  local reference='' ref owner repository attempt
  for attempt in 1 2 3; do
    if reference="$(run_git ls-remote --exit-code --heads "$DEPLOY_REPOSITORY" "refs/heads/$DEPLOY_BRANCH")"; then
      break
    fi
    printf 'Branch lookup failed (attempt %s of 3).\n' "$attempt" >&2
    if [[ "$attempt" != 3 ]]; then
      sleep 2
    fi
  done
  [[ -n "$reference" ]]
  read -r REVISION ref <<< "$reference"
  [[ "$REVISION" =~ ^[0-9a-f]{40}$ && "$ref" == "refs/heads/$DEPLOY_BRANCH" ]]
  if [[ "$DEPLOY_REPOSITORY" =~ ^https://github\.com/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)$ ]]; then
    owner="${BASH_REMATCH[1]}"
    repository="${BASH_REMATCH[2]%.git}"
    [[ -n "$repository" && "$repository" != '.' && "$repository" != '..' ]]
    curl -fsSL --connect-timeout 15 --max-time 1200 --retry 2 --retry-delay 2 \
      --output "$STAGING/source.tar.gz" \
      "https://codeload.github.com/$owner/$repository/tar.gz/$REVISION"
    mkdir "$STAGING/source"
    tar -xzf "$STAGING/source.tar.gz" --strip-components 1 -C "$STAGING/source"
  else
    run_git clone --quiet --single-branch --depth 1 --branch "$DEPLOY_BRANCH" \
      "$DEPLOY_REPOSITORY" "$STAGING/source"
    [[ "$(run_git -C "$STAGING/source" rev-parse HEAD)" == "$REVISION" ]]
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

[[ -f "$CONFIG" && -f "$ENV_FILE" && -f "$OVERLAY" ]]
command -v docker >/dev/null
command -v python3 >/dev/null
command -v curl >/dev/null
command -v flock >/dev/null
command -v tar >/dev/null
command -v timeout >/dev/null
read_config
mkdir -p "$RELEASES" "$BACKUPS"
exec 9> "$ROOT/.deploy.lock"
flock -n 9 || { printf 'Another deployment is running.\n' >&2; exit 1; }
mkdir -p "$LOGS"
LOG_FILE="$(mktemp "$LOGS/deploy-$(date +%Y%m%d%H%M%S).XXXXXXXX")"
exec > >(tee -a "$LOG_FILE") 2>&1
printf 'Deployment log: %s\n' "$LOG_FILE"

STAGE="fetch"
run_git check-ref-format --branch "$DEPLOY_BRANCH" > /dev/null
STAGING="$(mktemp -d "$RELEASES/pending.XXXXXXXX")"
fetch_source
export AIHOT_DEPLOY_REV="${REVISION}-$(date +%Y%m%d%H%M%S)-$$"
RELEASE="$STAGING/source"
[[ ! -e "$RELEASE/.env" && ! -L "$RELEASE/.env" ]]
for required in .dockerignore package.json package-lock.json deploy/compose.yaml \
  deploy/Dockerfile.setup deploy/Dockerfile.web backend-go/Dockerfile \
  shared/site/package.json shared/industry/package.json shared/contracts/package.json \
  tooling/package.json legacy/ts-compat/api/package.json legacy/ts-compat/backend/package.json; do
  [[ -f "$RELEASE/$required" ]] || { printf 'Missing build file: %s\n' "$required" >&2; exit 1; }
done
ln -s "$ENV_FILE" "$RELEASE/.env"
printf 'Deploying branch %s at commit %s\n' "$DEPLOY_BRANCH" "$REVISION"

STAGE="preflight"
validate_config
SITE_DOMAIN="$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' aihot-caddy-1 | sed -n 's/^SITE_DOMAIN=//p')"
[[ "$SITE_DOMAIN" =~ ^[A-Za-z0-9.-]+$ ]]
printf 'Current application images:\n'
docker inspect -f '{{.Name}} {{.Config.Image}} {{index .Config.Labels "com.docker.compose.project.working_dir"}} {{index .Config.Labels "com.docker.compose.project.config_files"}}' \
  aihot-go-api-1 aihot-go-worker-1 aihot-web-1 | tee "$STAGING/previous-images.txt"

STAGE="build"
compose build setup go-api web

STAGE="backup"
compose stop go-worker
backup_database

STAGE="migration"
compose stop web go-api
compose run --rm --no-deps setup

STAGE="startup"
compose up -d --no-deps go-api

STAGE="health"
check_health
printf 'Deployment completed: %s\nRelease: %s\n' "$REVISION" "$RELEASE"
