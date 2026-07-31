package xiaozhi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/digital-dream-labs/vector-cloud/internal/log"
)

// PendingAction is a Vector engine intent queued by an MCP tool call.
type PendingAction struct {
	Intent string
	Params map[string]string
}

// MCPHandler processes JSON-RPC MCP tool calls from the Xiaozhi server
// (ESP32-style self-control) and exposes pending robot intents for the voice layer.
type MCPHandler struct {
	client        *Client
	pendingMu     sync.Mutex
	pendingAction PendingAction
	intentsReady  int32

	// Handshake tracking (initialize + tools/list answered at least once).
	hsMu       sync.Mutex
	initDone   bool
	toolsDone  bool
	toolsReady chan struct{} // closed when tools/list has been answered
}

// NewMCPHandler creates an MCPHandler attached to the given client.
func NewMCPHandler(c *Client) *MCPHandler {
	return &MCPHandler{
		client:     c,
		toolsReady: make(chan struct{}),
	}
}

func (h *MCPHandler) markInitDone() {
	h.hsMu.Lock()
	h.initDone = true
	h.hsMu.Unlock()
}

func (h *MCPHandler) markToolsDone() {
	h.hsMu.Lock()
	defer h.hsMu.Unlock()
	if h.toolsDone {
		return
	}
	h.toolsDone = true
	close(h.toolsReady)
}

// ToolsListed reports whether tools/list has been answered on this handler.
func (h *MCPHandler) ToolsListed() bool {
	h.hsMu.Lock()
	defer h.hsMu.Unlock()
	return h.toolsDone
}

// WaitToolsListed blocks until tools/list has been answered or ctx is done.
func (h *MCPHandler) WaitToolsListed(ctx context.Context) error {
	if h.ToolsListed() {
		return nil
	}
	select {
	case <-h.toolsReady:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TakePendingAction retrieves (and clears) any pending robot action.
func (h *MCPHandler) TakePendingAction() PendingAction {
	if atomic.LoadInt32(&h.intentsReady) == 0 {
		return PendingAction{}
	}
	h.pendingMu.Lock()
	defer h.pendingMu.Unlock()
	a := h.pendingAction
	h.pendingAction = PendingAction{}
	atomic.StoreInt32(&h.intentsReady, 0)
	return a
}

// TakePendingIntent is kept for callers that only need the intent name.
func (h *MCPHandler) TakePendingIntent() string {
	return h.TakePendingAction().Intent
}

func (h *MCPHandler) setPending(intent string, params map[string]string) {
	if intent == "" {
		return
	}
	h.pendingMu.Lock()
	h.pendingAction = PendingAction{Intent: intent, Params: params}
	h.pendingMu.Unlock()
	atomic.StoreInt32(&h.intentsReady, 1)
}

// ProcessMessage dispatches a server MCP message. Returns any robot intent to fire.
func (h *MCPHandler) ProcessMessage(msg ServerMessage) (string, error) {
	switch msg.Method {
	case "initialize":
		return "", h.handleInitialize(msg)
	case "tools/list":
		return "", h.HandleToolsList(msg)
	case "tools/call":
		return h.HandleToolCall(msg)
	case "notifications/initialized":
		return "", nil
	case "notifications/cancelled":
		if AnalyzeInFlight() {
			log.Println("[Xiaozhi] MCP notifications/cancelled — analyze in flight, keep await")
		} else {
			AbandonPostCaptureAwait("mcp_notifications_cancelled")
			log.Println("[Xiaozhi] MCP notifications/cancelled — cleared post-camera wait")
		}
		return "", nil
	default:
		_ = h.sendResult(msg.ID, map[string]interface{}{})
		return "", nil
	}
}

func (h *MCPHandler) handleInitialize(msg ServerMessage) error {
	url, token := parseVisionCaps(msg.Params)
	SetVisionExplain(url, token)
	if url == "" {
		log.Println("[Xiaozhi] MCP initialize: no capabilities.vision (analyze_photo will fail until set)")
	}

	result := map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities": map[string]interface{}{
			"tools": map[string]interface{}{},
		},
		"serverInfo": map[string]interface{}{
			"name":    "vector-wireos",
			"version": "1.0.0",
		},
	}
	if err := h.sendResult(msg.ID, result); err != nil {
		return err
	}
	h.markInitDone()
	log.Println("[Xiaozhi] MCP ← initialize → replied (serverInfo=vector-wireos)")
	return nil
}

