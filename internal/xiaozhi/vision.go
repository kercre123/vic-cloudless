package xiaozhi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/digital-dream-labs/vector-cloud/internal/log"
)

// JPEGCaptureFunc returns one camera JPEG frame (WirePod CaptureSingleImage style).
// Injected from cloud/main so xiaozhi avoids importing engineProtoManager.
type JPEGCaptureFunc func(ctx context.Context, highRes bool) ([]byte, error)

// PhotoShutterFunc plays the take-photo shutter animation (WirePod anim_photo_shutter_01).
type PhotoShutterFunc func(ctx context.Context) error

var (
	jpegCaptureMu sync.RWMutex
	jpegCapture   JPEGCaptureFunc

	photoShutterMu sync.RWMutex
	photoShutter   PhotoShutterFunc

	visionMu    sync.RWMutex
	visionURL   string
	visionToken string

	// Single-flight analyze_photo: server often calls the tool twice.
	analyzeMu     sync.Mutex
	analyzeFlight *analyzeFlightState

	// Last successful/attempted analyze wall time — delay continuous relisten so anim RAM recovers.
	lastAnalyzeUnixNano atomic.Int64
)

type analyzeFlightState struct {
	done   chan struct{}
	result string
	err    error
}

// SetJPEGCapture registers the robot camera capture hook (call once from cloud main).
func SetJPEGCapture(fn JPEGCaptureFunc) {
	jpegCaptureMu.Lock()
	jpegCapture = fn
	jpegCaptureMu.Unlock()
}

// SetPhotoShutter registers the shutter-animation hook (WirePod-style UX).
func SetPhotoShutter(fn PhotoShutterFunc) {
	photoShutterMu.Lock()
	photoShutter = fn
	photoShutterMu.Unlock()
}

func playPhotoShutter(ctx context.Context) {
	photoShutterMu.RLock()
	fn := photoShutter
	photoShutterMu.RUnlock()
	if fn == nil {
		return
	}
	if err := fn(ctx); err != nil {
		log.Println("[Xiaozhi] analyze_photo shutter:", err)
		return
	}
	log.Println("[Xiaozhi] analyze_photo: shutter")
}

// SetVisionExplain stores Xiaozhi vision Explain endpoint from MCP initialize.
func SetVisionExplain(url, token string) {
	visionMu.Lock()
	visionURL = strings.TrimSpace(url)
	visionToken = strings.TrimSpace(token)
	visionMu.Unlock()
	if visionURL != "" {
		log.Println("[Xiaozhi] vision Explain URL set:", visionURL)
	} else {
		log.Println("[Xiaozhi] vision Explain URL cleared / empty")
	}
}

// GetVisionExplain returns the current Explain URL and token.
func GetVisionExplain() (url, token string) {
	visionMu.RLock()
	defer visionMu.RUnlock()
	return visionURL, visionToken
}

// HasVisionExplain reports whether an Explain URL is configured.
func HasVisionExplain() bool {
	u, _ := GetVisionExplain()
	return u != ""
}

func captureJPEG(ctx context.Context, highRes bool) ([]byte, error) {
	jpegCaptureMu.RLock()
	fn := jpegCapture
	jpegCaptureMu.RUnlock()
	if fn == nil {
		return nil, fmt.Errorf("JPEG capture not registered")
	}
	return fn(ctx, highRes)
}

// ExplainPhoto uploads a JPEG to Xiaozhi vision URL (ESP32 multipart contract).
func ExplainPhoto(ctx context.Context, question string, jpeg []byte) (string, error) {
	url, token := GetVisionExplain()
	if url == "" {
		return "", fmt.Errorf("vision Explain URL not set (server did not send capabilities.vision)")
	}
	if len(jpeg) == 0 {
		return "", fmt.Errorf("empty JPEG")
	}
	question = strings.TrimSpace(question)
	if question == "" {
		question = "What do you see?"
	}

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if err := w.WriteField("question", question); err != nil {
		return "", err
	}
	part, err := w.CreateFormFile("file", "camera.jpg")
	if err != nil {
		return "", err
	}
	if _, err := part.Write(jpeg); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return "", err
	}
	cfg := GetConfig()
	if cfg.DeviceID != "" {
		req.Header.Set("Device-Id", cfg.DeviceID)
	}
	if cfg.ClientID != "" {
		req.Header.Set("Client-Id", cfg.ClientID)
	}
	if token != "" {
		tok := token
		if !strings.HasPrefix(tok, "Bearer ") {
			tok = "Bearer " + tok
		}
		req.Header.Set("Authorization", tok)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	client := &http.Client{Timeout: 45 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("Explain HTTP %d: %s", resp.StatusCode, truncate(string(out), 200))
	}
	return string(out), nil
}

