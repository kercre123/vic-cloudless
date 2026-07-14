# Xiaozhi self-control (MCP) — Vector intents as ESP32-style tools

Snapshot 2026-07-13. Maps the old Vosk/cloudless keyword→`intent_*` table into
device MCP tools the Xiaozhi LLM can call (same idea as `xiaozhi-esp32` `self.*`).

## Flow

```text
User speech → Xiaozhi STT → LLM
  ├─ tools/call self.vector.action / photo / volume / stop
  │     → (optional TTS confirmation) → OnIntent(intent_*) → engine
  └─ else normal TTS chat
```

**No STT keyword / alias matching.** Intent runs only when the Xiaozhi LLM
calls an MCP tool (`self.vector.action` with an action id enum).

AbortSpeaking / barge-in stays a separate WSS `type:abort` (not an MCP tool), like ESP32.

## Tools advertised (`tools/list`)

| Tool | Role |
|------|------|
| `self.get_device_status` | Status JSON |
| `self.audio_speaker.set_volume` | → `intent_imperative_volumelevel_extend` + `volume_level` |
| `self.camera.take_photo` | → `intent_photo_take_extend` (album only) |
| `self.camera.analyze_photo` | Capture JPEG → Xiaozhi vision Explain URL → text for LLM (“thấy gì”) |
| `self.vector.action` | Otto-style: `action` enum → Vector `intent_*` |
| `self.vector.stop` | → `intent_imperative_quiet` |
| `self.vector.set_eye_color` | Eye color presets |

`analyze_photo` needs `capabilities.vision.{url,token}` from MCP `initialize` (ESP32 Explain). See `docs/XIAOZHI-VISION-ANALYZE-REVERT.md`.

Protocol: ESP32 envelope — inbound/outbound `type:mcp` + `payload` JSON-RPC
(`initialize`, `tools/list`, `tools/call`). Results use `{content:[{type:text,text}], isError}`.

## `self.vector.action` catalogue

| action id | Vector intent | Hint for LLM |
|-----------|---------------|--------------|
| fireworks | `intent_seasonal_happynewyear` | pháo hoa / fireworks |
| happy_holidays | `intent_seasonal_happyholidays` | christmas / ngày lễ |
| dance | `intent_imperative_dance` | nhảy múa |
| go_home | `intent_system_charger` | về nhà / về sạc |
| sleep | `intent_system_sleep` | đi ngủ |
| … | … | see `vectorActions` in `mcp.go` |

Full list: `internal/xiaozhi/mcp.go` → `vectorActions` (action id → intent only; **no aliases**).

## Code

| File | Change |
|------|--------|
| `internal/xiaozhi/mcp.go` | Tool registry + local match |
| `internal/xiaozhi/client.go` | `payload` unwrap + ESP32 `SendMCPResult` |
| `internal/xiaozhi/turn.go` | Local match after STT; MCP params; mid-TTS MCP aborts speak |
| `internal/voice/stream/init.go` | `OnIntent` with encoded params |

## Not the same as Vosk-only path

When Xiaozhi is **off**, `vtr.ProcessTextAll(en-US.json)` still runs (keyword intents).  
When Xiaozhi is **on**, **only** MCP `tools/call` can trigger robot intents (no local STT keyword match).

## Test

```bash
# on robot (Xiaozhi on): wake → "bắn pháo hoa đi"
# expect log: [Xiaozhi] MCP self.vector.action → intent intent_seasonal_happynewyear
# NOT: local self-control match
# after TTS: xiaozhi-dbg stream_done/post_ok, relisten within ~PCM duration (not 120s)
```
