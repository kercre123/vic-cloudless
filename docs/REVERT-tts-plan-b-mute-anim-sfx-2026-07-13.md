# REVERT — Plan B: mute Animation/Behavior SFX during Xiaozhi TTS head (2026-07-13)

Goal: stop ListeningGetOut / Animation **SFX** from ducking the start of ExternalAudio TTS.  
**Does not** abort face animation — only `StopAllAudioEvents` on Animation + Behavior buses.

Marker: `TTS_PLAN_B_MUTE_SFX_2026-07-13`

## Behavior

| When | Action |
|------|--------|
| `StartStreamPCM` | StopAll Animation + Behavior once |
| Every `Update` while stream active | Keep muting Animation + Behavior until speaker has been posted **and** ~2s of protect window elapsed |
| After protect window | Stop continuous mute (normal anim SFX can play again) |

Face getout anim can still run; its **audio** is silenced.

## Files touched

| File | Change |
|------|--------|
| `sdkAudioComponent.cpp` | `STREAM_PROTECT_HEAD_SEC`; continuous mute in `Update`; mute at `StartStreamPCM` |
| `sdkAudioComponent.h` | `_streamPostStart` |

## How to test

1. Wake + ask for a multi-sentence answer.
2. Hear first 1–2 sentences matching `TTS FULL TEXT FROM SERVER` / `TTS sentence #1`.
3. Getout face may still play; getout **sound** should be quiet during TTS head.
4. Anim log may show repeated mute only via dbg if added; cloud still logs full text.

## Revert

```bash
cd /home/linh/Projects/wire-os

# Restore these two files (git or manual undo of Plan B hunks):
#   anki/victor/animProcess/src/cozmoAnim/audio/sdkAudioComponent.cpp
#   anki/victor/animProcess/src/cozmoAnim/audio/sdkAudioComponent.h

# Manual undo:
# - Remove STREAM_PROTECT_HEAD_SEC and TTS_PLAN_B block in Update()
# - Restore Update() to mute Animation/Behavior only once after Post (_streamSilencedAnim)
# - Restore StartStreamPCM Animation StopAll only when _skipGetoutHoldNext
# - Remove _streamPostStart from .h

sed -i 's|/usr/bin/ccache|/home/linh/.local/bin/ccache|g' \
  anki/victor/_build/vicos/Release/launch-c \
  anki/victor/_build/vicos/Release/launch-cxx
ninja -C anki/victor/_build/vicos/Release vic-anim

# Deploy vic-anim → /anki/bin/vic-anim (stop/start vic-anim)
```

Robot: `192.168.100.247`, key `anki/vic-cloudless/ssh_root_key`.

## Related

- Plan A head pad (still active): `REVERT-tts-head-silence-pad-A-2026-07-13.md`
- Early-arm: `REVERT-tts-early-arm-silence-prime-2026-07-13.md`
- Next if B fails: Plan C (skip/abort ListeningGetOut) or D (wirepod SendText order)
