package xiaozhi

import (
	"context"
	"strings"
	"time"

	"github.com/digital-dream-labs/vector-cloud/internal/log"
)

// runMCPPump answers MCP JSON-RPC for the life of the WSS connection (ESP32-style).
// tools/call runs in a goroutine so notifications/cancelled can be handled while
// analyze_photo is still capturing (server cancels slow tools; otherwise TTS never comes).
func runMCPPump(ctx context.Context, c *Client, h *MCPHandler) {
	if c == nil || h == nil {
		return
	}
	log.Println("[Xiaozhi] MCP pump started; session:", c.SessionID())
	for {
		select {
		case <-ctx.Done():
			log.Println("[Xiaozhi] MCP pump stopped; session:", c.SessionID())
			return
		case msg, ok := <-c.MCPCh():
			if !ok {
				return
			}
			method := msg.Method
			if method == "" && msg.Params != nil {
				if m, ok := msg.Params["name"].(string); ok {
					method = "tools/call:" + m
				}
			}
			if method != "" {
				log.Println("[Xiaozhi] MCP pump ←", method)
			}

			if method == "notifications/cancelled" {
				// If analyze_photo is still running, keep await — Explain often finishes
				// right after cancel and we still need to stream analysis TTS.
				if AnalyzeInFlight() {
					log.Println("[Xiaozhi] MCP cancelled by server — analyze still in flight, keep await")
				} else {
					log.Println("[Xiaozhi] MCP cancelled by server — abandon post-camera await")
					AbandonPostCaptureAwait("mcp_notifications_cancelled")
				}
				continue
			}

			if method == "tools/call" || strings.HasPrefix(method, "tools/call:") {
				m := msg
				go func() {
					intent, err := h.ProcessMessage(m)
					if err != nil {
						log.Println("[Xiaozhi] MCP tools/call error:", err)
						return
					}
					if intent == "" {
						return
					}
					act := h.TakePendingAction()
					if act.Intent == "" {
						act.Intent = intent
					}
					NoteMCPIntentQueued(act.Intent, act.Params)
					log.Println("[Xiaozhi] MCP action queued (await TTS confirmation):", act.Intent)
				}()
				continue
			}

			intent, err := h.ProcessMessage(msg)
			if err != nil {
				log.Println("[Xiaozhi] MCP pump error:", err)
				continue
			}
			if intent == "" {
				continue
			}
			act := h.TakePendingAction()
			if act.Intent == "" {
				act.Intent = intent
			}
			NoteMCPIntentQueued(act.Intent, act.Params)
			log.Println("[Xiaozhi] MCP action queued (await TTS confirmation):", act.Intent)
		}
	}
}

// waitMCPHandshake waits until tools/list has been answered (initialize usually comes first).
func waitMCPHandshake(ctx context.Context, h *MCPHandler, timeout time.Duration) {
	if h == nil {
		return
	}
	if h.ToolsListed() {
		log.Println("[Xiaozhi] MCP handshake already complete (tools listed)")
		return
	}
	wctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := h.WaitToolsListed(wctx); err != nil {
		log.Println("[Xiaozhi] MCP handshake incomplete (tools/list not yet answered):", err)
		return
	}
	log.Println("[Xiaozhi] MCP handshake OK (initialize + tools/list)")
}
