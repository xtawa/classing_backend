#!/bin/sh
set -eu

backup_dir="${1:?usage: verify-restore.sh BACKUP_DIR ISOLATED_POSTGRES_CONTAINER}"
container="${2:?usage: verify-restore.sh BACKUP_DIR ISOLATED_POSTGRES_CONTAINER}"

case "$container" in
  backend-postgres-1|classing-postgres-1|postgres) echo "refusing to restore into a production-like container name" >&2; exit 1 ;;
esac

(cd "$backup_dir" && sha256sum -c SHA256SUMS)
test -s "$backup_dir/postgres.dump"
test -s "$backup_dir/releases.tar.gz"
docker exec -i "$container" sh -c 'createdb -U "$POSTGRES_USER" classing_restore_check'
docker exec -i "$container" sh -c 'pg_restore --exit-on-error -U "$POSTGRES_USER" -d classing_restore_check --clean --if-exists' < "$backup_dir/postgres.dump"
docker exec "$container" sh -c 'psql -U "$POSTGRES_USER" -d classing_restore_check -Atc "SELECT COUNT(*) FROM schema_migrations"'
tar -tzf "$backup_dir/releases.tar.gz" >/dev/null
echo "isolated restore verification passed"
