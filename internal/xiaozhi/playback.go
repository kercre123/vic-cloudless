package xiaozhi

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"

	"github.com/digital-dream-labs/vector-cloud/internal/log"
)

const (
	// PCMFilePath: growing PCM file for ExternalAudio.
	// Robot anim redirects Opus FIFO → this PCM path (Opus framing was desynced).
	PCMFilePath         = "/run/vic-cloud/xiaozhi_play.pcm"
	MetaFilePath        = "/data/data/com.anki.victor/cache/xiaozhi_play.meta"
	PlayFlagPath        = "/run/vic-cloud/xiaozhi-play"
	PlayFlagLegacy      = "/run/xiaozhi-play"
	StreamFlagPath      = "/run/vic-cloud/xiaozhi-stream"
	StreamFlagLegacy    = "/run/xiaozhi-stream"
	StreamEndFlagPath   = "/run/vic-cloud/xiaozhi-stream-end"
	StreamEndFlagLegacy = "/run/xiaozhi-stream-end"
	RelistenFlagPath    = "/run/vic-cloud/xiaozhi-relisten"
	RelistenPendingPath = "/run/vic-cloud/xiaozhi-relisten-pending"

	// Balanced window for 426MB Vector (2026-07-15 mitigate pack):
	// Between old ~8s/40k portal (LMK risk) and trial ~4s/20k (ALSA underrun/SIGILL).
	// Still streams long audio via punch+throttle; soft-capped in turn.go (~100s).
	pcmWindowAheadBytes = 192 * 1024 // ~6s @ 16kHz s16le
	pcmPunchKeepBytes   = 48 * 1024  // ~1.5s cushion
	pcmHardCapBytes          = 1024 * 1024 // ~32s unpunched live
	pcmHardCapPunchFailBytes = 3 * 1024 * 1024 // ~96s if punch unsupported

	SilencePrimeChunkBytes = 1024
	SilencePrimeChunks     = 3
	HeadPadSilenceMs       = 150
	HeadPadSilenceBytes    = 16000 * 2 * HeadPadSilenceMs / 1000
)

var (
	streamMu       sync.Mutex
	streamStartAt  time.Time
	pcmPunchedTo   int64
	pcmPunchFailed bool

	// ESP32-style: hold Opus only during JPEG capture (+ shutter). After tool
	// reply, stream analysis TTS normally — never CancelPlayback / oneshot.
	streamSuspend             atomic.Bool
	awaitPostCaptureStream    atomic.Bool
	postCaptureToolDone       atomic.Bool
	postCaptureResumeUnixNano atomic.Int64
	postCaptureToolDoneNano   atomic.Int64
)

func clearStreamFlags() {
	_ = os.Remove(StreamEndFlagPath)
	_ = os.Remove(StreamEndFlagLegacy)
	_ = os.Remove(StreamFlagPath)
	_ = os.Remove(StreamFlagLegacy)
	_ = os.Remove(PlayFlagPath)
	_ = os.Remove(PlayFlagLegacy)
}

// CleanupPlaybackFiles removes stream/oneshot artefacts after a turn.
func CleanupPlaybackFiles() {
	streamMu.Lock()
	streamStartAt = time.Time{}
	pcmPunchedTo = 0
	pcmPunchFailed = false
	streamMu.Unlock()
	_ = os.Remove(PCMFilePath)
	_ = os.Remove("/run/vic-cloud/xiaozhi_play.opus")
	clearStreamFlags()
}

// WritePCMFile writes decoded 16kHz PCM as a regular file (one-shot fallback).
func WritePCMFile(pcm []byte) error {
	if err := os.MkdirAll(filepath.Dir(MetaFilePath), 0777); err != nil {
		return fmt.Errorf("mkdir pcm: %w", err)
	}
	_ = os.Remove(PCMFilePath)
	if err := os.WriteFile(PCMFilePath, pcm, 0644); err != nil {
		return fmt.Errorf("write pcm: %w", err)
	}
	_ = os.WriteFile(MetaFilePath, []byte("rate=16000\n"), 0644)
	return nil
}

// RequestExternalAudioPlay asks anim to play the PCM file via SdkAudioComponent.PlayPCMFile.
func RequestExternalAudioPlay() error {
	return os.WriteFile(PlayFlagPath, []byte("1"), 0644)
}

func streamPrepareFile() error {
	clearStreamFlags()
	ClearCancelFlags()
	DisarmRelistenPending()
	_ = os.Remove("/run/vic-cloud/xiaozhi_play.opus")
	if err := os.MkdirAll(filepath.Dir(PCMFilePath), 0777); err != nil {
		return fmt.Errorf("mkdir pcm: %w", err)
	}
	if err := os.WriteFile(PCMFilePath, nil, 0644); err != nil {
		return fmt.Errorf("truncate pcm: %w", err)
	}
	_ = os.WriteFile(MetaFilePath, []byte("rate=16000\n"), 0644)
	streamMu.Lock()
	streamStartAt = time.Time{}
	pcmPunchedTo = 0
	pcmPunchFailed = false
	streamMu.Unlock()
	return nil
}

