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
)

var (
	gameMu            sync.Mutex
	blackjackGameMode bool
	blackjackEntered  time.Time
	lastBlackjackMic  time.Time
	blackjackSTTReady bool

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

// PrepareBlackjackSTT closes Xiaozhi WSS first, then lazy-loads Vosk.
// Never loads Vosk while a Xiaozhi session is still open.
func PrepareBlackjackSTT(reason string) {
	gameMu.Lock()
	if !blackjackGameMode {
		gameMu.Unlock()
		return
	}
	if blackjackSTTReady && vtr.VoskLoaded() {
		gameMu.Unlock()
		return
	}
	gameMu.Unlock()

	DisarmRelistenPending()
	// Tear down Xiaozhi + drop leftover PCM before the ~68MB Vosk model hits RAM.
	CloseSession()
	CleanupPlaybackFiles()
	runtime.GC()
	debug.FreeOSMemory()
	// Let ExternalAudio / Wwise finish draining after long music/TTS.
	time.Sleep(800 * time.Millisecond)
	runtime.GC()
	debug.FreeOSMemory()

	avail := memAvailableKB()
	log.Printf("[Game] PrepareBlackjackSTT MemAvailable=%d kB before Vosk (%s)", avail, reason)
	if avail > 0 && avail < 90000 {
		// Too tight for Vosk+anim+engine — drop buffers again and wait a bit.
		log.Println("[Game] low RAM before Vosk — delaying load")
		time.Sleep(1500 * time.Millisecond)
		runtime.GC()
		debug.FreeOSMemory()
		avail = memAvailableKB()
		log.Printf("[Game] PrepareBlackjackSTT MemAvailable=%d kB after wait", avail)
	}

	if err := vtr.EnsureVoskBlackjack(); err != nil {
		log.Println("[Game] PrepareBlackjackSTT EnsureVoskBlackjack failed:", err)
		return
	}
	gameMu.Lock()
	blackjackSTTReady = true
	gameMu.Unlock()
	log.Printf("[Game] Vosk ready — Xiaozhi closed; MemAvailable=%d kB; reason: %s",
		memAvailableKB(), reason)
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
	gameMu.Unlock()

	if !inGame {
		if mode == cloud.StreamType_Blackjack {
			markBlackjackGameMode("engine StreamType_Blackjack")
			PrepareBlackjackSTT("engine StreamType_Blackjack")
			return true
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
		PrepareBlackjackSTT("blackjack mic")
		return true
	}

	// Normal / button wake: user is back to chat — drop Vosk before Xiaozhi dials.
	ExitBlackjackGameMode("normal wake — return to Xiaozhi")
	return false
}
