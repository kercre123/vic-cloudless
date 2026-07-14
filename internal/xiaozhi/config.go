// Package xiaozhi implements the Xiaozhi AI voice assistant integration for vic-cloudless.
// Config is shared with wired via /data/data/com.anki.victor/persistent/xiaozhi/xiaozhi.json.
package xiaozhi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

const ConfigPath = "/data/data/com.anki.victor/persistent/xiaozhi/xiaozhi.json"

// Config mirrors the shared xiaozhi.json configuration.
type Config struct {
	Enabled               bool   `json:"enabled"`
	OTABaseURL            string `json:"ota_base_url"`
	Endpoint              string `json:"endpoint"`
	DeviceID              string `json:"device_id"`
	ClientID              string `json:"client_id"`
	Token                 string `json:"token"`
	ProtocolVersion       int    `json:"protocol_version"`
	AutoApplyOTAWebsocket bool   `json:"auto_apply_ota_websocket"`
	ConversationMode      string `json:"conversation_mode"`
	IdleTimeoutSec        int    `json:"idle_timeout_sec"`
	SessionIdleSec        int    `json:"session_idle_sec"`
	TTSMode               string `json:"tts_mode"`
}

func defaultConfig() Config {
	return Config{
		Enabled:               false,
		OTABaseURL:            "https://api.tenclass.net/",
		Endpoint:              "wss://api.tenclass.net/xiaozhi/v1/",
		ProtocolVersion:       1,
		AutoApplyOTAWebsocket: true,
		ConversationMode:      "continuous",
		IdleTimeoutSec:        20,
		SessionIdleSec:        60,
		TTSMode:               "xiaozhi",
	}
}

var (
	globalCfg     Config
	globalCfgOnce sync.Once
	globalCfgMu   sync.RWMutex
)

// LoadConfig reads config from disk (or returns default on error).
// It replaces any previously cached config so it's safe to call multiple times.
func LoadConfig() Config {
	globalCfgMu.Lock()
	defer globalCfgMu.Unlock()

	cfg := defaultConfig()
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		// Keep previous in-memory config if disk is temporarily unreadable.
		if globalCfg.Endpoint != "" || globalCfg.Enabled {
			return globalCfg
		}
		globalCfg = cfg
		return cfg
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		if globalCfg.Endpoint != "" || globalCfg.Enabled {
			return globalCfg
		}
		globalCfg = cfg
		return cfg
	}
	if cfg.ProtocolVersion == 0 {
		cfg.ProtocolVersion = 1
	}
	globalCfg = cfg
	return cfg
}

// GetConfig returns the last loaded config (or the default if never loaded).
func GetConfig() Config {
	globalCfgMu.RLock()
	defer globalCfgMu.RUnlock()
	return globalCfg
}

// SaveConfig writes the config atomically to disk and updates the in-memory cache.
func SaveConfig(cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(ConfigPath), 0777); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(ConfigPath, data, 0644); err != nil {
		return err
	}
	globalCfgMu.Lock()
	globalCfg = cfg
	globalCfgMu.Unlock()
	return nil
}

// Enabled returns true if Xiaozhi integration is enabled in the current config.
func Enabled() bool {
	globalCfgMu.RLock()
	defer globalCfgMu.RUnlock()
	return globalCfg.Enabled
}

// ContinuousMode returns true when conversation_mode == "continuous".
func ContinuousMode() bool {
	globalCfgMu.RLock()
	defer globalCfgMu.RUnlock()
	return globalCfg.ConversationMode == "continuous"
}
