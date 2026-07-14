# TTS ESP32-P0 — REVERTED (2026-07-13)

## Status: **REVERTED**

P0 (skip `VC_ListeningGetOut` + latch `0.02s`) made things worse:
- Still missing first TTS syllables
- Later sentences / long TTS often silent or cut (`busy cleared early` while cloud still had seconds of PCM)

Restored:
- `STREAM_AUDIO_TO_BEGIN_PLAYING_SEC` → **0.55f**
- Engine `TransitionToThinking` → always play ListeningGetOut again (no xiaozhi-busy skip)

Kept (pre-P0 head experiments — see `REVERT-tts-prebuffer-getout.md`):
- StopAll Animation/Behavior around stream start / first 1.5s
- Anim abort late getout while busy
- Cloud: append first PCM before `afterSTT`; clear legacy stream-end flags

---

## Next plan (không lặp P0)

Plan **C** đang thử — xem `REVERT-tts-plan-c-getout-settle.md`.

### Root causes còn lại (từ log)

1. **Head cut:** getout / Animation bus vẫn át ExternalAudio vài trăm ms–vài giây đầu; Wwise vẫn consume portal → mất syllable.
2. **Tail / “audio sau không phát”:** `busy cleared early` — anim `OnAudioCompleted` / portal underrun kết thúc sớm trong khi file PCM còn dài (cloud đã append đủ). Latch 0.02s làm underrun nặng hơn.

### Phương án A — Fix drain trước

...

### Phương án B — Mute bus Animation

...

### Phương án C — Chờ getout rồi Post (**ACTIVE experiment**)

Xem `REVERT-tts-plan-c-getout-settle.md`.

### Phương án D — ESP32 path thật (lớn)

...

---

## Re-apply P0 (không khuyến nghị)

Xem git history / đoạn cũ trong chat: latch `0.02f` + skip getout khi `xiaozhi-busy`.
