#!/bin/sh
# Pull-based updater (compose profile "autoupdate").
#
# Every UPDATE_INTERVAL seconds: pull the router and ui images; if either changed, tag the
# running images as :rollback, apply both together, wait for the router to be healthy, and
# put the previous images back if it isn't. Only router and ui are ever touched; the updater
# never recreates itself (or proxy), so the loop can't be killed mid-update.
#
# Runs inside docker:cli with the host's docker.sock and the project dir mounted at the same
# absolute path (see docker-compose.yml), so `docker compose` here behaves like on the host.

set -u

INTERVAL="${UPDATE_INTERVAL:-300}"
WEBHOOK="${UPDATE_WEBHOOK_URL:-}"
HEALTH_TIMEOUT=120

# Same project name and directory as the host's compose project.
dc() { docker compose -p ai-gateway --project-directory "${GATEWAY_DIR:?GATEWAY_DIR not set}" "$@"; }

log() { echo "$(date -u '+%Y-%m-%dT%H:%M:%SZ') updater: $*"; }

notify() {
  [ -n "$WEBHOOK" ] || return 0
  # Keep the JSON valid: drop quotes and backslashes from the message.
  # shellcheck disable=SC1003  # deleting a literal backslash is intended
  msg=$(printf '%s' "$1" | tr -d '"' | tr -d '[:cntrl:]\\')
  wget -q -O /dev/null --header 'Content-Type: application/json' \
    --post-data "{\"content\": \"$msg\"}" "$WEBHOOK" \
    || log "webhook notification failed"
}

short() { printf '%s' "${1#sha256:}" | cut -c1-12; }

image_ref() { dc config --images "$1" 2>/dev/null | head -n 1; }
running_image_id() {
  cid=$(dc ps -q "$1" 2>/dev/null | head -n 1)
  [ -n "$cid" ] && docker inspect --format '{{.Image}}' "$cid" 2>/dev/null
}
local_image_id() { docker image inspect --format '{{.Id}}' "$1" 2>/dev/null; }

wait_healthy() {
  cid=$(dc ps -q router | head -n 1)
  [ -n "$cid" ] || return 1
  waited=0
  while [ "$waited" -lt "$HEALTH_TIMEOUT" ]; do
    status=$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$cid" 2>/dev/null)
    [ "$status" = "healthy" ] && return 0
    sleep 3
    waited=$((waited + 3))
    # The container may be recreated while we wait; keep following the current one.
    cid=$(dc ps -q router | head -n 1)
    [ -n "$cid" ] || return 1
  done
  return 1
}

# Backup hook: the database must be backed up before applying an update, since a new router
# may run migrations. `router backup` doesn't exist yet.
# TODO(#19): run the router's backup command here and abort the update if it fails.
backup_database() {
  log "WARNING: database backup skipped (router backup not implemented yet, see issue #19)"
  return 0
}

failed_ids=""   # new image IDs that already failed, so a bad release isn't retried every cycle

check_once() {
  old_router=$(running_image_id router)
  old_ui=$(running_image_id ui)
  if [ -z "$old_router" ] || [ -z "$old_ui" ]; then
    log "router or ui is not running; skipping (start them with: docker compose up -d)"
    return 0
  fi

  if ! dc pull --quiet router ui; then
    log "pull failed; will retry next cycle"
    return 0
  fi

  router_ref=$(image_ref router)
  ui_ref=$(image_ref ui)
  new_router=$(local_image_id "$router_ref")
  new_ui=$(local_image_id "$ui_ref")
  if [ -z "$new_router" ] || [ -z "$new_ui" ]; then
    log "could not resolve pulled images; skipping"
    return 0
  fi

  if [ "$new_router" = "$old_router" ] && [ "$new_ui" = "$old_ui" ]; then
    return 0
  fi

  ids="$new_router/$new_ui"
  case " $failed_ids " in *" $ids "*) return 0 ;; esac

  desc="router $(short "$old_router") -> $(short "$new_router"), ui $(short "$old_ui") -> $(short "$new_ui")"
  log "new images found: $desc"

  # Keep the previous images reachable by name so they survive pruning and can be restored.
  docker tag "$old_router" "${router_ref%:*}:rollback"
  docker tag "$old_ui" "${ui_ref%:*}:rollback"

  backup_database || { log "backup failed; update aborted"; return 0; }

  log "applying update"
  dc up -d --no-deps --no-build --pull never router ui
  if wait_healthy; then
    log "update succeeded: $desc"
    notify "ai-gateway updated: $desc"
    return 0
  fi

  log "router not healthy within ${HEALTH_TIMEOUT}s; rolling back"
  failed_ids="$failed_ids $ids"
  # Point the tag compose uses back at the previous images, then recreate.
  docker tag "$old_router" "$router_ref"
  docker tag "$old_ui" "$ui_ref"
  dc up -d --no-deps --no-build --pull never router ui
  if wait_healthy; then
    log "rollback succeeded; staying on $(short "$old_router")/$(short "$old_ui")"
    notify "ai-gateway ROLLED BACK after failed update ($desc); previous version restored"
  else
    log "ROLLBACK FAILED: router still unhealthy; manual intervention needed"
    notify "ai-gateway rollback FAILED after update ($desc); router is unhealthy, manual fix needed"
  fi
  # TODO(#19): restore the pre-update database backup here if a migration ran.
}

log "started; checking every ${INTERVAL}s (images follow IMAGE_TAG=${IMAGE_TAG:-stable})"
while true; do
  check_once
  sleep "$INTERVAL"
done
