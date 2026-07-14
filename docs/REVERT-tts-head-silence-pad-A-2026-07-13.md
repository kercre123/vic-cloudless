# REVERT — Plan A: TTS head silence pad (2026-07-13)

Goal: if getout/Wwise still ducks the start of Xiaozhi TTS after early-arm (steps 1–2), **prepend ~400ms of zeros before the first real PCM** so ducking eats silence instead of syllables.

Marker / log: `TTS head pad` / `head silence pad`

## Behavior

| Step | Change |
|------|--------|
| Cloud | Once per stream, before first real `StreamAppend`: write ~400ms zero PCM (`HeadPadSilenceBytes`) |
| Anim | No change (still Post after ~200ms non-silent speech; pad is silent so Post waits for real speech) |

Trade-off: audible delay ~0.4s before first syllable (plus existing getout settle).

## Files touched

| File | Change |
|------|--------|
| `internal/xiaozhi/playback.go` | `HeadPadSilenceMs` / `AppendHeadPadSilence()` |
| `internal/xiaozhi/turn.go` | Call pad once before first real append |

## How to test

1. Wake + short sentence — hear short silence then **full** first words matching `TTS FULL TEXT FROM SERVER`.
2. Log: `TTS head pad … ms silence before first real frame`.
3. Continuous second turn — pad again (once per stream arm).

## Revert

```bash
cd /home/linh/Projects/wire-os

# Remove AppendHeadPadSilence + constants from playback.go
# Remove headPadDone / AppendHeadPadSilence call from turn.go
# Or restore those two files from git / prior copy, then rebuild:

cd anki/vic-cloudless && make vic-cloud
# deploy build/vic-cloud → /anki/bin/vic-cloud (stop/start vic-cloud)
```

Manual undo:

1. Delete `HeadPadSilenceMs`, `HeadPadSilenceBytes`, `AppendHeadPadSilence` from `playback.go`.
2. In `turn.go`: remove `headPadDone` and the block that calls `AppendHeadPadSilence` before the first real `StreamAppend`.

## Related

- Early-arm + silence prime: `REVERT-tts-early-arm-silence-prime-2026-07-13.md`
- Plan C getout hold: `REVERT-tts-plan-c-getout-settle.md`
- Next if A fails: Plan B (mute Animation bus) or C (skip ListeningGetOut)
