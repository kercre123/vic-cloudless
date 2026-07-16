package xiaozhi

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/digital-dream-labs/vector-cloud/internal/clad/cloud"
	"github.com/digital-dream-labs/vector-cloud/internal/log"
	"github.com/digital-dream-labs/vector-cloud/internal/voice/vtr"
)

const (
	intentPlayBlackjack = "intent_play_blackjack"

	// Hard cap so a stuck gameMode cannot keep Vosk loaded forever.
	blackjackGameModeMaxAge = 20 * time.Minute

	// Vosk model costs ~90MB RSS. Refuse load unless MemAvailable stays above
	// this after Xiaozhi teardown + anim soft-restart (else LMK → fault 923).
	voskMinAvailKB int64 = 150 * 1024

	// After REFUSE, do not immediately re-ENTER from engine Blackjack streams.
	voskRefuseCooldown = 90 * time.Second
)

var (
	gameMu            sync.Mutex
	prepareMu         sync.Mutex // serializes PrepareBlackjackSTT (no double-load race)
	blackjackGameMode bool
	blackjackEntered  time.Time
	lastBlackjackMic  time.Time
	blackjackSTTReady bool
	voskRefusedUntil  time.Time

	// After EXIT, reopen Xiaozhi mic once (FakeTrigger) so the user can talk
	// without a second button press — especially when the exit wake itself
	// timed out on silence.
	reopenMicAfterBlackjack atomic.Bool

	// xiaozhiTurnSeq invalidates delayed afterSTT goroutines from a prior listen.
	xiaozhiTurnSeq atomic.Uint64
)

// BeginXiaozhiTurn returns a generation id for this listen; stale afterSTT must abort.
func BeginXiaozhiTurn() uint64 {
	return xiaozhiTurnSeq.Add(1)
}

// XiaozhiTurnCurrent is the active listen generation.
func XiaozhiTurnCurrent() uint64 {
	return xiaozhiTurnSeq.Load()
}

// MaybeEnterBlackjackFromIntent marks blackjack gameMode when MCP starts the game.
// Does NOT CloseSession / EnsureVosk here — that must wait until confirmation TTS
// finishes (otherwise mid-TTS CloseSession kills WSS and panics/load races).
func MaybeEnterBlackjackFromIntent(intent string) {
	if intent != intentPlayBlackjack {
		return
	}
	markBlackjackGameMode("intent " + intent)
}

func markBlackjackGameMode(reason string) {
	gameMu.Lock()
	already := blackjackGameMode
	blackjackGameMode = true
	if !already {
		blackjackEntered = time.Now()
		blackjackSTTReady = false
	}
	lastBlackjackMic = time.Now()
	gameMu.Unlock()

	// Stop continuous Xiaozhi relisten so Vosk and Xiaozhi never fight for the mic.
	DisarmRelistenPending()

	if !already {
		log.Println("[Game] blackjack ENTER (Vosk deferred until after TTS); reason:", reason)
	}
}

