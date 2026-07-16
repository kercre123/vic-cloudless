package xiaozhi

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

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
//
// Writes go through a queue so the TTS select loop is not blocked by realtime
// ALSA backpressure (that race let TTSStateStop close the FIFO while Opus was
// still queued → speech cut mid-sentence).

const (
	alsaFIFOPath = "/run/vic-cloud/xiaozhi_alsa.fifo"
	alsaCard     = "0"
	alsaDevice   = "1"
	alsaRate     = "16000"
	alsaChannels = "1"
	alsaPeriod   = "1024"
	alsaMixerCtl = "PRI_MI2S_RX Audio Mixer MultiMedia2"
	alsaQueueCap = 256
)

var (
	alsaMu     sync.Mutex
	alsaCmd    *exec.Cmd
	alsaWriter *os.File
	alsaPCMCh  chan []byte
	alsaDone   chan struct{}
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
	ch := alsaPCMCh
	alsaPCMCh = nil
	if ch != nil {
		func() {
			defer func() { _ = recover() }()
			close(ch)
		}()
	}
	if alsaCmd != nil && alsaCmd.Process != nil {
		_ = alsaCmd.Process.Kill()
	}
	// Pump owns Wait() and writer Close(); just wait for it to finish.
	done := alsaDone
	if done != nil {
		alsaMu.Unlock()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		alsaMu.Lock()
	}
	alsaCmd = nil
	alsaWriter = nil
	alsaDone = nil
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

	pcmCh := make(chan []byte, alsaQueueCap)
	done := make(chan struct{})
	alsaCmd = cmd
	alsaWriter = w
	alsaPCMCh = pcmCh
	alsaDone = done
	alsaBytes = 0
	alsaReady.Store(true)

	go alsaPump(w, pcmCh, done, cmd)

	log.Println("[Xiaozhi][ALSA] tinyplay started (MM2 16k mono)")
	return nil
}

func alsaPump(w *os.File, pcmCh <-chan []byte, done chan struct{}, cmd *exec.Cmd) {
	defer func() {
		_ = w.Close()
		_, _ = cmd.Process.Wait()
		alsaMu.Lock()
		if alsaCmd == cmd {
			alsaCmd = nil
			alsaWriter = nil
			alsaPCMCh = nil
			alsaDone = nil
		}
		alsaMu.Unlock()
		alsaReady.Store(false)
		_ = os.Remove(alsaFIFOPath)
		close(done)
		log.Println("[Xiaozhi][ALSA] tinyplay finished")
		_ = os.Remove(BusyPath)
		_ = os.Remove("/run/xiaozhi-busy")
	}()

	for pcm := range pcmCh {
		if len(pcm) == 0 {
			continue
		}
		if _, err := w.Write(pcm); err != nil {
			log.Println("[Xiaozhi][ALSA] write:", err)
			return
		}
	}
}

func alsaWrite(pcm []byte) (written int64, err error) {
	if len(pcm) == 0 {
		return alsaBytesWritten(), nil
	}
	cp := make([]byte, len(pcm))
	copy(cp, pcm)

	alsaMu.Lock()
	ch := alsaPCMCh
	alsaMu.Unlock()
	if ch == nil {
		return -1, fmt.Errorf("alsa not started")
	}
	defer func() {
		if recover() != nil {
			err = fmt.Errorf("alsa closed")
		}
	}()
	ch <- cp
	alsaMu.Lock()
	alsaBytes += int64(len(cp))
	total := alsaBytes
	alsaMu.Unlock()
	return total, nil
}

func alsaBytesWritten() int64 {
	alsaMu.Lock()
	defer alsaMu.Unlock()
	return alsaBytes
}

// alsaEnd closes the PCM queue so the pump drains to tinyplay then exits.
func alsaEnd() error {
	alsaMu.Lock()
	ch := alsaPCMCh
	done := alsaDone
	alsaMu.Unlock()
	if ch != nil {
		func() {
			defer func() { _ = recover() }()
			close(ch)
		}()
	}
	alsaMu.Lock()
	if alsaPCMCh == ch {
		alsaPCMCh = nil
	}
	alsaMu.Unlock()
	if done == nil {
		alsaReady.Store(false)
		return nil
	}
	return nil
}

// AlsaActive is true while tinyplay / pump is still running.
func AlsaActive() bool {
	return alsaReady.Load()
}
