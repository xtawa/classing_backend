#!/bin/sh
set -eu

repo_dir="${CLASSING_REPO_DIR:-/opt/classing/backend}"
backup_dir="${CLASSING_BACKUP_DIR:-/opt/classing/backups}"
health_url="${CLASSING_HEALTH_URL:-http://127.0.0.1:8080/health/ready}"
lock_file="${CLASSING_DEPLOY_LOCK:-/run/lock/classing-deploy.lock}"
state_file="${CLASSING_DEPLOY_STATE:-/opt/classing/.deployed-commit}"

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
if [ -s "$state_file" ]; then
  deployed_commit="$(cat "$state_file")"
else
  deployed_commit="$local_commit"
  mkdir -p "$(dirname "$state_file")"
  printf '%s\n' "$deployed_commit" > "$state_file"
fi

if [ "$deployed_commit" = "$remote_commit" ]; then
  echo "Classing is already deployed at $remote_commit"
  exit 0
fi

if ! git merge-base --is-ancestor "$local_commit" "$remote_commit"; then
  echo "refusing to deploy: origin/main is not a fast-forward from $local_commit" >&2
  exit 1
fi

build_dir="$(mktemp -d /tmp/classing-build.XXXXXX)"
cleanup() {
  git worktree remove --force "$build_dir" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM
git worktree add --detach "$build_dir" "$remote_commit"
target_image="classing-backend:$remote_commit"
docker build --pull --build-arg "GIT_COMMIT=$remote_commit" --label "org.opencontainers.image.revision=$remote_commit" -t "$target_image" "$build_dir"

backup_path="$(CLASSING_REPO_DIR="$repo_dir" CLASSING_BACKUP_DIR="$backup_dir" "$repo_dir/deploy/backup.sh")"
test -s "$backup_path/postgres.dump"
previous_image="$(docker inspect --format '{{.Config.Image}}' backend-classing-1)"

if [ "$local_commit" != "$remote_commit" ]; then
  git pull --ff-only origin main
fi
CLASSING_IMAGE="$target_image" docker compose up -d

attempt=0
while [ "$attempt" -lt 60 ]; do
  status="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' backend-classing-1 2>/dev/null || true)"
  if [ "$status" = "healthy" ]; then
    break
  fi
  if [ "$status" = "unhealthy" ] || [ "$status" = "exited" ]; then
    docker compose logs --tail=200 classing >&2
    CLASSING_IMAGE="$previous_image" docker compose up -d classing || true
    exit 1
  fi
  attempt=$((attempt + 1))
  sleep 2
done

if ! test "$(docker inspect --format '{{.State.Health.Status}}' backend-classing-1)" = "healthy" || ! curl -fsS "$health_url" >/dev/null; then
  CLASSING_IMAGE="$previous_image" docker compose up -d classing || true
  exit 1
fi

test "$(git rev-parse HEAD)" = "$remote_commit"
mkdir -p "$(dirname "$state_file")"
printf '%s\n' "$remote_commit" > "$state_file.tmp"
mv "$state_file.tmp" "$state_file"
echo "deployed Classing $deployed_commit -> $remote_commit; backup=$backup_path"