// PrepareBlackjackSTT closes Xiaozhi WSS first, frees anim RSS, then lazy-loads Vosk.
// Never loads Vosk while a Xiaozhi session is still open. Aborts instead of LMK
// if MemAvailable stays below voskMinAvailKB.
// Serialized: concurrent engine + after-TTS callers must not double-load / Unload.
// Returns true only when Vosk is ready for blackjack.
func PrepareBlackjackSTT(reason string) bool {
	prepareMu.Lock()
	defer prepareMu.Unlock()

	gameMu.Lock()
	if !blackjackGameMode {
		gameMu.Unlock()
		return false
	}
	if blackjackSTTReady && vtr.VoskLoaded() {
		gameMu.Unlock()
		return true
	}
	gameMu.Unlock()

	DisarmRelistenPending()
	// Tear down Xiaozhi + drop leftover PCM before the ~90MB Vosk RSS hits.
	CloseSession()
	CleanupPlaybackFiles()
	AlsaCancel()
	runtime.GC()
	debug.FreeOSMemory()
	time.Sleep(400 * time.Millisecond)

	// Another caller may have finished loading while we GC'd.
	if vtr.VoskLoaded() {
		gameMu.Lock()
		if blackjackGameMode {
			blackjackSTTReady = true
			gameMu.Unlock()
			log.Printf("[Game] Vosk already loaded — skip re-prepare (%s)", reason)
			return true
		}
		gameMu.Unlock()
		return false
	}

	avail := memAvailableKB()
	log.Printf("[Game] PrepareBlackjackSTT MemAvailable=%d kB before anim free (%s)", avail, reason)
	if avail > 0 && avail < voskMinAvailKB {
		if err := softRestartAnimForVosk(); err != nil {
			log.Println("[Game] anim soft-restart for Vosk failed:", err)
		}
		runtime.GC()
		debug.FreeOSMemory()
		avail = memAvailableKB()
		log.Printf("[Game] PrepareBlackjackSTT MemAvailable=%d kB after anim free", avail)
	}

	if avail > 0 && avail < voskMinAvailKB {
		log.Printf("[Game] REFUSE Vosk — MemAvailable=%d kB < %d kB (would LMK). Exit blackjack.",
			avail, voskMinAvailKB)
		gameMu.Lock()
		voskRefusedUntil = time.Now().Add(voskRefuseCooldown)
		gameMu.Unlock()
		ExitBlackjackGameMode("vosk_oom_guard")
		return false
	}

	if err := vtr.EnsureVoskBlackjack(); err != nil {
		log.Println("[Game] PrepareBlackjackSTT EnsureVoskBlackjack failed:", err)
		gameMu.Lock()
		voskRefusedUntil = time.Now().Add(voskRefuseCooldown)
		gameMu.Unlock()
		ExitBlackjackGameMode("vosk_load_failed")
		return false
	}
	after := memAvailableKB()
	if after > 0 && after < 40*1024 {
		// Loaded but machine is already critical — unload and bail.
		log.Printf("[Game] Vosk loaded but MemAvailable=%d kB critical — unloading", after)
		vtr.UnloadVosk()
		gameMu.Lock()
		voskRefusedUntil = time.Now().Add(voskRefuseCooldown)
		gameMu.Unlock()
		ExitBlackjackGameMode("vosk_post_load_oom")
		return false
	}
	gameMu.Lock()
	if !blackjackGameMode {
		gameMu.Unlock()
		vtr.UnloadVosk()
		return false
	}
	blackjackSTTReady = true
	voskRefusedUntil = time.Time{}
	gameMu.Unlock()
	log.Printf("[Game] Vosk ready — Xiaozhi closed; MemAvailable=%d kB; reason: %s",
		after, reason)
	return true
}

// softRestartAnimForVosk drops Wwise RSS (~50→~50MB cold) so Vosk can fit.
// Anim-only restart is OK here: conversation is over, engine will reattach.
func softRestartAnimForVosk() error {
	log.Println("[Game] soft-restart vic-anim to free RAM for Vosk")
	cmd := exec.Command("/usr/bin/sudo", "-n", "/bin/systemctl", "restart", "vic-anim.service")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v — %s", err, strings.TrimSpace(string(out)))
	}
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		if animRSSKB() > 0 {
			time.Sleep(1500 * time.Millisecond)
			return nil
		}
		time.Sleep(400 * time.Millisecond)
	}
	return fmt.Errorf("vic-anim did not come back")
}

func animRSSKB() int64 {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return -1
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if name[0] < '0' || name[0] > '9' {
			continue
		}
		comm, err := os.ReadFile("/proc/" + name + "/comm")
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(comm)) != "vic-anim" {
			continue
		}
		status, err := os.ReadFile("/proc/" + name + "/status")
		if err != nil {
			return -1
		}
		for _, line := range strings.Split(string(status), "\n") {
			if !strings.HasPrefix(line, "VmRSS:") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 2 {
				return -1
			}
			kb, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				return -1
			}
			return kb
		}
	}
	return -1
}

func memAvailableKB() int64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return -1
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "MemAvailable:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return -1
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return -1
		}
		return kb
	}
	return -1
}

