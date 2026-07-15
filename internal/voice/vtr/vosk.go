package vtr

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"

	vosk "github.com/os-vector/vosk-api/go"
)

var (
	voskMu sync.Mutex
	model  *vosk.VoskModel
	rec    *vosk.VoskRecognizer
)

const voskModelPath = "/anki/data/assets/cozmo_resources/cloudless/en-US/model"

// InitVosk loads the STT model at process start (Xiaozhi-off path).
func InitVosk() {
	if err := EnsureVosk(); err != nil {
		log.Fatal("vosk init:", err)
	}
}

// EnsureVosk lazy-loads the model + recognizer if not already loaded.
func EnsureVosk() error {
	return ensureVoskWithGrammar(false)
}

// EnsureVoskBlackjack loads Vosk with a tiny hit/stand grammar (RAM-friendlier).
func EnsureVoskBlackjack() error {
	return ensureVoskWithGrammar(true)
}

func ensureVoskWithGrammar(blackjackOnly bool) error {
	voskMu.Lock()
	defer voskMu.Unlock()
	if model != nil && rec != nil {
		return nil
	}
	loadIntents()
	m, err := vosk.NewModel(voskModelPath)
	if err != nil {
		return fmt.Errorf("model %s: %w", voskModelPath, err)
	}
	// GetGrammerList uses the package-level model for FindWord — assign before calling.
	model = m
	grammar := GetGrammerList("en-US")
	if blackjackOnly {
		grammar = GetBlackjackGrammarList()
	}
	r, err := vosk.NewRecognizerGrm(m, 16000, grammar)
	if err != nil {
		model = nil
		m.Free()
		return fmt.Errorf("recognizer: %w", err)
	}
	r.SetMaxAlternatives(0)
	r.SetEndpointerDelays(3, 0, 0)
	rec = r
	if blackjackOnly {
		log.Println("[Vosk] model loaded (blackjack grammar):", voskModelPath)
	} else {
		log.Println("[Vosk] model loaded:", voskModelPath)
	}
	return nil
}

// UnloadVosk frees the model to reclaim RAM (blackjack gameMode exit).
func UnloadVosk() {
	voskMu.Lock()
	defer voskMu.Unlock()
	if rec != nil {
		rec.Free()
		rec = nil
	}
	if model != nil {
		model.Free()
		model = nil
	}
	log.Println("[Vosk] model unloaded")
}

// VoskLoaded reports whether the STT model is currently in memory.
func VoskLoaded() bool {
	voskMu.Lock()
	defer voskMu.Unlock()
	return model != nil && rec != nil
}

func Process(chunk []byte) string {
	voskMu.Lock()
	defer voskMu.Unlock()
	if rec == nil || model == nil {
		return ""
	}
	if len(chunk) == 0 {
		fmt.Println("empty chunk")
		return ""
	}
	stop, _ := DetectEndOfSpeech(chunk)
	rec.AcceptWaveform(chunk)
	if stop {
		var jres map[string]interface{}
		json.Unmarshal([]byte(rec.FinalResult()), &jres)
		transcribedText, _ := jres["text"].(string)
		fmt.Println("transcribed text: " + transcribedText)
		go func() {
			voskMu.Lock()
			if rec != nil {
				rec.Reset()
			}
			voskMu.Unlock()
		}()
		return transcribedText
	}
	return ""
}

func GetFreq() string {
	file, err := os.ReadFile("/sys/devices/system/cpu/cpu0/cpufreq/scaling_max_freq")
	if err == nil {
		return strings.TrimSpace(string(file))
	}
	return "533333"
}

func SetFreq(cpu, ram string) {
	go exec.Command("/usr/bin/sudo", "/usr/sbin/setfreq", cpu, ram).Run()
}
