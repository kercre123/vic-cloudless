# Xiaozhi / WireOS state snapshot — 2026-07-13 (~22:07 ICT)

Trạng thái **đang chạy ổn** trên robot sau chuỗi fix OTA cloudless + mất đầu TTS + first-wake im loa.

Robot tham chiếu: **Vector-T9D7** `192.168.100.45` (OTA `7d` + binary deploy tay).  
B4X2 `192.168.100.247` từng là máy dev TTS trước đó.

---

## 1. OTA cloudless (vic-cloud ~24MB)

### Vấn đề đã gặp
- Flash `4d`/`6d` “xong” nhưng `/anki/bin/vic-cloud` vẫn ~4.6MB.
- Nguyên nhân **không** phải config `/data` cũ.
- Trong image `devcloudless`, package `vic-cloudless` (~24MB) bị package **`victor`** đè bằng C++ `vic-cloud` ~4.6MB (cài sau theo alphabet).

### Fix recipe (lâu dài) — đã có trong tree
| File | Thay đổi |
|------|----------|
| `poky/victor/meta-anki/recipes/anki-robot/victor.bb` | Khi `CLOUDLESS=1`: `rm -f ${D}/anki/bin/vic-cloud` sau `do_install` |
| `poky/victor/meta-anki/recipes-products/images/apq8009/apq8009-anki-robot-image.inc` | Cài `vic-cloudless` **sau** `victor` |

### Build / verify
```bash
./build/build.sh -bt devcloudless -v <N>
# → _build/vicos-3.0.1.<N>d.ota

debugfs -R 'stat /anki/bin/vic-cloud' _build/apq8009-robot-sysfs.img
# Size phải ~24e6, không phải ~4.6e6
```

OTA đã xác nhận: **`vicos-3.0.1.7d.ota`** → robot có `vic-cloud` ~24MB + chuỗi Xiaozhi.

### Flash
- `reboot_after_install=0` → **phải `reboot` thủ công**.
- `/anki/etc/version` thường vẫn `3.0.1.0` → **không** dùng làm marker; dùng `ls -la /anki/bin/vic-cloud`.

---

## 2. Mất đầu TTS (nghe từ giữa câu)

### Root cause (đã chứng minh bằng TRACE)
Server gửi đủ Opus + `sentence_start` **sớm** (thường khi mic còn uplink / trước khi STT phase xong).

Code cũ **discard** `AudioCh` / `TTSCh` trong listen + STT wait, rồi `DrainEventChannels()` sau STT → **vứt đầu câu**.

Ví dụ: text *“Tao sẽ kể … cười nhé”* nhưng `CONSUME #1` không khớp `WSS RX #1`.

### Fix hiện tại (`turn.go`)
- **Không** discard TTS/audio lúc listen / chờ STT.
- **Không** `DrainEventChannels()` sau STT.
- Arm ExternalAudio khi `tts/start` hoặc first Opus; `releaseListenUI` **sau** khi stream đã armed.

---

## 3. Im loa / first Hey Vector không có tiếng

### Nguyên nhân
1. Raise flag `xiaozhi-stream` khi file PCM **còn trống** → anim `StartStreamPCM` trên file rỗng (nhất là lần wake đầu).
2. Hold getout ~1.5s + stream-end trước khi Post → có lúc `stream_done` không `post_ok`.

### Fix hiện tại
| Layer | Hành vi |
|-------|---------|
| `playback.go` | `ArmStreamWithSilencePrime`: ghi silence prime + head pad (~400ms) **rồi mới** `raiseStreamFlag` (cả `/run/vic-cloud/xiaozhi-stream` và legacy `/run/xiaozhi-stream`) |
| `turn.go` | `headPadDone=true` sau arm (không pad trùng); release listen UI sau arm |
| `sdkAudioComponent.cpp` | Skip getout hold nếu file đã `>8KB`; force Post nếu stream-end mà chưa Post nhưng đã có speech |

Debug file (khi cần): `/run/vic-cloud/xiaozhi-dbg`  
Kỳ vọng: `post_ok` / `post_ok_forced` / `stream_start_no_hold`.

---

## 4. Log hiện tại (đã gọn)

Chỉ còn text thoại:

```text
[Xiaozhi] STT: ...
[Xiaozhi] TTS: ...
```

(TRACE WSS/CONSUME đã tắt.)

Xem:
```bash
journalctl -u vic-cloud -n 50 --no-pager | grep Xiaozhi
```

---

## 5. Pipeline TTS hiện tại (tóm tắt)

```text
Server WSS (stt / tts sentence_start / binary Opus)
  → vic-cloud decode Opus→PCM 16kHz
  → Arm: prime+pad → flag stream
  → append /run/vic-cloud/xiaozhi_play.pcm
  → vic-anim SdkAudioComponent::StartStreamPCM
  → (optional short hold) Post ExternalAudio → loa
  → stream-end → drain → relisten (continuous)
```

---

## 6. File chính đã đụng

**Cloudless / OTA**
- `poky/.../victor.bb`
- `poky/.../apq8009-anki-robot-image.inc`
- `poky/.../vic-cloudless.bb`

**TTS / playback**
- `anki/vic-cloudless/internal/xiaozhi/turn.go`
- `anki/vic-cloudless/internal/xiaozhi/playback.go`
- `anki/vic-cloudless/internal/xiaozhi/client.go` (buffer audio 1024; không TRACE)
- `anki/victor/animProcess/.../sdkAudioComponent.cpp` (+ `.h` nếu có members hold)

**Related revert / lịch sử thí nghiệm** (không nhất thiết active):  
`docs/REVERT-tts-*.md`, `docs/XIAOZHI-AUDIO-MIC-STATE-2026-07-13.md`

---

## 7. Deploy tay (khi không flash full OTA)

```bash
# PC
scp -i ssh_root_key build/vic-cloud root@<IP>:/data/local/vic-cloud.new
# + vic-anim nếu đổi anim

# Robot
mount -o remount,rw /
systemctl stop vic-cloud vic-anim
cp -f /data/local/vic-cloud.new /anki/bin/vic-cloud
# cp vic-anim nếu có
chmod 755 /anki/bin/vic-cloud
systemctl start vic-anim; systemctl start vic-cloud
systemctl restart vic-switchboard
```

SSH OpenSSH mới (Windows/WSL): thêm  
`-o PubkeyAcceptedAlgorithms=+ssh-rsa -o HostKeyAlgorithms=+ssh-rsa`  
Key trên `/mnt/c/...` cần `chmod 600` bản copy trong home Linux.

---

## 8. Checklist “ổn”

- [x] `/anki/bin/vic-cloud` ~24MB, có Xiaozhi  
- [x] STT/TTS text đủ từ đầu câu (không discard sớm)  
- [x] Loa có tiếng (kể cả Hey Vector lần đầu sau reboot)  
- [x] Log gọn `STT:` / `TTS:`  

---

*Snapshot ngày 2026-07-13. Nếu đổi pipeline TTS/OTA, cập nhật file này hoặc thêm snapshot mới.*
