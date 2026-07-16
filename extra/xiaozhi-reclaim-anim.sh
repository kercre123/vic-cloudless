#!/bin/sh
# Soft-reclaim vic-anim RSS after Xiaozhi WSS ends without blanking the face.
# Restarting only vic-anim under low MemAvailable can fail LCD SPI (fault 990).
set -u

log() { echo "[xiaozhi-reclaim] $*"; }

systemctl stop vic-anim.service 2>/dev/null || true
sleep 1
systemctl start vic-anim.service
sleep 2
# Engine often drops the anim socket across anim recycle — bring it back.
if ! systemctl is-active --quiet vic-engine.service; then
  log "vic-engine inactive after anim recycle — starting"
  systemctl start vic-engine.service
  sleep 2
fi
if ! systemctl is-active --quiet vic-cloud.service; then
  log "vic-cloud inactive — starting"
  systemctl start vic-cloud.service
fi

if ! systemctl is-active --quiet vic-anim.service; then
  log "vic-anim not active — restarting anki-robot.target"
  systemctl restart anki-robot.target
  exit 0
fi

if journalctl -u vic-anim --no-pager -n 40 --since "15 sec ago" 2>/dev/null | grep -q "Can't open LCD SPI"; then
  log "LCD SPI open failed — restarting anki-robot.target to restore eyes"
  rm -f /run/fault_code /run/fault_code.pending 2>/dev/null || true
  systemctl restart anki-robot.target
  exit 0
fi

log "vic-anim recycled OK"
exit 0