// HandleToolsList responds to tools/list with ESP32-style self.* tools.
func (h *MCPHandler) HandleToolsList(msg ServerMessage) error {
	tools := []map[string]interface{}{
		toolDef("self.get_device_status",
			"Get Anki Vector device status (platform, firmware). Call when user asks about the robot status.",
			nil, nil),
		toolDef("self.audio_speaker.set_volume",
			"Set Vector master speaker volume. Pass 0-100 percent. "+
				"0=mute; 1-20=VOLUME_1; 21-40=VOLUME_2; 41-60=VOLUME_3; 61-80=VOLUME_4; 81-100=VOLUME_5. "+
				"Use when user asks to change volume / âm lượng / mute / to nhỏ tiếng.",
			map[string]interface{}{
				"volume": map[string]interface{}{
					"type":        "number",
					"description": "Volume 0-100 (mapped to 5 robot steps)",
					"minimum":     0,
					"maximum":     100,
				},
			},
			[]string{"volume"}),
		toolDef("self.camera.take_photo",
			"Take a photo and SAVE it to Vector's photo album only. Use for chụp ảnh / take a photo / picture when the user only wants a saved photo (not a description). Do NOT use this to answer what you see — use self.camera.analyze_photo instead. Set selfie=true for a selfie.",
			map[string]interface{}{
				"selfie": map[string]interface{}{
					"type":        "boolean",
					"description": "true = selfie (face the user); false/omit = scene photo",
				},
			},
			nil),
		toolDef("self.camera.analyze_photo",
			"Always remember you have a camera. Capture a NEW photo and send it to Xiaozhi vision for analysis, then describe what you see. "+
				"Use for: bạn thấy gì / mày thấy gì / what do you see / nhìn xem / chụp hình phân tích / describe the scene / phân tích ảnh. "+
				"Do NOT use self.camera.take_photo for these questions. Pass a clear `question` about the image.",
			map[string]interface{}{
				"question": map[string]interface{}{
					"type":        "string",
					"description": "What to ask about the photo, e.g. What do you see? / Bạn thấy gì trước mặt?",
				},
			},
			[]string{"question"}),
		toolDef("self.vector.action",
			vectorActionToolDescription(),
			map[string]interface{}{
				"action": map[string]interface{}{
					"type":        "string",
					"description": "Action id (see tool description). Examples: fireworks, dance, go_home, sleep, look_at_me, fistbump",
					"enum":        vectorActionEnum(),
				},
			},
			[]string{"action"}),
		toolDef("self.vector.stop",
			"Stop current Vector behavior / be quiet. Call for dừng / stop / im lặng / be quiet / shut up.",
			nil, nil),
		toolDef("self.vector.set_eye_color",
			"Change Vector's eye color preset. Call when user asks to đổi màu mắt / change eye color / eyes to purple/blue/teal/etc. Use color=next to cycle to the next preset.",
			map[string]interface{}{
				"color": map[string]interface{}{
					"type":        "string",
					"description": "Eye color preset id",
					"enum":        eyeColorEnum(),
				},
			},
			[]string{"color"}),
	}
	if err := h.sendResult(msg.ID, map[string]interface{}{"tools": tools}); err != nil {
		return err
	}
	h.markToolsDone()
	log.Printf("[Xiaozhi] MCP ← tools/list → replied (%d tools)", len(tools))
	return nil
}

