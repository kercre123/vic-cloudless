// Package memprobe is a TEMPORARY 10s RAM sampler for performance tuning.
// Remove once profiling is done (search TEMP_PERF_RAM_PROBE).
package memprobe

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/digital-dream-labs/vector-cloud/internal/log"
	"github.com/digital-dream-labs/vector-cloud/internal/xiaozhi"
)

// TEMP_PERF_RAM_PROBE — delete this package + Start() call in cloud/main.go when done.
const interval = 10 * time.Second

var watchProcs = []string{
	"vic-cloud",
	"vic-anim",
	"vic-engine",
	"vic-robot",
	"vic-gateway",
	"wired",
	"mm-anki-camera",
}

// Start launches a background sampler. Safe to call once at boot.
func Start() {
	go run()
}

func run() {
	boot := time.Now()
	var first map[string]int64
	tick := 0
	bloatedIdleTicks := 0
	log.Println("[MemProbe] TEMP 10s RAM sampler started (remove after perf pass)")
	for {
		tick++
		sys := readMemInfo()
		rss := readProcRSS()
		if first == nil {
			first = make(map[string]int64, len(rss))
			for k, v := range rss {
				first[k] = v
			}
		}
		phase := detectPhase()
		var parts []string
		for _, name := range watchProcs {
			kb, ok := rss[name]
			if !ok {
				continue
			}
			delta := kb - first[name]
			parts = append(parts, fmt.Sprintf("%s=%dkB(%+d)", name, kb, delta))
		}
		// Catch unexpected heavy processes (>20MB) not in the watch list.
		for name, kb := range rss {
			if kb < 20*1024 {
				continue
			}
			known := false
			for _, w := range watchProcs {
				if w == name {
					known = true
					break
				}
			}
			if !known {
				parts = append(parts, fmt.Sprintf("%s=%dkB", name, kb))
			}
		}
		up := time.Since(boot).Round(time.Second)
		log.Printf("[MemProbe] #%d up=%s phase=%s sys={total=%dkB free=%dkB avail=%dkB anon=%dkB cached=%dkB} procs={%s}",
			tick, up, phase,
			sys["MemTotal"], sys["MemFree"], sys["MemAvailable"], sys["AnonPages"], sys["Cached"],
			strings.Join(parts, " "))

		// Safety net: after long music, anim keeps growing while idle with WSS
		// closed — reclaim before avail collapses into face fault numbers.
		animKB := rss["vic-anim"]
		if phase == "idle" && animKB >= 100*1024 && !xiaozhi.SessionAlive() {
			bloatedIdleTicks++
			if bloatedIdleTicks >= 3 { // ~30s of idle+bloated samples
				xiaozhi.MaybeReclaimAnimIfBloated("memprobe_anim_bloated")
				bloatedIdleTicks = 0
			}
		} else {
			bloatedIdleTicks = 0
		}

		time.Sleep(interval)
	}
}

func detectPhase() string {
	// Cheap flag-based hint so logs align with talk / game / vision.
	tags := make([]string, 0, 4)
	if fileExists("/run/vic-cloud/xiaozhi-busy") || fileExists("/run/xiaozhi-busy") {
		tags = append(tags, "tts")
	}
	if fileExists("/run/vic-cloud/xiaozhi-stream") || fileExists("/run/xiaozhi-stream") {
		tags = append(tags, "stream")
	}
	if fileExists("/run/vic-cloud/xiaozhi-relisten") || fileExists("/run/xiaozhi-relisten") ||
		fileExists("/run/vic-cloud/xiaozhi-relisten-pending") {
		tags = append(tags, "relisten")
	}
	// Blackjack / game often leaves vosk loaded; flag if present.
	if fileExists("/run/vic-cloud/blackjack") || fileExists("/run/xiaozhi-blackjack") {
		tags = append(tags, "game")
	}
	if len(tags) == 0 {
		return "idle"
	}
	return strings.Join(tags, "+")
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func readMemInfo() map[string]int64 {
	out := map[string]int64{
		"MemTotal": -1, "MemFree": -1, "MemAvailable": -1,
		"AnonPages": -1, "Cached": -1,
	}
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		for key := range out {
			if !strings.HasPrefix(line, key+":") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			if kb, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
				out[key] = kb
			}
		}
	}
	return out
}

// readProcRSS maps process basename -> VmRSS kB (largest if multiple).
func readProcRSS() map[string]int64 {
	out := make(map[string]int64)
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid := e.Name()
		if pid[0] < '0' || pid[0] > '9' {
			continue
		}
		commPath := filepath.Join("/proc", pid, "comm")
		commBytes, err := os.ReadFile(commPath)
		if err != nil {
			continue
		}
		name := strings.TrimSpace(string(commBytes))
		if name == "" {
			continue
		}
		statusPath := filepath.Join("/proc", pid, "status")
		kb := readVmRSS(statusPath)
		if kb <= 0 {
			continue
		}
		if prev, ok := out[name]; !ok || kb > prev {
			out[name] = kb
		}
	}
	return out
}

func readVmRSS(statusPath string) int64 {
	f, err := os.Open(statusPath)
	if err != nil {
		return -1
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
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
	return -1
}
