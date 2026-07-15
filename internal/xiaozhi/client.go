package xiaozhi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// Message types from server.
const (
	MsgTypeHello        = "hello"
	MsgTypeTTS          = "tts"
	MsgTypeLLM          = "llm"
	MsgTypeSTT          = "stt"
	MsgTypeMCP          = "mcp"
	MsgTypeGoodbye      = "goodbye"
	MsgTypeError        = "error"
)

// TTSState values.
const (
	TTSStateStart         = "start"
	TTSStateStop          = "stop"
	TTSStateSentenceStart = "sentence_start"
)

// ServerMessage is a parsed JSON message from the server.
type ServerMessage struct {
	Type      string `json:"type"`
	State     string `json:"state,omitempty"`
	Text      string `json:"text,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Emotion   string `json:"emotion,omitempty"`
	Error     string `json:"error,omitempty"`
	// For MCP
	ID     interface{}            `json:"id,omitempty"`
	Method string                 `json:"method,omitempty"`
	Params map[string]interface{} `json:"params,omitempty"`
}

// Client is a Xiaozhi WebSocket client.
type Client struct {
	cfg        Config
	conn       *websocket.Conn
	mu         sync.Mutex // protects conn writes
	sessionID  string
	closed     bool
	dead       bool // set when read loop ends or a write fails (TCP broken)
	readCancel context.CancelFunc

	// Event channels — written by the read loop, consumed by Turn.
	sttCh     chan string        // transcribed text
	ttsCh     chan ServerMessage // tts state changes
	llmCh     chan ServerMessage // llm text
	audioCh   chan []byte        // binary opus frames from server (TTS audio)
	mcpCh     chan ServerMessage // mcp tool calls
	goodbyeCh chan struct{}
	errCh     chan error

	// TRACE: WSS binary TTS RX counters (uint32 — ARM32 forbids unaligned uint64 atomics).
	rxAudioN       uint32
	rxAudioDropped uint32
}

// Dial creates and connects a new Xiaozhi WebSocket client, sends hello, and waits for server hello.
// The read loop runs until Close(); dialCtx only bounds dial+hello.
func Dial(dialCtx context.Context, cfg Config) (*Client, error) {
	header := buildWSHeaders(cfg)
	dialer := websocket.DefaultDialer
	conn, resp, err := dialer.DialContext(dialCtx, cfg.Endpoint, header)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("WSS dial %s: HTTP %d: %w", cfg.Endpoint, resp.StatusCode, err)
		}
		return nil, fmt.Errorf("WSS dial %s: %w", cfg.Endpoint, err)
	}

	c := &Client{
		cfg:       cfg,
		conn:      conn,
		sttCh:     make(chan string, 8),
		// Large TTS/LLM buffers: sentence_start floods must not drop (default: lose text).
		ttsCh:     make(chan ServerMessage, 128),
		llmCh:     make(chan ServerMessage, 128),
		// Deep enough that slow arm/pad must not drop head Opus (was 256 + silent drop).
		audioCh:   make(chan []byte, 1024),
		mcpCh:     make(chan ServerMessage, 16),
		goodbyeCh: make(chan struct{}, 1),
		errCh:     make(chan error, 4),
	}

	// Send hello.
	hello := buildHelloEvent(cfg)
	if err := c.writeJSON(hello); err != nil {
		conn.Close()
		return nil, fmt.Errorf("hello send: %w", err)
	}

	// Wait for server hello (up to 15s).
	helloCtx, helloCancel := context.WithTimeout(dialCtx, 15*time.Second)
	defer helloCancel()
	if err := c.readServerHello(helloCtx); err != nil {
		conn.Close()
		return nil, fmt.Errorf("hello recv: %w", err)
	}

	readCtx, readCancel := context.WithCancel(context.Background())
	c.readCancel = readCancel
	go c.readLoop(readCtx)
	return c, nil
}

// Close closes the WebSocket connection and stops the read loop.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	if c.readCancel != nil {
		c.readCancel()
		c.readCancel = nil
	}
	_ = c.conn.Close()
}

// Alive reports whether the client is usable (not closed and not dead).
// A peer/server close marks dead even before Close() is called — without this,
// EnsureConnected would reuse a broken-pipe socket.
func (c *Client) Alive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.closed && !c.dead
}

func (c *Client) markDeadLocked() {
	c.dead = true
}

// DrainEventChannels discards queued STT/TTS/LLM/audio events so a new
// listen round on a reused socket does not see leftovers from the prior round.
//
// MCP (mcpCh) is intentionally NOT drained — the MCP pump must answer
// initialize / tools/list promptly. Dropping those (e.g. after a silence
// STT timeout) leaves the server without self-control tools for the session.
func (c *Client) DrainEventChannels() {
	for {
		select {
		case <-c.sttCh:
		case <-c.ttsCh:
		case <-c.llmCh:
		case <-c.audioCh:
		case <-c.goodbyeCh:
		default:
			return
		}
	}
}

// DrainAudioChannel discards queued Opus TTS frames only (keep TTS/LLM/MCP).
func (c *Client) DrainAudioChannel() (n int) {
	for {
		select {
		case <-c.audioCh:
			n++
		default:
			return n
		}
	}
}

// SessionID returns the session_id received from the server hello.
func (c *Client) SessionID() string { return c.sessionID }

// ListenStart sends listen start to the server.
func (c *Client) ListenStart() error {
	return c.writeJSON(map[string]interface{}{
		"type":       "listen",
		"state":      "start",
		"mode":       "auto",
		"session_id": c.sessionID,
	})
}

// ListenStop sends listen stop to the server (VAD end / stream done).
func (c *Client) ListenStop() error {
	return c.writeJSON(map[string]interface{}{
		"type":       "listen",
		"state":      "stop",
		"session_id": c.sessionID,
	})
}

// SendAudio sends a binary opus frame to the server.
func (c *Client) SendAudio(opusData []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.dead {
		return fmt.Errorf("client closed")
	}
	if err := c.conn.WriteMessage(websocket.BinaryMessage, opusData); err != nil {
		c.markDeadLocked()
		return err
	}
	return nil
}

// SendText sends a text message to the server (for LLM-only path).
func (c *Client) SendText(text string) error {
	return c.writeJSON(map[string]interface{}{
		"type":       "text",
		"text":       text,
		"session_id": c.sessionID,
	})
}

// SendAbortSpeaking asks the server to stop TTS (xiaozhi-esp32 AbortSpeaking).
// reason may be "" or "wake_word_detected".
func (c *Client) SendAbortSpeaking(reason string) error {
	msg := map[string]interface{}{
		"type":       "abort",
		"session_id": c.sessionID,
	}
	if reason != "" {
		msg["reason"] = reason
	}
	return c.writeJSON(msg)
}

// SendGoodbye notifies the server before closing the channel (ESP32-style).
func (c *Client) SendGoodbye() error {
	return c.writeJSON(map[string]interface{}{
		"type":       "goodbye",
		"session_id": c.sessionID,
	})
}

// STTCh returns the channel for STT text results from the server.
func (c *Client) STTCh() <-chan string { return c.sttCh }

// TTSCh returns the channel for TTS state/sentence events.
func (c *Client) TTSCh() <-chan ServerMessage { return c.ttsCh }

// LLMCh returns the channel for LLM text events.
func (c *Client) LLMCh() <-chan ServerMessage { return c.llmCh }

// AudioCh returns the channel for binary Opus audio frames (TTS audio from server).
func (c *Client) AudioCh() <-chan []byte { return c.audioCh }

// AudioRXStats returns WSS binary TTS frames received and how many were dropped
// from the queue (oldest discarded when full). Non-zero drops can eat TTS head.
func (c *Client) AudioRXStats() (received, dropped uint32) {
	return atomic.LoadUint32(&c.rxAudioN), atomic.LoadUint32(&c.rxAudioDropped)
}

// MCPCh returns the channel for MCP tool-call requests.
func (c *Client) MCPCh() <-chan ServerMessage { return c.mcpCh }

// GoodbyeCh returns the channel signalling server goodbye.
func (c *Client) GoodbyeCh() <-chan struct{} { return c.goodbyeCh }

// ErrCh returns the error channel.
func (c *Client) ErrCh() <-chan error { return c.errCh }

// SendMCPResult sends an MCP JSON-RPC result in the ESP32 envelope:
// {"type":"mcp","session_id":"...","payload":{"jsonrpc":"2.0","id":N,"result":...}}
func (c *Client) SendMCPResult(id interface{}, result interface{}) error {
	return c.writeJSON(map[string]interface{}{
		"session_id": c.sessionID,
		"type":       MsgTypeMCP,
		"payload": map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      id,
			"result":  result,
		},
	})
}

// --- internal ---

func (c *Client) writeJSON(v interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.dead {
		return fmt.Errorf("client closed")
	}
	if err := c.conn.WriteJSON(v); err != nil {
		c.markDeadLocked()
		return err
	}
	return nil
}

func (c *Client) readServerHello(ctx context.Context) error {
	doneCh := make(chan error, 1)
	go func() {
		for {
			mt, msg, err := c.conn.ReadMessage()
			if err != nil {
				doneCh <- err
				return
			}
			if mt != websocket.TextMessage {
				continue
			}
			var sm map[string]interface{}
			if err := json.Unmarshal(msg, &sm); err != nil {
				doneCh <- fmt.Errorf("hello JSON: %w", err)
				return
			}
			if tp, _ := sm["type"].(string); tp == MsgTypeHello {
				if sid, _ := sm["session_id"].(string); sid != "" {
					c.sessionID = sid
				}
				doneCh <- nil
				return
			}
		}
	}()
	select {
	case err := <-doneCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) readLoop(ctx context.Context) {
	defer func() {
		c.mu.Lock()
		c.markDeadLocked()
		c.mu.Unlock()
		select {
		case c.errCh <- fmt.Errorf("read loop ended"):
		default:
		}
	}()
	for {
		mt, msg, err := c.conn.ReadMessage()
		if err != nil {
			c.mu.Lock()
			c.markDeadLocked()
			c.mu.Unlock()
			select {
			case c.errCh <- err:
			default:
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		default:
		}

		if mt == websocket.BinaryMessage {
			_ = atomic.AddUint32(&c.rxAudioN, 1)
			frame := make([]byte, len(msg))
			copy(frame, msg)
			select {
			case c.audioCh <- frame:
			default:
				// Consumer slow: drop oldest (can eat TTS head if it happens).
				select {
				case <-c.audioCh:
				default:
				}
				_ = atomic.AddUint32(&c.rxAudioDropped, 1)
				select {
				case c.audioCh <- frame:
				default:
				}
			}
			continue
		}

		if mt != websocket.TextMessage {
			continue
		}

		var sm ServerMessage
		if err := json.Unmarshal(msg, &sm); err != nil {
			continue
		}

		switch sm.Type {
		case MsgTypeSTT:
			if sm.Text != "" {
				select {
				case c.sttCh <- sm.Text:
				default:
				}
			}
		case MsgTypeTTS:
			select {
			case c.ttsCh <- sm:
			default:
			}
		case MsgTypeLLM:
			select {
			case c.llmCh <- sm:
			default:
			}
		case MsgTypeMCP:
			// ESP32 servers nest JSON-RPC under "payload"; also accept flat method/params.
			normalizeMCPMessage(msg, &sm)
			select {
			case c.mcpCh <- sm:
			default:
			}
		case MsgTypeGoodbye:
			select {
			case c.goodbyeCh <- struct{}{}:
			default:
			}
		case MsgTypeError:
			errMsg := sm.Error
			if errMsg == "" {
				errMsg = sm.Text
			}
			if errMsg == "" {
				errMsg = "server error"
			}
			select {
			case c.errCh <- fmt.Errorf("server: %s", errMsg):
			default:
			}
		}
	}
}

func buildWSHeaders(cfg Config) http.Header {
	h := http.Header{}
	h.Set("Protocol-Version", fmt.Sprintf("%d", cfg.ProtocolVersion))
	if cfg.DeviceID != "" {
		h.Set("Device-Id", cfg.DeviceID)
	}
	if cfg.ClientID != "" {
		h.Set("Client-Id", cfg.ClientID)
	}
	if cfg.Token != "" {
		tok := strings.TrimSpace(cfg.Token)
		if !strings.HasPrefix(tok, "Bearer ") {
			tok = "Bearer " + tok
		}
		h.Set("Authorization", tok)
	}
	return h
}

func buildHelloEvent(cfg Config) map[string]interface{} {
	return map[string]interface{}{
		"type":      "hello",
		"version":   cfg.ProtocolVersion,
		"transport": "websocket",
		"features": map[string]interface{}{
			"mcp": true,
			"aec": false,
		},
		"audio_params": map[string]interface{}{
			"format":         "opus",
			"sample_rate":    16000,
			"channels":       1,
			"frame_duration": 60,
		},
	}
}

// normalizeMCPMessage lifts JSON-RPC fields from payload into ServerMessage.
func normalizeMCPMessage(raw []byte, sm *ServerMessage) {
	var env struct {
		ID      interface{}            `json:"id"`
		Method  string                 `json:"method"`
		Params  map[string]interface{} `json:"params"`
		Payload *struct {
			ID     interface{}            `json:"id"`
			Method string                 `json:"method"`
			Params map[string]interface{} `json:"params"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return
	}
	if env.Payload != nil {
		if env.Payload.Method != "" {
			sm.Method = env.Payload.Method
		}
		if env.Payload.ID != nil {
			sm.ID = env.Payload.ID
		}
		if env.Payload.Params != nil {
			sm.Params = env.Payload.Params
		}
		return
	}
	if env.Method != "" {
		sm.Method = env.Method
	}
	if env.ID != nil {
		sm.ID = env.ID
	}
	if env.Params != nil {
		sm.Params = env.Params
	}
}
