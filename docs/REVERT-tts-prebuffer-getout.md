# TTS first-chunk fixes (2026-07-13)

## Status

| Attempt | What | Result |
|---------|------|--------|
| Cloud prebuffer + 900ms getout settle | **REVERTED** | still missed head |
| One-shot `StopAllAudioEvents` at StartStreamPCM / PostAudioEvent | **partial** | audio exists but head still delayed/missing |
| **ACTIVE:** defer listen-UI + short latch + keep killing getout | see below | |

## Root cause

Cloud receives/logs first TTS sentence and writes PCM. After STT, `intent_system_noaudio` starts **`anim_wakeword_groggyeyes_getout_01`** with ActiveAudioEvent. That Animation-GO SFX ducks ExternalAudio while Wwise still consumes the portal → first syllables play into silence (feels like missing chunks + delay). One-shot StopAll loses the race when getout keyframes post *after* stream start.

## Active fix

### Cloud (`turn.go`)

- Do **not** call `afterSTT` at STT time.
- Call it on **first TTS audio** (after `StreamStart`), or after **8s** fallback / turn end.
- Order: `StreamStart` → then release listen UI (getout).

### Anim (`sdkAudioComponent.cpp` + `animEngine.cpp`)

- `STREAM_AUDIO_TO_BEGIN_PLAYING_SEC` **0.12** (was 0.55).
- Every `Update` while streaming: `StopAllAudioEvents(Animation|Behavior)` until **1.5s** of TTS frames played (never stop TextToSpeech after post — that is Xiaozhi’s GO).
- On `xiaozhi-stream` flag: Abort competing wakeword/getout anim.
- While `xiaozhi-busy`: Abort any late `*getout*` anim.

Marker: `TTS_HEAD_FIX_2026-07-13`

## Revert

```bash
# Cloud: restore immediate afterSTT after STT in turn.go
# Anim: remove Abort blocks in animEngine.cpp; restore STREAM_AUDIO_TO_BEGIN 0.55f;
#       remove Update() StopAll loop and StartStreamPCM/PostAudioEvent StopAll blocks

cd /home/linh/Projects/wire-os/anki/victor
sed -i 's|/usr/bin/ccache|/home/linh/.local/bin/ccache|g' _build/vicos/Release/launch-c _build/vicos/Release/launch-cxx
ninja -C _build/vicos/Release vic-anim

cd /home/linh/Projects/wire-os/anki/vic-cloudless && make vic-cloud

KEY=/home/linh/Projects/wire-os/anki/vic-cloudless/ssh_root_key
ROBOT=root@192.168.100.247
scp -i "$KEY" _build/vicos/Release/bin/vic-anim "$ROBOT":/data/local/vic-anim
# from vic-cloudless:
scp -i "$KEY" build/vic-cloud "$ROBOT":/data/local/vic-cloud
ssh -i "$KEY" "$ROBOT" 'mount -o remount,rw /; systemctl stop vic-cloud; systemctl stop vic-anim; sleep 2; cp /data/local/vic-anim /anki/bin/vic-anim; cp /data/local/vic-cloud /anki/bin/vic-cloud; chmod 755 /anki/bin/vic-anim /anki/bin/vic-cloud; sync; systemctl start vic-anim; sleep 4; systemctl start vic-cloud; sleep 2; systemctl restart vic-switchboard'
```

## Success check

Cloud:

```text
[Xiaozhi] TTS audio FIRST frame ...
[Xiaozhi] releasing listen UI (first_tts_audio)
[Xiaozhi] TTS sentence: <opening line>
```

Anim (optional):

```text
Aborting competing anim ... / Aborting late getout ...
SdkAudioComponent.FeedStreamPcm: Speaker started after ~120ms buffer
```

You should **hear** the first logged TTS sentence, not only later lines.
