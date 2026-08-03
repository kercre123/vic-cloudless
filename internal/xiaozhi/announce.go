package xiaozhi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/digital-dream-labs/vector-cloud/internal/log"
)

// ChessAnnouncePath is written by wired after each chess move.
// Payload is JSON: {mode,say,prompt,summary} or plain text (legacy SayText).
const ChessAnnouncePath = "/run/vic-cloud/chess-announce"

var (
	chessAnnounceOnce sync.Once
	chessAnnounceBusy int32

	speakTextFn func(text string) error
)

type chessAnnounceMsg struct {
	Mode    string `json:"mode"`
	Say     string `json:"say"`
	Prompt  string `json:"prompt"`
	Summary string `json:"summary"`
}

// SetSpeakText registers engine SayText (Acapela robot voice) for chess comments.
func SetSpeakText(fn func(text string) error) {
	speakTextFn = fn
}

// StartChessAnnounceWatcher polls for wired chess comments and speaks them.
func StartChessAnnounceWatcher() {
	chessAnnounceOnce.Do(func() {
		go chessAnnounceLoop()
		log.Println("[Xiaozhi] chess announce watcher started:", ChessAnnouncePath)
	})
}

func chessAnnounceLoop() {
	for {
		time.Sleep(350 * time.Millisecond)
		b, err := os.ReadFile(ChessAnnouncePath)
		if err != nil {
			continue
		}
		raw := strings.TrimSpace(string(b))
		_ = os.Remove(ChessAnnouncePath)
		if raw == "" {
			continue
		}
		msg := parseChessAnnounce(raw)

		// Xiaozhi Conversation / Google VI: may interrupt an idle listen so a move can comment.
		if (msg.Mode == "xiaozhi" || msg.Mode == "google_vi") && Enabled() {
			if IsBusy() {
				if InListenTurn() && atomic.LoadInt32(&chessAnnounceBusy) == 0 {
					InterruptTurn("chess_comment")
					deadline := time.Now().Add(2 * time.Second)
					for IsBusy() && time.Now().Before(deadline) {
						time.Sleep(50 * time.Millisecond)
					}
				}
				if IsBusy() {
					_ = os.WriteFile(ChessAnnouncePath, []byte(raw), 0644)
					continue
				}
			}
		} else if IsBusy() {
			_ = os.WriteFile(ChessAnnouncePath, []byte(raw), 0644)
			continue
		}

		if !atomic.CompareAndSwapInt32(&chessAnnounceBusy, 0, 1) {
			_ = os.WriteFile(ChessAnnouncePath, []byte(raw), 0644)
			continue
		}
		go func(m chessAnnounceMsg) {
			defer atomic.StoreInt32(&chessAnnounceBusy, 0)
			if err := RunChessAnnounceMsg(m); err != nil {
				log.Println("[Xiaozhi] chess announce:", err)
			}
		}(msg)
	}
}

func parseChessAnnounce(raw string) chessAnnounceMsg {
	var m chessAnnounceMsg
	if strings.HasPrefix(raw, "{") {
		if err := json.Unmarshal([]byte(raw), &m); err == nil {
			m.Mode = strings.ToLower(strings.TrimSpace(m.Mode))
			m.Say = strings.TrimSpace(m.Say)
			m.Prompt = strings.TrimSpace(m.Prompt)
			m.Summary = strings.TrimSpace(m.Summary)
			if m.Say == "" && m.Prompt != "" {
				m.Say = m.Prompt
			}
			if m.Mode == "" {
				m.Mode = "saytext"
			}
			return m
		}
	}
	return chessAnnounceMsg{Mode: "saytext", Say: raw}
}

// RunChessAnnounceMsg dispatches SayText, Google VI TTS, or Xiaozhi conversation mode.
func RunChessAnnounceMsg(m chessAnnounceMsg) error {
	switch m.Mode {
	case "google_vi":
		if GameGoogleTTSVIEnabled() {
			return RunGoogleViAnnounce(m.Say)
		}
		return RunChessAnnounce(m.Say)
	case "xiaozhi":
		if Enabled() {
			if err := ensureChessXiaozhiSession(); err != nil {
				log.Println("[Xiaozhi][Chess] ensure session:", err)
			}
			if err := RunChessAnnounce(m.Say); err != nil {
				return err
			}
			relistenAfterChess()
			return nil
		}
		return RunChessAnnounce(m.Say)
	default:
		return RunChessAnnounce(m.Say)
	}
}

func relistenAfterChess() {
	if !ContinuousMode() || InBlackjackGameMode() {
		return
	}
	if !SessionAlive() {
		return
	}
	DisarmRelistenPending()
	time.Sleep(250 * time.Millisecond)
	if rerr := TriggerRelisten(); rerr != nil {
		log.Println("[Xiaozhi][Chess] relisten:", rerr)
	} else {
		log.Println("[Xiaozhi][Chess] mic reopened after chess comment")
	}
}

// ensureChessXiaozhiSession opens or reuses the Xiaozhi WSS so follow-up questions
// share MCP chess tools / conversation context — without stealing the mic path.
func ensureChessXiaozhiSession() error {
	cfg := GetConfig()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client, err := EnsureConnected(ctx, cfg)
	if err != nil {
		return err
	}
	if mcp := SessionMCP(); mcp != nil && !mcp.ToolsListed() {
		waitMCPHandshake(ctx, mcp, 2*time.Second)
	}
	MarkActivity()
	log.Printf("[Xiaozhi][Chess] session ready: %s (conversation mode)", client.SessionID())
	return nil
}

// RunChessAnnounce speaks with Vector Acapela SayText only (no Google TTS).
func RunChessAnnounce(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	runes := []rune(text)
	if len(runes) > 180 {
		text = string(runes[:180])
	}
	if speakTextFn == nil {
		return fmt.Errorf("SayText callback not registered")
	}
	log.Printf("[Xiaozhi][Chess] announce SayText: %q", text)
	DisarmRelistenPending()
	if err := speakTextFn(text); err != nil {
		return err
	}
	log.Println("[Xiaozhi][Chess] announce done (SayText)")
	return nil
}