func raiseStreamFlag() error {
	if err := os.WriteFile(StreamFlagPath, []byte("1"), 0644); err != nil {
		return err
	}
	_ = os.WriteFile(StreamFlagLegacy, []byte("1"), 0644)
	return nil
}

// StreamStart truncates the PCM file and asks anim to begin ExternalAudio streaming.
func StreamStart() error {
	if err := streamPrepareFile(); err != nil {
		return err
	}
	return raiseStreamFlag()
}

// ErrPCMHardCap is returned when the on-disk PCM window would exceed pcmHardCapBytes.
var ErrPCMHardCap = fmt.Errorf("pcm hard cap reached")

func playbackBusyFile() bool {
	if _, err := os.Stat(BusyPath); err == nil {
		return true
	}
	if _, err := os.Stat("/run/xiaozhi-busy"); err == nil {
		return true
	}
	return false
}

// SuspendPlaybackForCapture soft-ends intro ASAP so tools/call finishes before
// the Xiaozhi server notifications/cancelled timeout (~10s).
func SuspendPlaybackForCapture() {
	awaitPostCaptureStream.Store(false)
	postCaptureToolDone.Store(false)
	postCaptureToolDoneNano.Store(0)

	streamSuspend.Store(true)

	if playbackBusyFile() {
		log.Println("[Xiaozhi] analyze_photo: soft StreamEnd before camera")
		_ = StreamEnd()
		time.Sleep(250 * time.Millisecond)
	} else {
		// Brief arm if intro TTS just starting.
		armDeadline := time.Now().Add(800 * time.Millisecond)
		for time.Now().Before(armDeadline) {
			if playbackBusyFile() {
				log.Println("[Xiaozhi] analyze_photo: soft StreamEnd before camera")
				_ = StreamEnd()
				time.Sleep(250 * time.Millisecond)
				break
			}
			time.Sleep(40 * time.Millisecond)
		}
	}
	CleanupPlaybackFiles()
	ClearCancelFlags()
	_ = os.Remove(BusyPath)
	_ = os.Remove("/run/xiaozhi-busy")
	SetPlaying(false)
	time.Sleep(120 * time.Millisecond)
}

// ResumePlaybackAfterCapture frees the speaker; Opus stays dropped until the
// MCP tool reply is ready, then turn.go streams analysis TTS (no oneshot).
func ResumePlaybackAfterCapture() {
	ClearCancelFlags()
	CleanupPlaybackFiles()
	streamSuspend.Store(false)
	awaitPostCaptureStream.Store(true)
	postCaptureToolDone.Store(false)
	postCaptureToolDoneNano.Store(0)
	postCaptureResumeUnixNano.Store(time.Now().UnixNano())
	log.Println("[Xiaozhi] analyze_photo: resume — stream analysis after tool reply")
	time.Sleep(300 * time.Millisecond)
}

// PreferOneshotAfterCapture always false — ESP32-style stream only (oneshot
// PlayPCMFile bloated anim RAM and triggered yellow fault countdown).
func PreferOneshotAfterCapture() bool {
	return false
}

// ClearPostCaptureOneshot is a no-op kept for call-site compatibility.
func ClearPostCaptureOneshot() {}

// MarkPostCaptureToolDone allows analysis Opus after the vision result is ready.
// Do NOT drain audioCh — analysis frames may already be queued (ESP32 keeps them).
func MarkPostCaptureToolDone() {
	if postCaptureToolDone.Swap(true) {
		return
	}
	postCaptureToolDoneNano.Store(time.Now().UnixNano())
	log.Println("[Xiaozhi] analyze_photo: tool reply ready — stream analysis TTS")
}

// PostCaptureToolDone is true after MarkPostCaptureToolDone.
func PostCaptureToolDone() bool {
	return postCaptureToolDone.Load()
}

// IgnorePrematureTTSStop ignores intro/stop until analysis playback is armed.
// Must stay true after MarkPostCaptureToolDone: a late stop from Soft StreamEnd
// otherwise ends the turn and analysis Opus never plays (silent hang).
func IgnorePrematureTTSStop() bool {
	return streamSuspend.Load() || awaitPostCaptureStream.Load()
}

// DropPostCaptureOpus drops Opus only while camera capture holds the speaker.
// Do not gate on toolDone — analysis frames can arrive in the same instant as
// sendResult returns and were dropped (silent hang after tool reply ready).
func DropPostCaptureOpus() bool {
	return streamSuspend.Load()
}

