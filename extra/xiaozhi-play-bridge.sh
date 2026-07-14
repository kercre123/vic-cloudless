#!/bin/sh
# Mirror only busy/relisten for older mic/anim helpers.
# Do NOT move stream/cancel/play/end — current vic-anim watches /run/vic-cloud
# directly; moving those flags raced cancel vs stream (silent TTS after barge-in).
while true; do
  if [ -f /run/vic-cloud/xiaozhi-busy ]; then
    echo 1 > /run/xiaozhi-busy 2>/dev/null
  else
    rm -f /run/xiaozhi-busy 2>/dev/null
  fi
  if [ -f /run/vic-cloud/xiaozhi-relisten-pending ]; then
    echo 1 > /run/xiaozhi-relisten-pending 2>/dev/null
  fi
  if [ -f /run/vic-cloud/xiaozhi-relisten ]; then
    echo 1 > /run/xiaozhi-relisten 2>/dev/null
  fi
  # Drop legacy stream/cancel leftovers so they cannot cancel a /run/vic-cloud stream.
  rm -f /run/xiaozhi-stream /run/xiaozhi-stream-end /run/xiaozhi-play /run/xiaozhi-cancel 2>/dev/null
  sleep 0.05
done