// ExitBlackjackGameMode unloads Vosk (best-effort) and allows Xiaozhi on the next wake.
// Also arms a Xiaozhi mic reopen so the user can speak without pressing the button again.
func ExitBlackjackGameMode(reason string) {
	gameMu.Lock()
	if !blackjackGameMode {
		gameMu.Unlock()
		return
	}
	blackjackGameMode = false
	blackjackSTTReady = false
	blackjackEntered = time.Time{}
	lastBlackjackMic = time.Time{}
	gameMu.Unlock()

	vtr.UnloadVosk()
	DisarmRelistenPending()
	log.Println("[Game] blackjack EXIT — Vosk unloaded; reason:", reason)

	// OOM/load fail: engine may still spam Blackjack streams — arm Xiaozhi so user
	// can chat, but only on intentional leave do we always want mic. Both OK.
	reopenMicAfterBlackjack.Store(true)
	go ensureXiaozhiMicAfterBlackjackExit()
}

// ClearReopenMicAfterBlackjack drops the post-EXIT mic arm (continuous path already reopened).
func ClearReopenMicAfterBlackjack() {
	reopenMicAfterBlackjack.Store(false)
}

// ConsumeReopenMicAfterBlackjack is true once after EXIT until cleared/consumed.
func ConsumeReopenMicAfterBlackjack() bool {
	return reopenMicAfterBlackjack.Swap(false)
}

// ensureXiaozhiMicAfterBlackjackExit FakeTriggers a Xiaozhi listen after EXIT when
// nothing else owns the mic (max-age/oom, or exit-wake silence ended).
func ensureXiaozhiMicAfterBlackjackExit() {
	if !Enabled() {
		reopenMicAfterBlackjack.Store(false)
		return
	}
	for i := 0; i < 12; i++ {
		time.Sleep(500 * time.Millisecond)
		if !reopenMicAfterBlackjack.Load() || InBlackjackGameMode() {
			return
		}
		if !Enabled() {
			reopenMicAfterBlackjack.Store(false)
			return
		}
		// Exit wake may already be in runXiaozhiTurn — wait for it to finish.
		if InListenTurn() || IsPlaying() {
			continue
		}
		reopenMicAfterBlackjack.Store(false)
		if err := TriggerRelisten(); err != nil {
			log.Println("[Game] Xiaozhi mic arm after EXIT failed:", err)
			return
		}
		log.Println("[Game] Xiaozhi mic armed after blackjack EXIT — speak now")
		return
	}
}

// InBlackjackGameMode reports whether cloud is in local-Vosk blackjack mode.
func InBlackjackGameMode() bool {
	gameMu.Lock()
	defer gameMu.Unlock()
	return blackjackGameMode
}

// BlackjackSTTReady is true after PrepareBlackjackSTT succeeded.
func BlackjackSTTReady() bool {
	gameMu.Lock()
	defer gameMu.Unlock()
	return blackjackSTTReady && blackjackGameMode
}

// ShouldUseLocalVosk decides whether this hotword/stream uses Vosk instead of Xiaozhi.
//
// Engine hit/stand/play-again prompts use StreamType_Blackjack → Vosk.
// Hey Vector / backpack (Normal) → leave gameMode and use Xiaozhi after Vosk unload.
func ShouldUseLocalVosk(mode cloud.StreamType) bool {
	gameMu.Lock()
	inGame := blackjackGameMode
	entered := blackjackEntered
	refused := !voskRefusedUntil.IsZero() && time.Now().Before(voskRefusedUntil)
	gameMu.Unlock()

	if !inGame {
		if mode == cloud.StreamType_Blackjack {
			if refused {
				log.Println("[Game] skip blackjack re-enter — recent Vosk OOM refuse")
				return false
			}
			markBlackjackGameMode("engine StreamType_Blackjack")
			return PrepareBlackjackSTT("engine StreamType_Blackjack")
		}
		return false
	}

	if !entered.IsZero() && time.Since(entered) > blackjackGameModeMaxAge {
		ExitBlackjackGameMode("max age")
		return false
	}

	if mode == cloud.StreamType_Blackjack {
		gameMu.Lock()
		lastBlackjackMic = time.Now()
		gameMu.Unlock()
		// Must return Prepare result — false after OOM Exit must NOT runLocalVoskTurn.
		return PrepareBlackjackSTT("blackjack mic")
	}

	// Normal / button wake: user is back to chat — drop Vosk before Xiaozhi dials.
	ExitBlackjackGameMode("normal wake — return to Xiaozhi")
	return false
}
