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
	voskMinAvailKB int64 = 155 * 1024
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

// PrepareBlackjackSTT closes Xiaozhi WSS first, frees anim RSS, then lazy-loads Vosk.
// Never loads Vosk while a Xiaozhi session is still open. Aborts instead of LMK
// if MemAvailable stays below voskMinAvailKB.
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
	// Tear down Xiaozhi + drop leftover PCM before the ~90MB Vosk RSS hits.
	CloseSession()
	CleanupPlaybackFiles()
	AlsaCancel()
	runtime.GC()
	debug.FreeOSMemory()
	time.Sleep(400 * time.Millisecond)

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
		ExitBlackjackGameMode("vosk_oom_guard")
		return
	}

	if err := vtr.EnsureVoskBlackjack(); err != nil {
		log.Println("[Game] PrepareBlackjackSTT EnsureVoskBlackjack failed:", err)
		ExitBlackjackGameMode("vosk_load_failed")
		return
	}
	after := memAvailableKB()
	if after > 0 && after < 40*1024 {
		// Loaded but machine is already critical — unload and bail.
		log.Printf("[Game] Vosk loaded but MemAvailable=%d kB critical — unloading", after)
		vtr.UnloadVosk()
		ExitBlackjackGameMode("vosk_post_load_oom")
		return
	}
	gameMu.Lock()
	blackjackSTTReady = true
	gameMu.Unlock()
	log.Printf("[Game] Vosk ready — Xiaozhi closed; MemAvailable=%d kB; reason: %s",
		after, reason)
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
