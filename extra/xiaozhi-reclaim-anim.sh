#!/bin/sh
# Reclaim Xiaozhi/Wwise RSS after WSS conversation ends.
#
# Recycling only vic-anim leaves engine/cloud sockets desynced → next wake
# shows NoCloud / mic open-close. Conversation is already over (WSS closed),
# so restart the full robot target — eyes return and Hey Vector works again.
set -u

log() { echo "[xiaozhi-reclaim] $*"; }

rm -f /run/fault_code /run/fault_code.pending 2>/dev/null || true
log "restarting anki-robot.target (post-WSS reclaim)"
systemctl restart anki-robot.target
# Give anim/engine a moment before cloud callers assume sockets are up.
sleep 6
if systemctl is-active --quiet vic-anim.service \
   && systemctl is-active --quiet vic-engine.service \
   && systemctl is-active --quiet vic-cloud.service; then
  log "stack active after reclaim"
  exit 0
fi
log "stack not fully active — retry anki-robot.target"
systemctl restart anki-robot.target
sleep 8
exit 0
