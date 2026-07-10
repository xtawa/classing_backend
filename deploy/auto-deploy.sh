#!/bin/sh
set -eu

repo_dir="${CLASSING_REPO_DIR:-/opt/classing/backend}"
backup_dir="${CLASSING_BACKUP_DIR:-/opt/classing/backups}"
health_url="${CLASSING_HEALTH_URL:-http://127.0.0.1:8080/health/ready}"
lock_file="${CLASSING_DEPLOY_LOCK:-/run/lock/classing-deploy.lock}"

exec 9>"$lock_file"
if ! flock -n 9; then
  echo "another Classing deployment is already running"
  exit 0
fi

cd "$repo_dir"

if [ -n "$(git status --porcelain)" ]; then
  echo "refusing to deploy: server working tree is not clean" >&2
  git status --short >&2
  exit 1
fi

git fetch --prune origin main
local_commit="$(git rev-parse HEAD)"
remote_commit="$(git rev-parse origin/main)"

if [ "$local_commit" = "$remote_commit" ]; then
  echo "Classing is already current at $local_commit"
  exit 0
fi

if ! git merge-base --is-ancestor "$local_commit" "$remote_commit"; then
  echo "refusing to deploy: origin/main is not a fast-forward from $local_commit" >&2
  exit 1
fi

mkdir -p "$backup_dir"
stamp="$(date +%Y%m%d-%H%M%S)"
backup_file="$backup_dir/classing-auto-$stamp.dump"
docker compose exec -T postgres sh -c 'pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc' > "$backup_file"
test -s "$backup_file"
chmod 600 "$backup_file"

git pull --ff-only origin main
docker compose build --pull classing
docker compose up -d

attempt=0
while [ "$attempt" -lt 60 ]; do
  status="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' backend-classing-1 2>/dev/null || true)"
  if [ "$status" = "healthy" ]; then
    break
  fi
  if [ "$status" = "unhealthy" ] || [ "$status" = "exited" ]; then
    docker compose logs --tail=200 classing >&2
    exit 1
  fi
  attempt=$((attempt + 1))
  sleep 2
done

test "$(docker inspect --format '{{.State.Health.Status}}' backend-classing-1)" = "healthy"
curl -fsS "$health_url" >/dev/null

deployed_commit="$(git rev-parse HEAD)"
test "$deployed_commit" = "$remote_commit"
echo "deployed Classing $local_commit -> $deployed_commit; backup=$backup_file"
