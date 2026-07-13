#!/bin/sh
set -eu

repo_dir="${CLASSING_REPO_DIR:-/opt/classing/backend}"
backup_root="${CLASSING_BACKUP_DIR:-/opt/classing/backups}"
stamp="${CLASSING_BACKUP_STAMP:-$(date -u +%Y%m%dT%H%M%SZ)}"
destination="$backup_root/classing-$stamp"

mkdir -p "$destination"
chmod 700 "$destination"
cd "$repo_dir"

docker compose exec -T postgres sh -c 'pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc' > "$destination/postgres.dump"
test -s "$destination/postgres.dump"

docker compose exec -T classing sh -c 'if [ -d /data/releases ]; then tar -C /data -czf - releases; else tar -czf - --files-from /dev/null; fi' > "$destination/releases.tar.gz"
test -s "$destination/releases.tar.gz"

{
  printf 'created_at=%s\n' "$stamp"
  printf 'git_commit=%s\n' "$(git rev-parse HEAD)"
  printf 'compose_project=%s\n' "$(basename "$(dirname "$repo_dir")")"
} > "$destination/manifest.txt"

(cd "$destination" && sha256sum postgres.dump releases.tar.gz manifest.txt > SHA256SUMS)
test -s "$destination/SHA256SUMS"
chmod 600 "$destination"/*
printf '%s\n' "$destination"
