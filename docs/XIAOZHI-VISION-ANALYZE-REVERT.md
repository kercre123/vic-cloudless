# Xiaozhi vision analyze_photo — revert guide (2026-07-14)

Adds **`self.camera.analyze_photo`**: capture JPEG → upload to Xiaozhi `capabilities.vision` Explain URL → return text to LLM.

**`self.camera.take_photo` is unchanged in behavior** (still queues `intent_photo_take_extend` for the album). Only its tool *description* was clarified so the LLM picks the right tool.

---

## If you need to revert

### On robot (fastest)

```bash
# If you backed up before this deploy:
ssh root@<IP> 'mount -o rw,remount /; cp -a /anki/bin/vic-cloud.bak-pre-vision-analyze /anki/bin/vic-cloud; systemctl restart anki-robot.target'
```

Or restore any prior known-good `vic-cloud` binary.

### In source (`anki/vic-cloudless`)

```bash
# Remove new file
rm -f internal/xiaozhi/vision.go

# Restore touched files (if under git):
git checkout -- \
  internal/xiaozhi/mcp.go \
  cloud/main.go \
  cloud/message_handler.go \
  docs/XIAOZHI-SELF-CONTROL-MCP.md

# Rebuild + deploy
make vic-cloud
./deploy.sh <ROBOT_IP>
```

Manual undo if no git:

| File | Undo |
|------|------|
| `internal/xiaozhi/vision.go` | Delete |
| `internal/xiaozhi/mcp.go` | Remove vision parse in `handleInitialize`; remove `analyze_photo` tool + switch case; restore old `take_photo` description |
| `cloud/message_handler.go` | Remove `CaptureJPEGBytes` |
| `cloud/main.go` | Remove `xiaozhi.SetJPEGCapture(CaptureJPEGBytes)` from `xzcloudinit` |

---

## What this feature needs at runtime

1. Xiaozhi MCP `initialize` must include `params.capabilities.vision.url` (+ optional `token`).
2. Logs on connect:
   - `vision Explain URL set: …` **or**
   - `no capabilities.vision (analyze_photo will fail until set)`
3. On ask “mày thấy gì”:
   - `MCP ← tools/call` / `analyze_photo captured N bytes`
   - `analyze_photo Explain OK` **or** clear error

Album-only: “chụp hình” → still `self.camera.take_photo` → `intent_photo_take_extend`.

---

## Smoke test after deploy

| Say | Expect |
|-----|--------|
| Chụp hình | `take_photo` + photo in UI Ảnh |
| Mày thấy gì / what do you see | `analyze_photo` + TTS description (if vision URL present) |
