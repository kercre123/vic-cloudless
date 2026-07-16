package xiaozhi

import (
	"os"
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

	// After REFUSE / idle unload, do not immediately re-ENTER from engine Blackjack streams.
	voskRefuseCooldown = 90 * time.Second

	// Blackjack-only: no Vosk mic/STT activity for this long → unload + leave game mode.
	voskIdleTimeout = 30 * time.Second

	// After idle unload, block engine StreamType_Blackjack re-enter briefly.
	voskIdleReenterCooldown = 60 * time.Second
)

var (
	gameMu            sync.Mutex
	prepareMu         sync.Mutex // serializes PrepareBlackjackSTT (no double-load race)
	blackjackGameMode bool
	blackjackEntered  time.Time
	lastBlackjackMic  time.Time
	lastVoskActivity  time.Time // last blackjack Vosk use (mic / STT / prepare)
	blackjackSTTReady bool
	voskRefusedUntil  time.Time

	blackjackIdleWatchOnce sync.Once

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
	now := time.Now()
	lastBlackjackMic = now
	lastVoskActivity = now
	gameMu.Unlock()

	// Stop continuous Xiaozhi relisten so Vosk and Xiaozhi never fight for the mic.
	DisarmRelistenPending()
	ensureBlackjackIdleWatcher()

	if !already {
		log.Println("[Game] blackjack ENTER (Vosk deferred until after TTS); reason:", reason)
		log.Printf("[Game] Vosk idle watchdog armed (%v no activity → unload)", voskIdleTimeout)
	}
}

// NoteBlackjackVoskActivity resets the blackjack-only idle timer (mic / STT / prepare).
func NoteBlackjackVoskActivity(why string) {
	gameMu.Lock()
	if !blackjackGameMode {
		gameMu.Unlock()
		return
	}
	now := time.Now()
	lastVoskActivity = now
	lastBlackjackMic = now
	gameMu.Unlock()
	ensureBlackjackIdleWatcher()
}

func ensureBlackjackIdleWatcher() {
	blackjackIdleWatchOnce.Do(func() {
		go blackjackIdleWatcherLoop()
	})
}

// blackjackIdleWatcherLoop unloads Vosk after voskIdleTimeout with no activity.
// Only runs meaningful checks while blackjack game mode + Vosk ready.
func blackjackIdleWatcherLoop() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		gameMu.Lock()
		inGame := blackjackGameMode
		ready := blackjackSTTReady
		last := lastVoskActivity
		if last.IsZero() {
			last = lastBlackjackMic
		}
		gameMu.Unlock()

		if !inGame || !ready || last.IsZero() {
			continue
		}
		idle := time.Since(last)
		if idle < voskIdleTimeout {
			continue
		}

		log.Printf("[Game] Vosk idle %v (threshold %v) — no blackjack activity; unloading Vosk",
			idle.Round(time.Second), voskIdleTimeout)
		gameMu.Lock()
		voskRefusedUntil = time.Now().Add(voskIdleReenterCooldown)
		gameMu.Unlock()
		ExitBlackjackGameMode("vosk_idle_30s")
		log.Println("[Game] EXIT after Vosk idle 30s — Xiaozhi on next manual wake (no auto-mic); Blackjack re-enter blocked ~60s")
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
	log.Printf("[Game] PrepareBlackjackSTT MemAvailable=%d kB (%s)", avail, reason)

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
	lastVoskActivity = time.Now()
	lastBlackjackMic = lastVoskActivity
	gameMu.Unlock()
	log.Printf("[Game] Vosk ready — Xiaozhi closed; MemAvailable=%d kB; reason: %s",
		after, reason)
	ensureBlackjackIdleWatcher()
	return true
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
// Auto-mic FakeTrigger only for Normal wake EXIT; idle/OOM leave mic closed until button/Hey Vector.
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
	lastVoskActivity = time.Time{}
	gameMu.Unlock()

	vtr.UnloadVosk()
	DisarmRelistenPending()
	log.Println("[Game] blackjack EXIT — Vosk unloaded; reason:", reason)

	// Idle / OOM / max-age: do not FakeTrigger — user wakes manually.
	if strings.Contains(reason, "normal wake") {
		reopenMicAfterBlackjack.Store(true)
		go ensureXiaozhiMicAfterBlackjackExit()
		return
	}
	reopenMicAfterBlackjack.Store(false)
	log.Println("[Game] mic stays closed after EXIT — wait for Hey Vector / button")
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
				log.Println("[Game] skip blackjack re-enter — cooldown after OOM/idle Vosk unload")
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
		NoteBlackjackVoskActivity("StreamType_Blackjack")
		// Must return Prepare result — false after OOM Exit must NOT runLocalVoskTurn.
		return PrepareBlackjackSTT("blackjack mic")
	}

	// Normal / button wake: user is back to chat — drop Vosk before Xiaozhi dials.
	ExitBlackjackGameMode("normal wake — return to Xiaozhi")
	return false
}
