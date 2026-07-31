package xiaozhi

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// OtaResult is the parsed relevant fields from a /xiaozhi/ota/ response.
type OtaResult struct {
	ActivationCode    string
	ActivationMessage string
	WebsocketURL      string
	WebsocketToken    string
	ProtocolVersion   int
}

// FetchActivation POSTs to {ota_base_url}xiaozhi/ota/ and parses the response.
// Empty DeviceID → random MAC; empty ClientID → UUID; then saves when caller persists cfg.
func FetchActivation(cfg *Config) (OtaResult, error) {
	cfg.OTABaseURL = normalizeOTAURL(cfg.OTABaseURL)
	if cfg.DeviceID == "" {
		cfg.DeviceID = genRandomMAC()
	}
	if cfg.ClientID == "" {
		cfg.ClientID = genUUIDv4()
	}

	result, err := doOtaPost(*cfg)
	if err != nil {
		return OtaResult{}, err
	}

	var out OtaResult
	if act, ok := result["activation"].(map[string]interface{}); ok {
		out.ActivationCode, _ = act["code"].(string)
		out.ActivationMessage, _ = act["message"].(string)
	}
	if ws, ok := result["websocket"].(map[string]interface{}); ok {
		if u, _ := ws["url"].(string); strings.TrimSpace(u) != "" {
			out.WebsocketURL = strings.TrimSpace(u)
		}
		if t, _ := ws["token"].(string); strings.TrimSpace(t) != "" {
			out.WebsocketToken = strings.TrimSpace(t)
		}
		if v, _ := ws["version"].(float64); int(v) > 0 {
			out.ProtocolVersion = int(v)
		}
	}
	return out, nil
}

// OtaCheck performs a passive OTA check (no activation code needed).
// If auto_apply is true, updates endpoint/token in cfg and saves.
func OtaCheck(cfg *Config) error {
	result, err := doOtaPost(*cfg)
	if err != nil {
		return err
	}
	changed := ApplyWebsocketFromOta(cfg, result)
	if changed {
		SaveConfig(*cfg)
	}
	return nil
}

// ApplyWebsocketFromOta updates cfg from OTA response websocket block.
// Returns true if any field changed.
func ApplyWebsocketFromOta(cfg *Config, result map[string]interface{}) bool {
	changed := false
	ws, ok := result["websocket"].(map[string]interface{})
	if !ok {
		return false
	}
	if u, _ := ws["url"].(string); strings.TrimSpace(u) != "" && cfg.Endpoint != strings.TrimSpace(u) {
		cfg.Endpoint = strings.TrimSpace(u)
		changed = true
	}
	if t, _ := ws["token"].(string); strings.TrimSpace(t) != "" && cfg.Token != strings.TrimSpace(t) {
		cfg.Token = strings.TrimSpace(t)
		changed = true
	}
	if v, _ := ws["version"].(float64); int(v) > 0 && cfg.ProtocolVersion != int(v) {
		cfg.ProtocolVersion = int(v)
		changed = true
	}
	return changed
}

// --- internal helpers ---

func doOtaPost(cfg Config) (map[string]interface{}, error) {
	if cfg.DeviceID == "" || cfg.ClientID == "" {
		return nil, fmt.Errorf("device_id and client_id required")
	}
	otaURL := cfg.OTABaseURL + "xiaozhi/ota/"
	body := buildOtaBody(cfg.DeviceID, cfg.ClientID)

	req, err := http.NewRequest("POST", otaURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Device-Id", cfg.DeviceID)
	req.Header.Set("Client-Id", cfg.ClientID)
	req.Header.Set("User-Agent", "wire-os/1.0")
	req.Header.Set("Accept-Language", "en-US")
	req.Header.Set("Activation-Version", "1")

	c := &http.Client{Timeout: 20 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("OTA request: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OTA HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var result map[string]interface{}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("OTA JSON: %w", err)
	}
	return result, nil
}

func buildOtaBody(deviceID, clientID string) []byte {
	hostname, _ := os.Hostname()
	payload := map[string]interface{}{
		"version":                2,
		"language":               "en-US",
		"mac_address":            deviceID,
		"uuid":                   clientID,
		"platform":               "linux",
		"arch":                   "arm",
		"hostname":               hostname,
		"minimum_free_heap_size": 0,
		"application": map[string]interface{}{
			"name":    "wire-os",
			"version": "1.0",
		},
		"board": map[string]interface{}{
			"type":   "wire-os",
			"name":   "vector",
			"vendor": "anki",
		},
	}
	b, _ := json.Marshal(payload)
	return b
}

func normalizeOTAURL(u string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		return "https://api.tenclass.net/"
	}
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		u = "https://" + u
	}
	if !strings.HasSuffix(u, "/") {
		u += "/"
	}
	return u
}

func genRandomMAC() string {
	var b [6]byte
	rand.Read(b[:])
	b[0] = (b[0] | 0x02) & 0xfe // locally administered, unicast
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", b[0], b[1], b[2], b[3], b[4], b[5])
}

func genUUIDv4() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
