# Xiaozhi audio + mic state (snapshot 2026-07-13 ~02:05 ICT)

Trạng thái **code + robot** sau chuỗi fix Plan C / barge-in / bridge / switchboard.  
**Chưa ổn định cuối** — barge-in → TTS mới ra loa vẫn đang xác nhận sau deploy ~01:58.

Robot: `192.168.100.247` (Vector-B4X2)  
SSH: `anki/vic-cloudless/ssh_root_key`

Related revert docs:

- `REVERT-tts-plan-c-getout-settle.md` — Plan C (hold loa ~1.5s sau getout)
- `REVERT-tts-esp32-p0.md` — P0 skip-getout (**đã revert**)
- `REVERT-tts-prebuffer-getout.md` — prebuffer / StopAll sớm hơn

---

## 0. Verdict ngắn (02:05)

| Mục | Trạng thái |
|-----|------------|
| TTS live stream PCM + Plan C hold ~1.5s | Đang chạy (turn bình thường thường có loa) |
| Continuous mic / STT miss → relisten | Cố ý — mic có thể mở lại khi im lặng |
| Barge-in nút giữa TTS → TTS mới **im loa** | **Chưa đóng** — nhiều lớp race; đang P0 hard-reset |
| ESP32 barge-in ổn | Vì abort = reset DAC cùng process (xem §7) |
| Switchboard `operation not permitted` sau deploy | **Không phải bug TTS** — restart cloud thiếu switchboard |

Binaries trên robot (mtime gần nhất):

- `/anki/bin/vic-anim` ~01:58 — epoch Complete + skip-hold sau cancel + `xiaozhi-dbg`
- `/anki/bin/vic-cloud` ~01:52 — `turnGen`, abort **không** `CleanupPlaybackFiles`, ClearCancelFlags
- `/anki/bin/xiaozhi-play-bridge.sh` ~01:52 — **không còn move** stream/cancel/play/end

---

## 1. Đường phát TTS ra loa

### Pipeline

```text
Server Opus (WSS)
  → vic-cloud decode Opus→PCM 16kHz
  → append /run/vic-cloud/xiaozhi_play.pcm
  → flag /run/vic-cloud/xiaozhi-stream   (anim đọc trực tiếp; bridge KHÔNG move)
  → vic-anim SdkAudioComponent::StartStreamPCM
  → feed portal Wwise (ExternalAudio, GO TextToSpeech)
  → [HOLD getout ~1.5s]  — trừ khi vừa CancelPlayback (_skipGetoutHoldNext)
  → PostAudioEvent → loa
  → stream-end → drain → OnAudioCompleted → clear xiaozhi-busy
```

### Cloud (`vic-cloudless`)

| File | Hành vi |
|------|---------|
| `turn.go` | STT → `releasing listen UI (after_stt_getout_settle)`. TTS `StreamStart` + append. **Abort: không gọi CleanupPlaybackFiles** (tránh xóa PCM turn mới). |
| `session.go` | `turnGen` + Bind/Unbind/Finish gen-guard. BargeIn: abort WSS, cancel turn, xóa flag (cả legacy), `RequestCancelPlayback`, chờ busy, `ClearCancelFlags`. |
| `playback.go` | PCM `/run/vic-cloud/xiaozhi_play.pcm`; `StreamStart` clear flags + cancel rồi ghi stream; append `O_CREATE\|O_APPEND`. |
| `init.go` | `afterSTT` → `intent_system_noaudio`. Sau stream: `WaitPlaybackIdle` → `TriggerRelisten` (continuous). |

### Anim (`victor`)

| File | Hành vi |
|------|---------|
| `sdkAudioComponent.cpp` | Plan C hold `STREAM_GETOUT_SETTLE_SEC=1.50f`, buffer `0.20f`, portal max 40000. **`_playbackEpoch`**: Complete stale bỏ qua. **`_skipGetoutHoldNext`**: sau `CancelPlayback`, StartStreamPCM kế **không hold 1.5s** (post ~200ms PCM). `OnAudioCompleted` **không xóa** `xiaozhi_play.pcm`. Debug: `/run/vic-cloud/xiaozhi-dbg` (`stream_start_hold` / `stream_start_skip_hold` / `post_ok` / `cancel_ok` / `prepare_fail`). |
| `animEngine.cpp` | Stream trước cancel: cùng tick có cả hai → ưu tiên stream. Unlink **cả hai** path stream. Cancel thật → `CancelPlayback`. |
| Bridge | Chỉ mirror **busy + relisten**; xóa legacy stream/cancel/play thừa. Anim watch `/run/vic-cloud`. |

### Hằng số

```cpp
STREAM_AUDIO_TO_BEGIN_PLAYING_SEC  0.20f
STREAM_GETOUT_SETTLE_SEC           1.50f   // bị skip nếu _skipGetoutHoldNext
STREAM_MAX_PORTAL_FRAMES           40000u
```

---

## 2. Mic / continuous

Continuous **bật** → nhiều path gọi `TriggerRelisten()` → FakeTrigger → mic mở lại không cần Hey Vector.

| Tình huống | Hành vi |
|------------|---------|
| TTS xong + playback idle | `WaitPlaybackIdle` → 400ms → `TriggerRelisten` |
| Timeout / empty STT | OnError + 600ms + **`TriggerRelisten`** (vòng im lặng) |
| Không mic uplink ~12s | `TriggerRelisten` |

