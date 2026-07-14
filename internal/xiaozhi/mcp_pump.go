package xiaozhi

import (
	"context"
	"time"

	"github.com/digital-dream-labs/vector-cloud/internal/log"
)

// runMCPPump answers MCP JSON-RPC for the life of the WSS connection (ESP32-style).
// Turn no longer exclusively owns mcpCh — without this pump, silence STT timeouts and
// DrainEventChannels used to drop initialize/tools/list so the LLM claimed "no camera".
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
