package xiaozhi

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/digital-dream-labs/vector-cloud/internal/log"
)

// Soft-restart vic-anim after Xiaozhi WSS ends so ExternalAudio/Wwise RSS
// returns toward ~50MB. Controlled systemctl stop is SERVICE_RESULT=success
// → no face fault 800 (unlike crash/OOM).

var (
	reclaimMu       sync.Mutex
	reclaimLast     time.Time
	reclaimMinGap   = 45 * time.Second
	reclaimBusyWait = 45 * time.Second
	// Soft-restart when anim stays bloated after WSS is gone (music/idle leak).
	reclaimAnimMinKB int64 = 100 * 1024 // 100 MB
)

// RequestAnimMemoryReclaim schedules a soft vic-anim restart once playback is idle.
// Call after goodbye / EndConversation, or via MaybeReclaimAnimIfBloated when idle.
func RequestAnimMemoryReclaim(reason string) {
	go runAnimMemoryReclaim(reason)
}

// MaybeReclaimAnimIfBloated restarts anim only when Xiaozhi is fully idle and
// vic-anim RSS is still high. Avoids the immediate idle-reclaim NoCloud race
// while still preventing fault-number freezes after long music.
func MaybeReclaimAnimIfBloated(reason string) {
	if SessionAlive() {
		return
	}
	if busyFlagPresent() {
		return
	}
	sess.mu.Lock()
	playing := sess.playing
	turnActive := sess.turnCancel != nil
	sess.mu.Unlock()
	if playing || turnActive {
		return
	}
	kb := animRSSKB()
	if kb < reclaimAnimMinKB {
		return
	}
	log.Printf("[Xiaozhi] anim bloated while idle (%dkB >= %dkB) — reclaim (%s)",
		kb, reclaimAnimMinKB, reason)
	RequestAnimMemoryReclaim(reason)
}

// ScheduleIdleAnimReclaim waits then MaybeReclaim — gives the user time to
// Hey Vector again without NoCloud from an immediate anim restart.
func ScheduleIdleAnimReclaim(delay time.Duration, reason string) {
	go func() {
		time.Sleep(delay)
		MaybeReclaimAnimIfBloated(reason)
	}()
}

func animRSSKB() int64 {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return -1
	}
	var best int64 = -1
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
			continue
		}
		for _, line := range strings.Split(string(status), "\n") {
			if !strings.HasPrefix(line, "VmRSS:") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 2 {
				break
			}
			kb, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				break
			}
			if kb > best {
				best = kb
			}
			break
		}
	}
	return best
}

func runAnimMemoryReclaim(reason string) {
	reclaimMu.Lock()
	if time.Since(reclaimLast) < reclaimMinGap {
		reclaimMu.Unlock()
		log.Println("[Xiaozhi] anim reclaim skipped (debounce):", reason)
		return
	}
	reclaimLast = time.Now()
	reclaimMu.Unlock()

	log.Println("[Xiaozhi] anim reclaim armed:", reason)

	// Stop any leftover ExternalAudio before killing anim.
	_ = RequestCancelPlayback()
	ClearCancelFlags()
	CleanupPlaybackFiles()
	SetPlaying(false)
	DisarmRelistenPending()

	deadline := time.Now().Add(reclaimBusyWait)
	for time.Now().Before(deadline) {
		if !busyFlagPresent() {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if busyFlagPresent() {
		log.Println("[Xiaozhi] anim reclaim: busy still set — clearing and restarting anyway")
		_ = os.Remove(BusyPath)
		_ = os.Remove("/run/xiaozhi-busy")
	}

	// Brief settle so face is not mid-draw when anim exits.
	time.Sleep(400 * time.Millisecond)

	cmd := exec.Command("/usr/bin/sudo", "-n", "/anki/bin/xiaozhi-reclaim-anim.sh")
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("[Xiaozhi] anim reclaim FAILED (%s): %v — %s", reason, err, string(out))
		return
	}
	log.Printf("[Xiaozhi] anim reclaim OK (%s): %s", reason, strings.TrimSpace(string(out)))
}

func busyFlagPresent() bool {
	if _, err := os.Stat(BusyPath); err == nil {
		return true
	}
	if _, err := os.Stat("/run/xiaozhi-busy"); err == nil {
		return true
	}
	return false
}
