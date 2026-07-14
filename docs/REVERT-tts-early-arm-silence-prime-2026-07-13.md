# REVERT — TTS early-arm + silence prime (wirepod-style) 2026-07-13

Goal: stop dropping the first TTS syllables by arming ExternalAudio **before** real speech (like `wirepodxiaozhi`), then Post speaker only after **real** PCM (not silence prime).

## Behavior

| Step | Change |
|------|--------|
| Cloud | On TTS start (or first Opus if start missed): `StreamStart` + append **3×1024** zero PCM, then real frames only `StreamAppend` |
| Anim | Feed silence into portal to warm DAC; `PostAudioEvent` only after ~200ms of **non-silent** PCM |

## Files touched

| File | Change |
|------|--------|
| `vic-cloudless/internal/xiaozhi/playback.go` | `ArmStreamWithSilencePrime()` |
| `vic-cloudless/internal/xiaozhi/turn.go` | Arm on `TTSStateStart` / first audio; first real frame append-only |
| `victor/.../sdkAudioComponent.h` | `_pcmSawSpeech`, `_pcmSpeechFramesReceived` |
| `victor/.../sdkAudioComponent.cpp` | Silence detect; Post threshold uses speech frames only |

## How to test

1. Short Vietnamese/English sentence after wake — first syllable audible.
2. Continuous second turn — still OK.
3. Barge-in mid-TTS — next turn still speaks.
4. Logs: `stream armed early (silence prime …)` then `TTS audio FIRST real frame — append only`.

## Revert

```bash
cd /home/linh/Projects/wire-os

# Cloud
git checkout -- \
  anki/vic-cloudless/internal/xiaozhi/playback.go \
  anki/vic-cloudless/internal/xiaozhi/turn.go

# Anim (or restore these hunks manually if not in git)
git checkout -- \
  anki/victor/animProcess/src/cozmoAnim/audio/sdkAudioComponent.cpp \
  anki/victor/animProcess/src/cozmoAnim/audio/sdkAudioComponent.h
```

If tree is not a single git repo, restore from this doc’s “pre-change” notes:

### Cloud — remove early arm

- Delete `ArmStreamWithSilencePrime` from `playback.go`.
- In `turn.go`: restore first-Opus path that calls `StreamStart()` + append first chunk (no arm on `TTSStateStart`).

### Anim — remove speech-only Post gate

- Remove `_pcmSawSpeech` / `_pcmSpeechFramesReceived` and silence helper.
- In `tryStartSpeaker`, restore:

```cpp
const float totalAudioReceived_s = _totalAudioFramesReceived / (float)_audioRate;
if (totalAudioReceived_s <= STREAM_AUDIO_TO_BEGIN_PLAYING_SEC) {
  return true;
}
```

## Deploy

```bash
# vic-cloud
cd anki/vic-cloudless && make vic-cloud
# scp build/vic-cloud → /data/local/vic-cloud.new → remount rw → /anki/bin/vic-cloud

# vic-anim
cd anki/victor && ./build-v.sh   # or incremental ninja vic-anim
# deploy binary to /anki/bin/vic-anim (stop anim first if file busy)
```

Robot: `192.168.100.247`, key `anki/vic-cloudless/ssh_root_key`.

## Related

- Reference: `wirepodxiaozhi/chipper/pkg/wirepod/ttr/xiaozhi_kg.go` (`sendSilencePrimeChunks`, Prepare-before-query)
- Prior experiments: `REVERT-tts-plan-c-getout-settle.md`, `REVERT-tts-prebuffer-getout.md`
