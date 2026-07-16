package xiaozhi

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"

	"golang.org/x/sys/unix"

	"github.com/digital-dream-labs/vector-cloud/internal/log"
)

// Plan B: play Xiaozhi TTS via ALSA MultiMedia2 from vic-cloud, bypassing
// ExternalAudio / Wwise in vic-anim. Goal: keep anim RSS flat during talk.
//
// Device: hw:0,1 (pcmC0D1p) — MultiMedia1 stays with Wwise.
// Format: 16 kHz mono s16le (matches Opus decode).
//
// Default ON. Set XIAOZHI_ALSA=0 to restore ExternalAudio.

const (
	alsaFIFOPath   = "/run/vic-cloud/xiaozhi_alsa.fifo"
	alsaCard       = "0"
	alsaDevice     = "1"
	alsaRate       = "16000"
	alsaChannels   = "1"
	alsaPeriod     = "1024"
	alsaMixerCtl   = "PRI_MI2S_RX Audio Mixer MultiMedia2"
)

var (
	alsaMu     sync.Mutex
	alsaCmd    *exec.Cmd
	alsaWriter *os.File
	alsaBytes  int64
	alsaReady  atomic.Bool
	mixerReady atomic.Bool
)

// UseAlsaPlayback reports whether Xiaozhi TTS should go to ALSA (Plan B).
func UseAlsaPlayback() bool {
	v := os.Getenv("XIAOZHI_ALSA")
	if v == "" {
		return true
	}
	return v != "0" && v != "false" && v != "off" && v != "no"
}

func ensureMM2Mixer() {
	if mixerReady.Load() {
		return
	}
	cmd := exec.Command("tinymix", "set", alsaMixerCtl, "1")
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("[Xiaozhi][ALSA] tinymix MM2 enable failed: %v — %s", err, string(out))
		return
	}
	mixerReady.Store(true)
	log.Println("[Xiaozhi][ALSA] MultiMedia2 mixer enabled")
}

func alsaKillLocked() {
	if alsaWriter != nil {
		_ = alsaWriter.Close()
		alsaWriter = nil
	}
	if alsaCmd != nil && alsaCmd.Process != nil {
		_ = alsaCmd.Process.Kill()
		_, _ = alsaCmd.Process.Wait()
	}
	alsaCmd = nil
	alsaBytes = 0
	alsaReady.Store(false)
	_ = os.Remove(alsaFIFOPath)
}

// AlsaCancel stops ALSA playback immediately (barge-in / cleanup).
func AlsaCancel() {
	if !UseAlsaPlayback() {
		return
	}
	alsaMu.Lock()
	defer alsaMu.Unlock()
	alsaKillLocked()
}

func alsaStart() error {
	alsaMu.Lock()
	defer alsaMu.Unlock()
	alsaKillLocked()
	ensureMM2Mixer()

	if err := os.MkdirAll(filepath.Dir(alsaFIFOPath), 0775); err != nil {
		return fmt.Errorf("alsa mkdir: %w", err)
	}
	_ = os.Remove(alsaFIFOPath)
	if err := unix.Mkfifo(alsaFIFOPath, 0666); err != nil {
		return fmt.Errorf("alsa mkfifo: %w", err)
	}

	// O_RDWR so open does not block waiting for tinyplay's read end.
	w, err := os.OpenFile(alsaFIFOPath, os.O_RDWR, 0)
	if err != nil {
		_ = os.Remove(alsaFIFOPath)
		return fmt.Errorf("alsa open fifo: %w", err)
	}

	cmd := exec.Command("tinyplay", alsaFIFOPath,
		"-D", alsaCard, "-d", alsaDevice,
		"-i", "raw", "-c", alsaChannels, "-r", alsaRate, "-p", alsaPeriod)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		_ = w.Close()
		_ = os.Remove(alsaFIFOPath)
		return fmt.Errorf("tinyplay start: %w", err)
	}

	alsaCmd = cmd
	alsaWriter = w
	alsaBytes = 0
	alsaReady.Store(true)
	log.Println("[Xiaozhi][ALSA] tinyplay started (MM2 16k mono)")
	return nil
}

func alsaWrite(pcm []byte) (written int64, err error) {
	if len(pcm) == 0 {
		alsaMu.Lock()
		n := alsaBytes
		alsaMu.Unlock()
		return n, nil
	}
	alsaMu.Lock()
	w := alsaWriter
	alsaMu.Unlock()
	if w == nil {
		return -1, fmt.Errorf("alsa not started")
	}
	// ALSA backpressure: Write blocks when the period buffer is full.
	n, err := w.Write(pcm)
	alsaMu.Lock()
	alsaBytes += int64(n)
	total := alsaBytes
	alsaMu.Unlock()
	if err != nil {
		return total, fmt.Errorf("alsa write: %w", err)
	}
	return total, nil
}

func alsaBytesWritten() int64 {
	alsaMu.Lock()
	defer alsaMu.Unlock()
	return alsaBytes
}

// alsaEnd closes the FIFO so tinyplay drains remaining PCM. Does not block —
// WaitPlaybackIdle / AlsaCancel observe AlsaActive.
func alsaEnd() error {
	alsaMu.Lock()
	w := alsaWriter
	alsaWriter = nil
	cmd := alsaCmd
	alsaMu.Unlock()

	if w != nil {
		_ = w.Close()
	}
	if cmd == nil {
		alsaReady.Store(false)
		_ = os.Remove(alsaFIFOPath)
		return nil
	}

	go func() {
		_ = cmd.Wait()
		alsaMu.Lock()
		if alsaCmd == cmd {
			alsaCmd = nil
		}
		alsaMu.Unlock()
		alsaReady.Store(false)
		_ = os.Remove(alsaFIFOPath)
		log.Println("[Xiaozhi][ALSA] tinyplay finished")
	}()
	return nil
}

// AlsaActive is true while tinyplay is running.
func AlsaActive() bool {
	return alsaReady.Load()
}