// AnalyzeScene captures one frame and uploads it for Xiaozhi vision analysis.
// WirePod order: soft-end intro → capture → shutter flash → Explain → MCP reply.
// Must finish tools/call quickly or the server sends notifications/cancelled and
// never streams analysis TTS.
func AnalyzeScene(ctx context.Context, question string) (string, error) {
	analyzeMu.Lock()
	if f := analyzeFlight; f != nil {
		analyzeMu.Unlock()
		log.Println("[Xiaozhi] analyze_photo waiting — join in-flight capture")
		<-f.done
		return f.result, f.err
	}
	f := &analyzeFlightState{done: make(chan struct{})}
	analyzeFlight = f
	analyzeMu.Unlock()

	defer func() {
		analyzeMu.Lock()
		analyzeFlight = nil
		close(f.done)
		analyzeMu.Unlock()
	}()

	SuspendPlaybackForCapture()

	// Capture FIRST — Xiaozhi cancels tools/call around ~8–10s. Shutter before
	// capture burned ~0.8s and tipped us over the budget (notifications/cancelled
	// → LLM says "timeout" even when Explain later succeeds).
	jpeg, err := captureJPEGForAnalysis(ctx)
	ResumePlaybackAfterCapture()
	NoteAnalyzeAttempt()
	if err != nil {
		f.err = fmt.Errorf("capture: %w", err)
		return "", f.err
	}
	log.Printf("[Xiaozhi] analyze_photo captured %d bytes JPEG", len(jpeg))

	// Shutter in parallel with Explain — UX click without blocking the MCP reply.
	go playPhotoShutter(context.Background())

	ectx, cancel2 := context.WithTimeout(ctx, 20*time.Second)
	defer cancel2()
	raw, err := ExplainPhoto(ectx, question, jpeg)
	if err != nil {
		f.err = err
		return "", err
	}
	analysis, err := parseExplainBody(raw)
	if err != nil {
		f.err = err
		log.Printf("[Xiaozhi] analyze_photo Explain body: %s", truncate(raw, 200))
		return "", err
	}
	log.Printf("[Xiaozhi] analyze_photo Explain OK (%d chars): %s", len(analysis), truncate(analysis, 120))
	// Plain description for the LLM (not raw JSON with filename).
	f.result = analysis
	return analysis, nil
}

// AnalyzeInFlight is true while AnalyzeScene holds the single-flight lock work.
func AnalyzeInFlight() bool {
	analyzeMu.Lock()
	defer analyzeMu.Unlock()
	return analyzeFlight != nil
}

// NoteAnalyzeAttempt records that analyze_photo ran (success or fail) for post-turn cooldown.
func NoteAnalyzeAttempt() {
	lastAnalyzeUnixNano.Store(time.Now().UnixNano())
}

// AnalyzeCooldownRemaining is how long continuous mode should wait before FakeTrigger
// after a heavy analyze_photo (camera + TTS spikes anim RAM; immediate relisten → yellow fault).
func AnalyzeCooldownRemaining() time.Duration {
	ns := lastAnalyzeUnixNano.Load()
	if ns == 0 {
		return 0
	}
	elapsed := time.Since(time.Unix(0, ns))
	const cool = 4 * time.Second
	if elapsed >= cool {
		return 0
	}
	return cool - elapsed
}

const minAnalysisJPEGBytes = 4000

func captureJPEGForAnalysis(ctx context.Context) ([]byte, error) {
	// Low-res is enough for Explain and usually returns a frame sooner (tools/call budget).
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	jpeg, err := captureJPEG(cctx, false)
	cancel()
	if err != nil {
		return nil, err
	}
	if len(jpeg) == 0 {
		return nil, fmt.Errorf("empty camera frame")
	}
	if len(jpeg) < minAnalysisJPEGBytes {
		log.Printf("[Xiaozhi] analyze_photo small frame (%d B) — still sending", len(jpeg))
	}
	return jpeg, nil
}

// parseExplainBody turns Xiaozhi vision HTTP JSON into plain text for MCP/TTS.
// ESP32 returns {"success":true,"result":"..."} or {"success":false,"message":"..."}.
func parseExplainBody(body string) (string, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", fmt.Errorf("empty explain response")
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return body, nil
	}
	if success, ok := m["success"].(bool); ok {
		if !success {
			msg := explainErrorMessage(m)
			if msg == "" {
				msg = "vision explain failed"
			}
			return "", fmt.Errorf("%s", msg)
		}
		if text := explainTextField(m); text != "" {
			return text, nil
		}
	}
	if text := explainTextField(m); text != "" {
		return text, nil
	}
	return body, nil
}

func explainTextField(m map[string]interface{}) string {
	for _, key := range []string{"result", "text", "description", "answer", "content", "message"} {
		if v, ok := m[key].(string); ok {
			v = strings.TrimSpace(v)
			if v != "" {
				return v
			}
		}
	}
	return ""
}

func explainErrorMessage(m map[string]interface{}) string {
	for _, key := range []string{"message", "error", "msg"} {
		if v, ok := m[key].(string); ok {
			v = strings.TrimSpace(v)
			if v != "" {
				return v
			}
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// parseVisionCaps extracts vision.url / vision.token from MCP initialize params.
func parseVisionCaps(params map[string]interface{}) (url, token string) {
	if params == nil {
		return "", ""
	}
	caps, _ := params["capabilities"].(map[string]interface{})
	if caps == nil {
		return "", ""
	}
	vision, _ := caps["vision"].(map[string]interface{})
	if vision == nil {
		return "", ""
	}
	if u, ok := vision["url"].(string); ok {
		url = u
	}
	if t, ok := vision["token"].(string); ok {
		token = t
	}
	return url, token
}
