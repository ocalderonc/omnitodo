#!/usr/bin/env bash
#
# Nightly encrypted backup of the OmniTodo database. Adapted from Moneta's.
#
# Install at /opt/omnitodo/backup.sh on the VPS, chmod 700, and run it from
# root's cron:
#
#   23 3 * * * /opt/omnitodo/backup.sh >> /var/log/omnitodo-backup.log 2>&1
#
# Configuration comes from /etc/omnitodo-backup.env (root-only):
#
#   COMPOSE_PROJECT=<coolify resource uuid>   # docker ps --format '{{.Labels}}' | grep omnitodo
#   AGE_RECIPIENT=age1...                    # the PUBLIC key; the private one is off this box
#   RCLONE_REMOTE=remote:omnitodo-backups    # off-site copy
#   KEEP_DAYS=14                             # local retention
#
# How: `omnitodo backup` runs VACUUM INTO inside the running container, which
# gives a consistent copy while the app keeps serving, with no sqlite3 needed on
# the host. The copy is pulled out with `docker cp`, encrypted with age, and
# shipped with rclone. Restore steps are in README §9.4.
set -euo pipefail

CONFIG=${OMNITODO_BACKUP_ENV:-/etc/omnitodo-backup.env}
DEST=${OMNITODO_BACKUP_DIR:-/var/backups/omnitodo}

if [[ ! -r "$CONFIG" ]]; then
  echo "backup: cannot read $CONFIG" >&2
  exit 1
fi
# shellcheck source=/dev/null
set -a && . "$CONFIG" && set +a

: "${COMPOSE_PROJECT:?set COMPOSE_PROJECT in $CONFIG}"
: "${AGE_RECIPIENT:?set AGE_RECIPIENT in $CONFIG}"
KEEP_DAYS=${KEEP_DAYS:-14}

for tool in docker age; do
  command -v "$tool" >/dev/null || { echo "backup: $tool is not installed" >&2; exit 1; }
done

# Found by compose labels rather than by name: Coolify suffixes container
# names with deployment ids that change on every deploy.
CONTAINER=$(docker ps -q \
  --filter "label=com.docker.compose.project=$COMPOSE_PROJECT" \
  --filter "label=com.docker.compose.service=app")
if [[ -z "$CONTAINER" || $(wc -w <<<"$CONTAINER") -ne 1 ]]; then
  echo "backup: expected exactly one running app container for project $COMPOSE_PROJECT, found: '${CONTAINER}'" >&2
  exit 1
fi

STAMP=$(date -u +%Y%m%dT%H%M%SZ)
ARCHIVE="$DEST/omnitodo-$STAMP.db.age"
mkdir -p "$DEST"
chmod 700 "$DEST"

WORK=$(mktemp -d "$DEST/.work-$STAMP.XXXXXX")
trap 'rm -rf "$WORK"' EXIT

# /data is the container's only writable path, so the snapshot goes there and
# is overwritten on the next run.
docker exec "$CONTAINER" /usr/local/bin/omnitodo backup /data/backup.db >/dev/null
docker cp "$CONTAINER:/data/backup.db" "$WORK/omnitodo.db"

# A VACUUMed database with the schema alone is well over this; an empty or
# truncated file is not a backup.
SIZE=$(stat -c%s "$WORK/omnitodo.db")
if [[ "$SIZE" -lt 16384 ]]; then
  echo "backup: snapshot is suspiciously small ($SIZE bytes), refusing to keep it" >&2
  exit 1
fi

# Encrypted to a public key: this machine cannot read what it wrote. Written to
# a temporary name and moved into place only when complete.
age -r "$AGE_RECIPIENT" -o "$WORK/archive.age" "$WORK/omnitodo.db"
mv "$WORK/archive.age" "$ARCHIVE"
chmod 600 "$ARCHIVE"

if [[ -n "${RCLONE_REMOTE:-}" ]]; then
  command -v rclone >/dev/null || { echo "backup: rclone is not installed" >&2; exit 1; }
  rclone copy "$ARCHIVE" "$RCLONE_REMOTE/"
else
  echo "backup: WARNING no RCLONE_REMOTE set, this archive exists only on this host" >&2
fi

find "$DEST" -name 'omnitodo-*.db.age' -mtime "+$KEEP_DAYS" -delete

echo "backup ok: $(basename "$ARCHIVE") ($SIZE bytes before encryption)"