func toolDef(name, desc string, props map[string]interface{}, required []string) map[string]interface{} {
	if props == nil {
		props = map[string]interface{}{}
	}
	schema := map[string]interface{}{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return map[string]interface{}{
		"name":        name,
		"description": desc,
		"inputSchema": schema,
	}
}

// HandleToolCall runs a tools/call and may queue a Vector engine intent.
func (h *MCPHandler) HandleToolCall(msg ServerMessage) (string, error) {
	toolName := ""
	args := map[string]interface{}{}
	if msg.Params != nil {
		if n, ok := msg.Params["name"].(string); ok {
			toolName = n
		}
		if a, ok := msg.Params["arguments"].(map[string]interface{}); ok {
			args = a
		}
	}

	var intent string
	var params map[string]string
	var text string
	isError := false
	markPostCaptureDone := false

	switch toolName {
	case "self.get_device_status":
		text = `{"platform":"vector","status":"ok","firmware":"wire-os"}`

	case "self.audio_speaker.set_volume":
		vol := 50
		if v, ok := args["volume"].(float64); ok {
			vol = int(v)
		}
		if vol < 0 {
			vol = 0
		}
		if vol > 100 {
			vol = 100
		}
		level := percentToVolumeLevel(vol)
		intent = "intent_imperative_volumelevel_extend"
		// Engine BehaviorVolume only accepts VOLUME_1..5 / min|low|medium|high|max — not raw %.
		params = map[string]string{"volume_level": level}
		text = fmt.Sprintf(`{"status":"ok","volume":%d,"level":%q}`, vol, level)
		log.Printf("[Xiaozhi] MCP set_volume %d%% → %s", vol, level)

	case "self.camera.take_photo":
		// Engine take_a_photo REQUIRES empty_or_selfie via entity_photo_selfie.
		// Sending {} is treated as "no params" and the intent is dropped (MissingParams).
		intent = "intent_photo_take_extend"
		selfieVal := ""
		if s, ok := args["selfie"].(bool); ok && s {
			selfieVal = "photo_selfie"
		}
		params = map[string]string{"entity_photo_selfie": selfieVal}
		text = fmt.Sprintf(`{"status":"ok","intent":"intent_photo_take_extend","selfie":%t}`, selfieVal != "")

	case "self.camera.analyze_photo":
		question, _ := args["question"].(string)
		question = strings.TrimSpace(question)
		if question == "" {
			question = "What do you see?"
		}
		if !HasVisionExplain() {
			isError = true
			text = `{"error":"vision Explain URL not configured — Xiaozhi server did not send capabilities.vision"}`
			log.Println("[Xiaozhi] MCP analyze_photo failed: no vision URL")
		} else {
			result, err := AnalyzeScene(context.Background(), question)
			if err != nil {
				isError = true
				text = fmt.Sprintf(`{"error":%q}`, err.Error())
				log.Println("[Xiaozhi] MCP analyze_photo error:", err)
			} else {
				// Plain analysis text for the LLM to speak (ESP32 Explain style).
				text = result
				markPostCaptureDone = true
				log.Println("[Xiaozhi] MCP self.camera.analyze_photo OK")
			}
		}

	case "self.vector.stop":
		intent = "intent_imperative_quiet"
		text = `{"status":"ok","intent":"intent_imperative_quiet"}`

	case "self.vector.set_eye_color":
		color, _ := args["color"].(string)
		color = strings.TrimSpace(strings.ToLower(color))
		if ec, ok := lookupEyeColor(color); ok {
			intent = ec.Intent
			params = ec.Params
			text = fmt.Sprintf(`{"status":"ok","color":%q,"intent":%q}`, ec.ID, ec.Intent)
		} else {
			isError = true
			text = fmt.Sprintf(`{"error":"unknown eye color: %s"}`, color)
		}

	case "self.vector.action":
		action, _ := args["action"].(string)
		action = strings.TrimSpace(strings.ToLower(action))
		if a, ok := lookupVectorAction(action); ok {
			intent = a.Intent
			params = a.Params
			text = fmt.Sprintf(`{"status":"ok","action":%q,"intent":%q}`, a.ID, a.Intent)
		} else {
			isError = true
			text = fmt.Sprintf(`{"error":"unknown action: %s"}`, action)
		}

	// Backward-compatible alias from earlier builds.
	case "self.vector.play_fistbump":
		intent = "intent_play_fistbump"
		text = `{"status":"ok","intent":"intent_play_fistbump"}`

	default:
		isError = true
		text = fmt.Sprintf(`{"error":"unknown tool: %s"}`, toolName)
	}

	result := map[string]interface{}{
		"content": []map[string]interface{}{
			{"type": "text", "text": text},
		},
		"isError": isError,
	}
	// Unblock analysis Opus before the WS reply — server may push audio immediately.
	if markPostCaptureDone {
		MarkPostCaptureToolDone()
	}
	if err := h.sendResult(msg.ID, result); err != nil {
		return "", err
	}
	if intent != "" && !isError {
		h.setPending(intent, params)
		if len(params) > 0 {
			log.Printf("[Xiaozhi] MCP %s → intent %s params %v", toolName, intent, params)
		} else {
			log.Printf("[Xiaozhi] MCP %s → intent %s", toolName, intent)
		}
		if intent == "intent_play_keepaway" || intent == "intent_play_popawheelie" {
			log.Printf("[Xiaozhi][KeepawayFlow] step=mcp_queued intent=%s (await TTS, then same-stream OnIntent — no FakeTrigger/mic)", intent)
		}
		return intent, nil
	}
	return "", nil
}

func (h *MCPHandler) sendResult(id interface{}, result interface{}) error {
	if h.client == nil {
		return nil
	}
	return h.client.SendMCPResult(id, result)
}

// --- Vector action catalogue (MCP action id → Vector intent_*) ---
// No STT keyword/alias matching — LLM must call self.vector.action with an action id.

type vectorActionDef struct {
	ID     string
	Intent string
	Params map[string]string
	Hint   string
}

var vectorActions = []vectorActionDef{
	{ID: "fireworks", Intent: "intent_seasonal_happynewyear", Hint: "pháo hoa / fireworks / chúc mừng năm mới"},
	{ID: "happy_holidays", Intent: "intent_seasonal_happyholidays", Hint: "christmas / ngày lễ"},
	{ID: "dance", Intent: "intent_imperative_dance", Hint: "dance / nhảy múa"},
	{ID: "go_home", Intent: "intent_system_charger", Hint: "go home / về sạc"},
	{ID: "sleep", Intent: "intent_system_sleep", Hint: "sleep / đi ngủ"},
	{ID: "explore", Intent: "intent_explore_start", Hint: "explore / đi dạo"},
	{ID: "look_at_me", Intent: "intent_imperative_lookatme", Hint: "look at me / nhìn tôi"},
	{ID: "come_here", Intent: "intent_imperative_come", Hint: "come here / lại đây"},
	{ID: "forward", Intent: "intent_imperative_forward", Hint: "forward / tiến lên"},
	{ID: "backup", Intent: "intent_imperative_backup", Hint: "backup / lùi"},
	{ID: "turn_left", Intent: "intent_imperative_turnleft", Hint: "turn left / rẽ trái"},
	{ID: "turn_right", Intent: "intent_imperative_turnright", Hint: "turn right / rẽ phải"},
	{ID: "turn_around", Intent: "intent_imperative_turnaround", Hint: "turn around / quay lại"},
	{ID: "fistbump", Intent: "intent_play_fistbump", Hint: "fistbump / cụng tay"},
	{ID: "roll_cube", Intent: "intent_play_rollcube", Hint: "roll cube"},
	{ID: "pop_a_wheelie", Intent: "intent_play_popawheelie", Hint: "pop a wheelie / bốc đầu / bóc đầu / bốc đầu xe / wheelie / dựng đầu (cần cube)"},
	{ID: "pickup_cube", Intent: "intent_play_pickupcube", Hint: "pick up cube"},
	{ID: "fetch_cube", Intent: "intent_imperative_fetchcube", Hint: "fetch cube"},
	{ID: "find_cube", Intent: "intent_imperative_findcube", Hint: "find cube"},
	{ID: "keepaway", Intent: "intent_play_keepaway", Hint: "keep away / keepaway / giữ cube / chơi keep away (cần cube)"},
	{ID: "blackjack", Intent: "intent_play_blackjack", Hint: "blackjack"},
	{ID: "do_a_trick", Intent: "intent_play_anytrick", Hint: "do a trick"},
	{ID: "good_robot", Intent: "intent_imperative_praise", Hint: "praise / giỏi quá"},
	{ID: "bad_robot", Intent: "intent_imperative_abuse", Hint: "scold / bad robot"},
	{ID: "love_you", Intent: "intent_imperative_love", Hint: "i love you"},
	{ID: "hello", Intent: "intent_greeting_hello", Hint: "hello / xin chào"},
	{ID: "good_morning", Intent: "intent_greeting_goodmorning", Hint: "good morning"},
	{ID: "good_night", Intent: "intent_greeting_goodnight", Hint: "good night"},
	{ID: "goodbye", Intent: "intent_greeting_goodbye", Hint: "goodbye / tạm biệt"},
	{ID: "what_time", Intent: "intent_clock_time", Hint: "what time / mấy giờ"},
	{ID: "how_old", Intent: "intent_character_age", Hint: "how old"},
	{ID: "my_name", Intent: "intent_names_ask", Hint: "who am i"},
	{ID: "volume_up", Intent: "intent_imperative_volumeup", Hint: "volume up"},
	{ID: "volume_down", Intent: "intent_imperative_volumedown", Hint: "volume down"},
	{ID: "shut_up", Intent: "intent_imperative_shutup", Hint: "shut up"},
	// Eye color shortcuts (same intents as self.vector.set_eye_color).
	{ID: "eye_teal", Intent: "intent_imperative_eyecolor_specific_extend", Params: map[string]string{"eye_color": "COLOR_TEAL"}, Hint: "eyes teal"},
	{ID: "eye_orange", Intent: "intent_imperative_eyecolor_specific_extend", Params: map[string]string{"eye_color": "COLOR_ORANGE"}, Hint: "eyes orange"},
	{ID: "eye_yellow", Intent: "intent_imperative_eyecolor_specific_extend", Params: map[string]string{"eye_color": "COLOR_YELLOW"}, Hint: "eyes yellow"},
	{ID: "eye_green", Intent: "intent_imperative_eyecolor_specific_extend", Params: map[string]string{"eye_color": "COLOR_GREEN"}, Hint: "eyes green / lime"},
	{ID: "eye_blue", Intent: "intent_imperative_eyecolor_specific_extend", Params: map[string]string{"eye_color": "COLOR_BLUE"}, Hint: "eyes blue / sapphire"},
	{ID: "eye_purple", Intent: "intent_imperative_eyecolor_specific_extend", Params: map[string]string{"eye_color": "COLOR_PURPLE"}, Hint: "eyes purple"},
	{ID: "eye_rainbow", Intent: "intent_imperative_eyecolor_specific_extend", Params: map[string]string{"eye_color": "COLOR_RAINBOW"}, Hint: "eyes rainbow"},
	{ID: "eye_next", Intent: "intent_imperative_eyecolor", Hint: "cycle eye color / đổi màu mắt"},
}

// --- Eye color presets (voice COLOR_* strings engine understands) ---

type eyeColorDef struct {
	ID     string
	Intent string
	Params map[string]string
}

var eyeColors = []eyeColorDef{
	{ID: "teal", Intent: "intent_imperative_eyecolor_specific_extend", Params: map[string]string{"eye_color": "COLOR_TEAL"}},
	{ID: "orange", Intent: "intent_imperative_eyecolor_specific_extend", Params: map[string]string{"eye_color": "COLOR_ORANGE"}},
	{ID: "yellow", Intent: "intent_imperative_eyecolor_specific_extend", Params: map[string]string{"eye_color": "COLOR_YELLOW"}},
	{ID: "green", Intent: "intent_imperative_eyecolor_specific_extend", Params: map[string]string{"eye_color": "COLOR_GREEN"}},
	{ID: "blue", Intent: "intent_imperative_eyecolor_specific_extend", Params: map[string]string{"eye_color": "COLOR_BLUE"}},
	{ID: "purple", Intent: "intent_imperative_eyecolor_specific_extend", Params: map[string]string{"eye_color": "COLOR_PURPLE"}},
	{ID: "rainbow", Intent: "intent_imperative_eyecolor_specific_extend", Params: map[string]string{"eye_color": "COLOR_RAINBOW"}},
	{ID: "next", Intent: "intent_imperative_eyecolor"},
}

func lookupEyeColor(id string) (eyeColorDef, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	// Common aliases from speech / LLM.
	switch id {
	case "sapphire", "xanh dương", "xanh-duong":
		id = "blue"
	case "lime", "xanh lá", "xanh la":
		id = "green"
	case "tím", "tim":
		id = "purple"
	case "cam":
		id = "orange"
	case "vàng", "vang":
		id = "yellow"
	case "xanh ngọc", "xanh ngoc", "cyan":
		id = "teal"
	case "cycle", "đổi", "doi", "change":
		id = "next"
	}
	for _, c := range eyeColors {
		if c.ID == id {
			return c, true
		}
	}
	return eyeColorDef{}, false
}

func eyeColorEnum() []string {
	out := make([]string, 0, len(eyeColors))
	for _, c := range eyeColors {
		out = append(out, c.ID)
	}
	return out
}

func lookupVectorAction(id string) (vectorActionDef, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, a := range vectorActions {
		if a.ID == id {
			return a, true
		}
	}
	return vectorActionDef{}, false
}

func vectorActionEnum() []string {
	out := make([]string, 0, len(vectorActions))
	for _, a := range vectorActions {
		out = append(out, a.ID)
	}
	return out
}

func vectorActionToolDescription() string {
	var b strings.Builder
	b.WriteString("Perform a built-in Anki Vector behavior (device self-control, like ESP32 self.* tools). ")
	b.WriteString("Call this when the user wants the robot to DO something — do not only chat. ")
	b.WriteString("action values: ")
	parts := make([]string, 0, len(vectorActions))
	for _, a := range vectorActions {
		parts = append(parts, a.ID+" ("+a.Hint+")")
	}
	b.WriteString(strings.Join(parts, "; "))
	b.WriteString(". ALWAYS call this tool when the user asks Vector to do a physical action ")
	b.WriteString("(dance, fireworks/pháo hoa, go home, sleep, look at me, fistbump, move, play, greetings). ")
	b.WriteString("Do NOT only describe the action in chat — invoke the tool. ")
	b.WriteString("Examples: fireworks → fireworks; dance → dance; go home → go_home; ")
	b.WriteString("bốc đầu/bóc đầu/wheelie → pop_a_wheelie; keep away/keepaway → keepaway. ")
	b.WriteString("For eye color prefer self.vector.set_eye_color (or action eye_purple / eye_next).")
	return b.String()
}

// percentToVolumeLevel maps Xiaozhi/ESP-style 0–100% onto Vector master_volume.
// 0 → mute (proto Volume::MUTE). 1–100 → five audible steps VOLUME_1..5.
func percentToVolumeLevel(vol int) string {
	if vol < 0 {
		vol = 0
	}
	if vol > 100 {
		vol = 100
	}
	if vol == 0 {
		return "mute"
	}
	switch {
	case vol <= 20:
		return "VOLUME_1"
	case vol <= 40:
		return "VOLUME_2"
	case vol <= 60:
		return "VOLUME_3"
	case vol <= 80:
		return "VOLUME_4"
	default:
		return "VOLUME_5"
	}
}

// EncodeIntentParamsJSON encodes params for cloud.IntentResult.Parameters.
// Always emit a JSON object (never omit keys). For take_a_photo the engine
// rejects missing params; empty {} is also treated as "no params" by UIC.
func EncodeIntentParamsJSON(params map[string]string) string {
	if params == nil {
		params = map[string]string{}
	}
	b, err := json.Marshal(params)
	if err != nil {
		return "{}\n"
	}
	return string(b) + "\n"
}