// AwaitPostCaptureStream is true after camera until analysis stream is armed.
func AwaitPostCaptureStream() bool {
	return awaitPostCaptureStream.Load()
}

// ClearAwaitPostCaptureStream marks post-camera wait done.
func ClearAwaitPostCaptureStream() {
	awaitPostCaptureStream.Store(false)
	postCaptureToolDone.Store(false)
	postCaptureToolDoneNano.Store(0)
}

// AbandonPostCaptureAwait hard-clears post-camera wait so a hung analyze_photo
// turn cannot leave speaker/busy/PCM state open and drain RAM into fault countdown.
func AbandonPostCaptureAwait(reason string) {
	streamSuspend.Store(false)
	awaitPostCaptureStream.Store(false)
	postCaptureToolDone.Store(false)
	postCaptureToolDoneNano.Store(0)
	postCaptureResumeUnixNano.Store(0)
	ClearCancelFlags()
	CleanupPlaybackFiles()
	_ = os.Remove(BusyPath)
	_ = os.Remove("/run/xiaozhi-busy")
	SetPlaying(false)
	log.Println("[Xiaozhi] analyze_photo: abandoned post-camera await —", reason)
}

// PostCaptureAwaitTimedOut reports hanging forever waiting for analysis audio.
func PostCaptureAwaitTimedOut(d time.Duration) bool {
	if !awaitPostCaptureStream.Load() {
		return false
	}
	ns := postCaptureToolDoneNano.Load()
	if ns == 0 {
		ns = postCaptureResumeUnixNano.Load()
	}
	if ns == 0 {
		return false
	}
	return time.Since(time.Unix(0, ns)) > d
}

// StreamAppendSuspended is true while camera capture holds the speaker.
func StreamAppendSuspended() bool {
	return streamSuspend.Load()
}

// StreamAppend appends decoded 16kHz s16le PCM and maintains a sliding tmpfs window.
func StreamAppend(pcm []byte) (fileSize int64, err error) {
	if len(pcm) == 0 {
		return PCMFileSize(), nil
	}
	if streamSuspend.Load() {
		// Drop late intro/frames during camera — avoids fighting ALSA.
		return PCMFileSize(), nil
	}

	// Throttle if we are more than pcmWindowAheadBytes ahead of estimated playhead.
	throttleForWindow(int64(len(pcm)))

	f, err := os.OpenFile(PCMFilePath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0644)
	if err != nil {
		return -1, fmt.Errorf("open pcm append: %w", err)
	}
	if _, err := f.Write(pcm); err != nil {
		_ = f.Close()
		return -1, fmt.Errorf("append pcm: %w", err)
	}
	st, serr := f.Stat()
	_ = f.Close()
	if serr != nil {
		return -1, nil
	}
	sz := st.Size()

	streamMu.Lock()
	if streamStartAt.IsZero() {
		streamStartAt = time.Now()
	}
	streamMu.Unlock()

	punchPlayedPCM(sz)

	streamMu.Lock()
	punched := pcmPunchedTo
	punchFail := pcmPunchFailed
	streamMu.Unlock()

	live := sz - punched
	if live < 0 {
		live = sz
	}
	if punchFail {
		if sz > pcmHardCapPunchFailBytes {
			log.Printf("[Xiaozhi][TTS] PCM hard cap %d bytes (punch unsupported, size=%d) — aborting long stream",
				pcmHardCapPunchFailBytes, sz)
			return sz, ErrPCMHardCap
		}
	} else if live > pcmHardCapBytes {
		log.Printf("[Xiaozhi][TTS] PCM live-window hard cap %d (live=%d size=%d punched=%d) — aborting",
			pcmHardCapBytes, live, sz, punched)
		return sz, ErrPCMHardCap
	}
	return sz, nil
}

func estimatedPlayhead() int64 {
	streamMu.Lock()
	defer streamMu.Unlock()
	return estimatedPlayheadLocked()
}

func estimatedPlayheadLocked() int64 {
	if streamStartAt.IsZero() {
		return 0
	}
	// Speaker starts after getout settle (~0.45–1.5s) + ~0.55s speech buffer.
	// Too-small lag makes punch-hole zero unread PCM → silent TTS + Wwise starve.
	elapsed := time.Since(streamStartAt) - 2000*time.Millisecond
	if elapsed < 0 {
		return 0
	}
	return int64(elapsed/time.Millisecond) * 32
}

