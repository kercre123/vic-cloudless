package xiaozhi

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/digital-dream-labs/vector-cloud/internal/log"
)

// JPEGCaptureFunc returns one camera JPEG frame (WirePod CaptureSingleImage style).
// Injected from cloud/main so xiaozhi avoids importing engineProtoManager.
type JPEGCaptureFunc func(ctx context.Context, highRes bool) ([]byte, error)

var (
	jpegCaptureMu sync.RWMutex
	jpegCapture   JPEGCaptureFunc

	visionMu    sync.RWMutex
	visionURL   string
	visionToken string
)

// SetJPEGCapture registers the robot camera capture hook (call once from cloud main).
func SetJPEGCapture(fn JPEGCaptureFunc) {
	jpegCaptureMu.Lock()
	jpegCapture = fn
	jpegCaptureMu.Unlock()
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
func AnalyzeScene(ctx context.Context, question string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	jpeg, err := captureJPEG(cctx, true)
	if err != nil {
		return "", fmt.Errorf("capture: %w", err)
	}
	log.Printf("[Xiaozhi] analyze_photo captured %d bytes JPEG", len(jpeg))

	ectx, cancel2 := context.WithTimeout(ctx, 45*time.Second)
	defer cancel2()
	result, err := ExplainPhoto(ectx, question, jpeg)
	if err != nil {
		return "", err
	}
	log.Printf("[Xiaozhi] analyze_photo Explain OK (%d chars)", len(result))
	return result, nil
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
