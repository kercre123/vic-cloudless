# TTS Plan C — getout settle then speak (2026-07-13)

## Status: **ACTIVE** (experiment)

Hold ExternalAudio `PostAudioEvent` ~**1.5s** while PCM buffers from offset 0, so `VC_ListeningGetOut` can finish. Then start speaker from the **head** of the portal (intentional delay, not missing syllables).

Marker: `TTS_PLAN_C_2026-07-13`

## What changed

| File | Change |
|------|--------|
| `sdkAudioComponent.cpp` | `_streamHoldForGetout` ~1.5s before Post; latch after settle 0.20s; no StopAll Animation at StartStreamPCM |
| `sdkAudioComponent.h` | hold state + `IsStreamSpeakerPosted()` |
| `animEngine.cpp` | no Abort getout on stream start; Abort getout only **after** speaker posted |
| `turn.go` | `releasing listen UI (after_stt_getout_settle)` at STT so getout starts early |

## Bugfix (same day)

Early-open when `!getoutActive` after 250ms opened the speaker **before** ListeningGetOut started (intent→engine lag). Getout then ducked the head again.

**Fix:** only early-open after `_streamSawGetout && !active`, else wait full `STREAM_GETOUT_SETTLE_SEC` (1.5s).


Cloud:

```text
[Xiaozhi] releasing listen UI (after_stt_getout_settle)
[Xiaozhi] TTS audio FIRST frame — buffering PCM (speaker after getout settle) ...
```

Anim:

```text
StartStreamPCM: ... (hold speaker ~1500ms for getout settle)
FeedStreamPcm: Getout settle done (1500ms) — arming ExternalAudio
FeedStreamPcm: Speaker started after settle + ~200ms buffer
```

You may hear ~1.5–1.7s silence after STT, then **full** first sentence.

## Revert

### 1. Anim constants / hold (`sdkAudioComponent.cpp`)

Restore roughly:

```cpp
#define STREAM_AUDIO_TO_BEGIN_PLAYING_SEC (0.55f)
// remove STREAM_GETOUT_SETTLE_SEC and _streamHoldForGetout gate in tryStartSpeaker
```

`StartStreamPCM`: optional StopAll Animation again (pre-C head fix), remove hold start.
`Update`: restore prior StopAll-during-head behavior if desired.
Remove `#include <chrono>` / hold members from `.h` if unused.
Remove `IsStreamSpeakerPosted()`.

### 2. `animEngine.cpp`

Restore Abort getout on stream flag / while `xiaozhi-busy` (pre-C), remove `IsStreamSpeakerPosted` gate.

### 3. Cloud `turn.go`

Defer listen UI to first TTS again (or keep at STT without anim hold — will duck head again).

### 4. Build / deploy

```bash
cd /home/linh/Projects/wire-os/anki/victor
sed -i 's|/usr/bin/ccache|/home/linh/.local/bin/ccache|g' _build/vicos/Release/launch-c _build/vicos/Release/launch-cxx
ninja -C _build/vicos/Release vic-anim

cd /home/linh/Projects/wire-os/anki/vic-cloudless && make vic-cloud

KEY=.../ssh_root_key
ROBOT=root@192.168.100.247
scp -i "$KEY" .../vic-anim .../vic-cloud "$ROBOT":/data/local/
ssh -i "$KEY" "$ROBOT" 'mount -o remount,rw /; systemctl stop vic-cloud; systemctl stop vic-anim; sleep 2; cp /data/local/vic-anim /anki/bin/vic-anim; cp /data/local/vic-cloud /anki/bin/vic-cloud; chmod 755 /anki/bin/vic-anim /anki/bin/vic-cloud; sync; systemctl start vic-anim; sleep 4; systemctl start vic-cloud; sleep 2; systemctl restart vic-switchboard'
```

## Related

- `REVERT-tts-esp32-p0.md` — P0 skip-getout **reverted** (worse)
- `REVERT-tts-prebuffer-getout.md` — earlier StopAll / prebuffer notes