func throttleForWindow(incoming int64) {
	// Do not throttle until streamStartAt is set (first write). A leftover large
	// PCM file before prepare/start must not stall forever with playhead=0.
	streamMu.Lock()
	started := !streamStartAt.IsZero()
	streamMu.Unlock()
	if !started {
		return
	}
	for {
		sz := PCMFileSize()
		if sz < 0 {
			return
		}
		ahead := sz + incoming - estimatedPlayhead()
		if ahead <= pcmWindowAheadBytes {
			return
		}
		extra := ahead - pcmWindowAheadBytes
		sleepMs := extra / 32
		if sleepMs < 20 {
			sleepMs = 20
		}
		if sleepMs > 250 {
			sleepMs = 250
		}
		time.Sleep(time.Duration(sleepMs) * time.Millisecond)
	}
}

// punchPlayedPCM frees already-played PCM in tmpfs via fallocate PUNCH_HOLE.
// Anim only reads forward via absolute offset, so punched ranges behind the
// playhead are never re-read.
func punchPlayedPCM(fileSize int64) {
	streamMu.Lock()
	if pcmPunchFailed {
		streamMu.Unlock()
		return
	}
	playhead := estimatedPlayheadLocked()
	punchTo := playhead - pcmPunchKeepBytes
	if punchTo < 4096 || punchTo <= pcmPunchedTo {
		streamMu.Unlock()
		return
	}
	punchTo &^= 4095 // page-align
	from := pcmPunchedTo
	streamMu.Unlock()

	if punchTo-from < 48*1024 {
		return // batch punches
	}
	if punchTo > fileSize {
		punchTo = fileSize &^ 4095
	}
	if punchTo <= from {
		return
	}

	f, err := os.OpenFile(PCMFilePath, os.O_RDWR, 0)
	if err != nil {
		return
	}
	err = unix.Fallocate(int(f.Fd()), unix.FALLOC_FL_PUNCH_HOLE|unix.FALLOC_FL_KEEP_SIZE, from, punchTo-from)
	_ = f.Close()
	if err != nil {
		streamMu.Lock()
		pcmPunchFailed = true
		streamMu.Unlock()
		log.Println("[Xiaozhi][TTS] PCM punch-hole unsupported — window relies on throttle only:", err)
		return
	}
	streamMu.Lock()
	pcmPunchedTo = punchTo
	streamMu.Unlock()
}

// PCMFileSize returns current size of the streaming PCM file, or -1.
func PCMFileSize() int64 {
	st, err := os.Stat(PCMFilePath)
	if err != nil {
		return -1
	}
	return st.Size()
}

// StreamEnd syncs the PCM file and sets the stream-end flag.
func StreamEnd() error {
	if f, err := os.OpenFile(PCMFilePath, os.O_RDWR, 0644); err == nil {
		_ = f.Sync()
		_ = f.Close()
	}
	if err := os.WriteFile(StreamEndFlagPath, []byte("1"), 0644); err != nil {
		return err
	}
	_ = os.WriteFile(StreamEndFlagLegacy, []byte("1"), 0644)
	return nil
}

// ArmRelistenAfterPlay asks anim to FakeTriggerWord after ExternalAudio completes.
func ArmRelistenAfterPlay() error {
	return os.WriteFile(RelistenPendingPath, []byte("1"), 0644)
}

// DisarmRelistenPending drops anim auto-relisten so cloud can fire one deliberate TriggerRelisten.
func DisarmRelistenPending() {
	_ = os.Remove(RelistenPendingPath)
	_ = os.Remove("/run/xiaozhi-relisten-pending")
	_ = os.Remove(RelistenFlagPath)
	_ = os.Remove("/run/xiaozhi-relisten")
}

// TriggerRelisten writes the relisten flag for continuous conversation.
func TriggerRelisten() error {
	if InBlackjackGameMode() {
		return nil
	}
	DisarmRelistenPending()
	if err := os.WriteFile(RelistenFlagPath, []byte("1"), 0644); err != nil {
		return err
	}
	_ = os.WriteFile("/run/xiaozhi-relisten", []byte("1"), 0644)
	return nil
}

// ArmStreamWithSilencePrime prepares PCM with silence prime + head pad, THEN raises
// the stream flag so ExternalAudio does not open on an empty file.
func ArmStreamWithSilencePrime() (primeBytes int, err error) {
	if err := streamPrepareFile(); err != nil {
		return 0, err
	}
	silence := make([]byte, SilencePrimeChunkBytes)
	for i := 0; i < SilencePrimeChunks; i++ {
		if _, err := StreamAppend(silence); err != nil {
			return primeBytes, fmt.Errorf("silence prime %d: %w", i+1, err)
		}
		primeBytes += SilencePrimeChunkBytes
	}
	if HeadPadSilenceBytes > 0 {
		pad := make([]byte, HeadPadSilenceBytes)
		if _, err := StreamAppend(pad); err != nil {
			return primeBytes, fmt.Errorf("head pad silence: %w", err)
		}
		primeBytes += HeadPadSilenceBytes
	}
	if err := raiseStreamFlag(); err != nil {
		return primeBytes, err
	}
	return primeBytes, nil
}
