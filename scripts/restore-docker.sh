#!/bin/sh
# Restore only to a new Docker volume. Never delete or overwrite an old volume.
set -eu
usage() {
    printf '%s\n' 'Usage: sh scripts/restore-docker.sh /absolute/backup.zip.age new-volume [/absolute/recovery.key]' >&2
    exit 2
}
[ "$#" -ge 2 ] && [ "$#" -le 3 ] || usage
input=$1
target=$2
identity=${3:-}
case "$target" in ''|*[!A-Za-z0-9_.-]*|[!A-Za-z0-9]*) printf '%s\n' 'Invalid new volume name.' >&2; exit 2;; esac
absolute_file() {
    [ -f "$1" ] || { printf '%s\n' 'Backup or identity file is missing.' >&2; return 1; }
    case "$1" in *,*) printf '%s\n' 'File paths containing commas are unsupported.' >&2; return 1;; esac
    directory=$(CDPATH= cd -- "$(dirname -- "$1")" && pwd -P)
    printf '%s/%s\n' "$directory" "$(basename -- "$1")"
}
input=$(absolute_file "$input")
if [ -n "$identity" ]; then identity=$(absolute_file "$identity"); fi
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$root"
command -v docker >/dev/null 2>&1 || { printf '%s\n' 'Docker CLI is required.' >&2; exit 1; }
docker compose version >/dev/null
docker info >/dev/null
if docker volume inspect "$target" >/dev/null 2>&1; then
    printf '%s\n' 'Target volume already exists; choose a new unused name. No data changed.' >&2
    exit 1
fi
# Relay Lab's provided Compose has one service/image. Refuse ambiguous projects.
images=$(docker compose config --images)
count=$(printf '%s\n' "$images" | awk 'NF {n++} END {print n+0}')
[ "$count" -eq 1 ] || { printf '%s\n' 'Expected exactly one Relay Lab Compose image.' >&2; exit 1; }
image=$images
docker image inspect "$image" >/dev/null
# Creation is non-destructive. Keep a failed new volume for diagnosis.
owner=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')
[ "${#owner}" -eq 32 ] || { printf '%s\n' 'Could not generate an operation identifier.' >&2; exit 1; }
docker volume create --label "relaylab.restore=$owner" "$target" >/dev/null
actual_owner=$(docker volume inspect --format '{{index .Labels "relaylab.restore"}}' "$target")
[ "$actual_owner" = "$owner" ] || { printf '%s\n' 'Target was created by another operation; no data changed.' >&2; exit 1; }
printf '%s\n' 'Validating the backup into the new volume; the existing service remains running.'
set -- docker run --rm --network none --read-only --user 0:0 \
    --cap-drop ALL --cap-add CHOWN --cap-add DAC_OVERRIDE \
    --security-opt no-new-privileges:true \
    --mount "type=volume,source=$target,target=/var/lib/relaylab" \
    --mount "type=bind,source=$input,target=/restore-input/archive,readonly" \
    --entrypoint /bin/sh
if [ -n "$identity" ]; then
    set -- "$@" --mount "type=bind,source=$identity,target=/restore-input/identity,readonly"
    mode=encrypted
else
    mode=legacy
fi
# root is used only inside this one-shot offline helper to read a 0600 host key
# and fix new-volume ownership. No Docker socket is mounted into the container.
if ! "$@" "$image" -c '
    set -eu
    mode=$1
    set -- /usr/local/bin/relaylab restore --config /etc/relaylab/config.yaml --input /restore-input/archive
    if [ "$mode" = encrypted ]; then set -- "$@" --identity-file /restore-input/identity; fi
    "$@"
    chown -R 10001:10001 /var/lib/relaylab/data /var/lib/relaylab/backups
' -- "$mode"; then
    printf '%s\n' 'Restore failed. Original service and volume are unchanged. Keep the new volume for diagnosis.' >&2
    exit 1
fi
printf '%s\n' 'Backup validated. Stopping Relay Lab before the explicit volume switch; the original volume is preserved.'
docker compose stop -t 720 relaylab
printf '\n%s\n' 'Restore validated into the new volume. The existing deployment has not been switched.'
printf 'Set this in the project private .env file: RELAYLAB_DATA_VOLUME=%s\n' "$target"
printf '%s\n' 'Then run: docker compose up -d --no-build --force-recreate'
printf '%s\n' 'Check container health, log in with the restored account and reconcile backup indexes.'
printf '%s\n' 'Keep the original volume until the restored deployment is verified.'