Hotword: ignore khi đang uplink mic; **không** ignore sau `AudioDone` / chờ STT.  
Nút backpack FakeTrigger vẫn barge-in khi busy.

---

## 3. Barge-in — đã thử / còn mở

### Triệu chứng

Cloud vẫn log `TTS audio FIRST frame` / frames sau nút đánh thức, **loa không phát** (hoặc chỉ turn đầu sau boot ổn).

### Root causes đã xác định / đã vá một phần

1. Turn abort `CleanupPlaybackFiles` xóa PCM/flag turn mới → **gen-guard rồi bỏ hẳn cleanup trên abort**  
2. Anim cancel cùng tick unlink stream flag → **ưu tiên stream**  
3. Bridge `mv` cancel/stream TOCTOU → **bridge không move stream/cancel**  
4. Wwise Complete sau Stop xóa PCM / ClearOperationData turn mới → **`_playbackEpoch`** + không xóa PCM on Complete  
5. Ignore-cancel 2.5s **nuốt barge-in thật** (~3s) → portal starve → **đã bỏ ignore**; cancel luôn chạy  
6. Sau cancel vẫn Plan C hold + getout loạn → **`_skipGetoutHoldNext`** (~01:58)

### Chưa đóng

- User chưa confirm barge-in → TTS mới **có tiếng** ổn định trên robot sau 01:58 + switchboard sạch.  
- Plan tiếp (ESP32-like): mute getout SFX khi Xiaozhi TTS; hard-reset portal nếu `prepare_fail`; không append Opus turn đã cancel.

### Debug sau barge-in

```bash
cat /run/vic-cloud/xiaozhi-dbg
# Kỳ vọng: cancel_ok → stream_start_skip_hold → post_ok
```

Sim trên robot (01:58): stream → cancel → stream lại thấy `post_ok`.

---

## 4. Deploy — thứ tự bắt buộc

Sai thứ tự (chỉ restart `vic-cloud`) → spam:

```text
Couldn't create sockets ... _switchboard_gateway_server_ ... operation not permitted
```

Xiaozhi WSS vẫn chạy; intent/UI (getout) có thể hỏng → tưởng “fix TTS làm chết máy”.

**Đúng:**

```text
systemctl stop vic-cloud
systemctl restart vic-switchboard   # đợi ~2–3s
systemctl restart vic-anim          # nếu đổi anim; cẩn thận SSH
systemctl start vic-cloud
```

Hoặc: switchboard → cloud (anim giữ nếu không đổi).  
Sau deploy kiểm tra: `Sockets successfully created` **không** kèm EPERM lặp.

Build:

```bash
# cloud
cd anki/vic-cloudless && make vic-cloud
# anim
cd anki/victor && ninja -C _build/vicos/Release vic-anim
```

Copy: `scp` → `/anki/bin/` (anim: stop rồi `vic-anim.new` → `mv` nếu file bận).

---

## 5. So với xiaozhi-esp32

| | ESP32 | Vector (WireOS) |
|--|-------|-----------------|
| Abort | `AbortSpeaking` cùng process | Flag cancel + cloud turn cancel + Wwise Stop |
| Loa | I2S/DAC độc quyền | Wwise portal + Animation/Behavior GO |
| Buffer | RAM + ResetDecoder | File PCM + StreamingWavePortal |
| Sau nút | Listen → TTS ghi DAC ngay | Cần hard-reset portal + skip hold + không race Complete/flag |

“Cho loa về lúc mở máy” = cold-start ExternalAudio path, **không** phải chỉ xóa flag.

---

## 6. Flags runtime

| Path | Vai trò |
|------|---------|
| `/run/vic-cloud/xiaozhi_play.pcm` | PCM 16k s16le mono |
| `/run/vic-cloud/xiaozhi-stream` | Start stream (anim) |
| `/run/vic-cloud/xiaozhi-stream-end` | Producer xong |
| `/run/vic-cloud/xiaozhi-cancel` | Barge-in stop |
| `/run/vic-cloud/xiaozhi-busy` | Cloud/anim playback gate |
| `/run/vic-cloud/xiaozhi-dbg` | Anim debug one-liner |
| `/run/vic-cloud/xiaozhi-relisten*` | Continuous FakeTrigger |

Bridge service: `xiaozhi-play-bridge.service` — busy/relisten only.

---

## 7. Checklist xác nhận snapshot này

- [ ] Deploy xong: `Sockets successfully created`, không EPERM lặp
- [ ] Turn thường: có loa + log `after_stt_getout_settle` / `buffering PCM`
- [ ] Continuous: `relisten armed after playback idle` hoặc sau STT miss
- [ ] Barge-in: `xiaozhi-dbg` = `cancel_ok` → `stream_start_skip_hold` → `post_ok` **và nghe loa**
- [ ] `journalctl -u vic-anim` có thể thấy `No Data, plugin will be starved` lúc cancel cũ — sau skip-hold không nên im mãi

---

## 8. Việc tiếp theo (ưu tiên)

1. **Confirm barge-in có tiếng** sau stack sạch (switchboard OK).  
2. Nếu vẫn im: siết Clear portal trong `CancelPlayback`; log `prepare_fail`.  
3. Mute/skip **SFX** getout khi Xiaozhi busy / vừa cancel (giữ anim mặt).  
4. (Tuỳ chọn) Im lặng → nghỉ: bỏ `TriggerRelisten` sau STT timeout.
