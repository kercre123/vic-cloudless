package xiaozhi

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/digital-dream-labs/vector-cloud/internal/log"
)

// ErrAborted is returned when a turn is interrupted by barge-in (wake word / button).
var ErrAborted = errors.New("xiaozhi turn aborted")

const (
	// CancelFlagPath asks anim to stop ExternalAudio immediately (ESP32-style barge-in).
	CancelFlagPath = "/run/vic-cloud/xiaozhi-cancel"
	// BusyPath is created while ExternalAudio is playing; removed when playback ends/cancels.
	BusyPath = "/run/vic-cloud/xiaozhi-busy"
)

// sessionState holds the long-lived WSS client (ESP32-style audio channel) plus
// the cancel func for the in-flight listen/TTS round.
type sessionState struct {
	mu         sync.Mutex
	client     *Client
	mcp        *MCPHandler
	mcpCancel  context.CancelFunc
	turnCancel context.CancelFunc
	// turnGen increments on every BindTurn so an aborted turn cannot Unbind /
	// CleanupPlaybackFiles belonging to a newer listen/TTS round (barge-in race).
	turnGen   uint64
	playing   bool
	// playingSince is set when playing flips true; used to ignore false-wake
	// barge-in in the first moments of TTS (hotword often queued during the
	// long wait for the first Opus frame).
	playingSince time.Time
	// ttsPending is set on server TTS start until the turn ends. While set and
	// not yet playing, Hey Vector must not cancel the turn (silent robot).
	ttsPending bool
	idleTimer  *time.Timer
}

var sess sessionState

// EnsureConnected returns a live Xiaozhi client, dialing if needed.
// In continuous mode the same session_id is reused across listen rounds.
func EnsureConnected(ctx context.Context, cfg Config) (*Client, error) {
	sess.mu.Lock()

	if sess.client != nil && sess.client.Alive() {
		// Drop stale STT/TTS/audio from the previous round before reuse.
		// mcpCh is NOT drained (see DrainEventChannels).
		sess.client.DrainEventChannels()
		log.Println("[Xiaozhi] reusing WSS session:", sess.client.SessionID())
		c := sess.client
		resetIdleTimerLocked()
		sess.mu.Unlock()
		return c, nil
	}

	if sess.client != nil {
		stopMCPPumpLocked()
		sess.client.Close()
		sess.client = nil
	}

	client, err := Dial(ctx, cfg)
	if err != nil {
		sess.mu.Unlock()
		return nil, err
	}
	sess.client = client
	startMCPPumpLocked(client)
	log.Println("[Xiaozhi] opened WSS session:", client.SessionID())
	resetIdleTimerLocked()
	h := sess.mcp
	sess.mu.Unlock()

	// Answer initialize/tools/list before listen (ESP32 always ready).
	waitMCPHandshake(ctx, h, 3*time.Second)
	return client, nil
}

// SessionMCP returns the session MCP handler (may be nil).
func SessionMCP() *MCPHandler {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.mcp
}

func startMCPPumpLocked(client *Client) {
	stopMCPPumpLocked()
	h := NewMCPHandler(client)
	sess.mcp = h
	pctx, cancel := context.WithCancel(context.Background())
	sess.mcpCancel = cancel
	go runMCPPump(pctx, client, h)
}

func stopMCPPumpLocked() {
	if sess.mcpCancel != nil {
		sess.mcpCancel()
		sess.mcpCancel = nil
	}
	sess.mcp = nil
}

// CloseSession sends goodbye (best-effort) and tears down the persistent WSS.
func CloseSession() {
	sess.mu.Lock()
	closeSessionLocked("manual")
	sess.mu.Unlock()
}

// InvalidateSession drops a dead/broken WSS without waiting for idle.
// Next EnsureConnected will Dial a fresh session.
func InvalidateSession(reason string) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if reason == "" {
		reason = "dead"
	}
	closeSessionLocked(reason)
}

func closeSessionLocked(reason string) {
	if sess.idleTimer != nil {
		sess.idleTimer.Stop()
		sess.idleTimer = nil
	}
	if sess.client == nil {
		return
	}
	sid := sess.client.SessionID()
	stopMCPPumpLocked()
	_ = sess.client.SendGoodbye()
	sess.client.Close()
	sess.client = nil
	log.Println("[Xiaozhi] closed WSS session:", sid, "reason:", reason)
}

// MarkActivity resets the session idle timer (call after a successful round).
// Do NOT arm while a listen/TTS/music turn is active — a superseded barge-in
// turn used to MarkActivity after the next BindTurn and kill long music at 30s.
func MarkActivity() {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.turnCancel != nil || sess.playing {
		return
	}
	resetIdleTimerLocked()
}

func resetIdleTimerLocked() {
	if sess.idleTimer != nil {
		sess.idleTimer.Stop()
		sess.idleTimer = nil
	}
	if !ContinuousMode() || sess.client == nil {
		return
	}
	idle := time.Duration(GetConfig().SessionIdleSec) * time.Second
	if idle <= 0 {
		idle = time.Duration(GetConfig().IdleTimeoutSec) * time.Second
	}
	if idle < 20*time.Second {
		idle = 30 * time.Second
	}
	// ESP32-style: after idle, tear down WSS. Timer must only run when no
	// active turn/playback (see MarkActivity / onSessionIdleTimeout).
	sess.idleTimer = time.AfterFunc(idle, onSessionIdleTimeout)
}

// onSessionIdleTimeout closes the persistent WSS after SessionIdleSec with no
// new listen. Live TTS/music defers close; only force-clear when truly stuck.
func onSessionIdleTimeout() {
	sess.mu.Lock()
	if sess.client == nil {
		sess.idleTimer = nil
		sess.mu.Unlock()
		return
	}

	activeTurn := sess.turnCancel != nil
	playing := sess.playing
	playAge := time.Duration(0)
	if playing && !sess.playingSince.IsZero() {
		playAge = time.Since(sess.playingSince)
	}
	// Music / long TTS can exceed session_idle_sec — defer, don't abort mid-song.
	// Cap at 15m so a wedged busy flag cannot hold WSS forever (RAM / fault path).
	const maxPlayHold = 15 * time.Minute
	if (activeTurn || playing) && playAge < maxPlayHold {
		log.Printf("[Xiaozhi] idle deferred — active playback/turn (playing=%v turn=%v age=%v)",
			playing, activeTurn, playAge.Round(time.Second))
		resetIdleTimerLocked()
		sess.mu.Unlock()
		return
	}

	stuckTurn := activeTurn
	stuckPlaying := playing
	var cancelTurn context.CancelFunc
	if sess.turnCancel != nil {
		cancelTurn = sess.turnCancel
		sess.turnCancel = nil
	}
	sess.playing = false
	sess.ttsPending = false
	sess.idleTimer = nil
	sess.mu.Unlock()

	busyLeft := false
	if _, err := os.Stat(BusyPath); err == nil {
		busyLeft = true
	}
	if _, err := os.Stat("/run/xiaozhi-busy"); err == nil {
		busyLeft = true
	}

	if stuckTurn || stuckPlaying || busyLeft {
		log.Printf("[Xiaozhi] idle force-close — clearing stuck state (turn=%v playing=%v busy=%v age=%v)",
			stuckTurn, stuckPlaying, busyLeft, playAge.Round(time.Second))
		if cancelTurn != nil {
			cancelTurn()
		}
		CleanupPlaybackFiles()
		_ = os.Remove(BusyPath)
		_ = os.Remove("/run/xiaozhi-busy")
		ClearCancelFlags()
		AbandonPostCaptureAwait("session_idle_force")
		SetPlaying(false)
	} else if cancelTurn != nil {
		cancelTurn()
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()
	closeSessionLocked("idle")
}

// BindTurn registers the active listen/TTS round cancel (does not own the WSS).
// Returns a generation id; UnbindTurn / FinishTurn / CleanupPlaybackIfOwner must
// pass the same id so a superseded (barge-in) turn cannot clobber the new one.
func BindTurn(cancel context.CancelFunc) uint64 {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.turnCancel != nil {
		sess.turnCancel()
	}
	sess.turnGen++
	sess.turnCancel = cancel
	sess.ttsPending = false
	if sess.idleTimer != nil {
		sess.idleTimer.Stop()
		sess.idleTimer = nil
	}
	return sess.turnGen
}

// UnbindTurn clears the in-flight round cancel only if gen is still current.
func UnbindTurn(gen uint64) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.turnGen == gen {
		sess.turnCancel = nil
		sess.ttsPending = false
	}
}

// FinishTurn is called when a listen/TTS round ends.
// keepSession=true leaves the WSS open (continuous); false closes it (one-shot).
func FinishTurn(gen uint64, keepSession bool, forceClose bool) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.turnGen == gen {
		sess.turnCancel = nil
		sess.ttsPending = false
	}
	if forceClose || !keepSession {
		closeSessionLocked("turn_end")
		return
	}
	resetIdleTimerLocked()
}

// CleanupPlaybackIfOwner deletes PCM/stream flags only when gen still owns the turn.
// After barge-in the new turn BindTurn's first — the aborted turn must not wipe
// the new PCM / xiaozhi-stream flag (silent TTS after button wake).
func CleanupPlaybackIfOwner(gen uint64) {
	sess.mu.Lock()
	owner := sess.turnGen == gen
	sess.mu.Unlock()
	if !owner {
		log.Println("[Xiaozhi] skip playback cleanup — newer turn owns files; gen:", gen)
		return
	}
	CleanupPlaybackFiles()
}

// SetPlaying marks ExternalAudio playback active/inactive.
func SetPlaying(playing bool) {
	sess.mu.Lock()
	sess.playing = playing
	if playing {
		sess.playingSince = time.Now()
	} else {
		sess.playingSince = time.Time{}
	}
	sess.mu.Unlock()
	if playing {
		_ = os.MkdirAll("/run/vic-cloud", 0775)
		if err := os.WriteFile(BusyPath, []byte("1"), 0644); err != nil {
			log.Println("[Xiaozhi] write busy flag:", err)
		}
		_ = os.WriteFile("/run/xiaozhi-busy", []byte("1"), 0644)
	} else {
		_ = os.Remove(BusyPath)
		_ = os.Remove("/run/xiaozhi-busy")
		MarkActivity()
	}
}

// MarkTTSPending notes that the server started TTS for this turn. Call on
// TTSStateStart so wake-word cannot cancel before the first Opus reaches anim.
func MarkTTSPending() {
	sess.mu.Lock()
	sess.ttsPending = true
	sess.mu.Unlock()
}

// ShouldHoldHotword is true when a new Hey Vector must not start a fresh turn
// (TTS buffering / early playback grace). Call after BargeIn returns false.
func ShouldHoldHotword() bool {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.playing {
		return true
	}
	return sess.ttsPending
}

// InListenTurn is true while a Xiaozhi round is active but not playing TTS
// (mic open / waiting for STT). Used to ignore duplicate Hey Vector mid-utterance.
func InListenTurn() bool {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.turnCancel != nil && !sess.playing
}

// IsPlaying reports whether ExternalAudio TTS is marked active.
func IsPlaying() bool {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.playing
}

// micUplinkStarted is set when the first Opus frame is sent this listen round.
// micListenEnded is set when Vector closes the mic stream (AudioDone) — after that
// we are only waiting for STT; Hey Vector must be allowed to abort a stuck wait.
var micUplinkStarted bool
var micListenEnded bool
var micUplinkMu sync.Mutex

// ResetMicUplink clears the uplink flag at the start of a listen round.
func ResetMicUplink() {
	micUplinkMu.Lock()
	micUplinkStarted = false
	micListenEnded = false
	micUplinkMu.Unlock()
}

// MarkMicUplink notes that mic audio is flowing to the server.
func MarkMicUplink() {
	micUplinkMu.Lock()
	micUplinkStarted = true
	micUplinkMu.Unlock()
}

// MarkMicListenEnded notes Vector finished sending mic (AudioDone / stream close).
func MarkMicListenEnded() {
	micUplinkMu.Lock()
	micListenEnded = true
	micUplinkMu.Unlock()
}

// ShouldIgnoreDuplicateHotword is true only while mic is still streaming an utterance.
// Once mic ends (STT wait) or uplink never started, allow hotword to restart/abort.
func ShouldIgnoreDuplicateHotword() bool {
	if !InListenTurn() {
		return false
	}
	micUplinkMu.Lock()
	ok := micUplinkStarted && !micListenEnded
	micUplinkMu.Unlock()
	return ok
}

var pendingServerAbort bool
var pendingAbortMu sync.Mutex

// MarkPendingServerAbort notes that the next listen should abort leftover server TTS.
func MarkPendingServerAbort() {
	pendingAbortMu.Lock()
	pendingServerAbort = true
	pendingAbortMu.Unlock()
}

// ConsumePendingServerAbort returns true once if a prior turn truncated TTS.
func ConsumePendingServerAbort() bool {
	pendingAbortMu.Lock()
	defer pendingAbortMu.Unlock()
	if !pendingServerAbort {
		return false
	}
	pendingServerAbort = false
	return true
}

// WaitPlaybackIdle waits until speaker playback should be finished.
// Busy-file alone is not enough: anim may clear it early (CleanupAudioEngine)
// while PCM is still draining — that caused the mic to hear the robot.
// We also enforce a floor from PCM duration since first audio frame.
func WaitPlaybackIdle(pcmBytes int, audioStartedAt time.Time, maxWait time.Duration) bool {
	if maxWait <= 0 {
		maxWait = 120 * time.Second
	}
	// 16kHz s16le mono: 32 bytes/ms
	pcmDur := time.Duration(0)
	if pcmBytes > 0 {
		pcmDur = time.Duration(pcmBytes/32) * time.Millisecond
	}
	margin := 600 * time.Millisecond
	var earliest time.Time
	if !audioStartedAt.IsZero() && pcmDur > 0 {
		earliest = audioStartedAt.Add(pcmDur + margin)
	} else {
		earliest = time.Now().Add(margin)
	}

	// If anim never clears busy (missed Complete), do not block relisten for maxWait (was 120s).
	stuckBusyDeadline := earliest.Add(3 * time.Second)
	deadline := time.Now().Add(maxWait)
	loggedEarly := false
	loggedStuck := false
	for time.Now().Before(deadline) {
		timeOk := !time.Now().Before(earliest)

		// Plan B: cloud owns busy + tinyplay. Do not wait for anim to clear the
		// busy file (it never will) — that caused false "busy stuck" and mid-tail kills.
		if UseAlsaPlayback() {
			if !AlsaActive() && timeOk {
				SetPlaying(false)
				return true
			}
			if AlsaActive() && timeOk && !time.Now().Before(stuckBusyDeadline) {
				if !loggedStuck {
					log.Println("[Xiaozhi][ALSA] drain stuck after PCM floor — killing for relisten")
					loggedStuck = true
				}
				AlsaCancel()
				SetPlaying(false)
				time.Sleep(100 * time.Millisecond)
				return false
			}
			time.Sleep(40 * time.Millisecond)
			continue
		}

		busyGone := true
		if _, err := os.Stat(BusyPath); err == nil {
			busyGone = false
		} else if _, err := os.Stat("/run/xiaozhi-busy"); err == nil {
			busyGone = false
		}
		if busyGone && timeOk {
			sess.mu.Lock()
			sess.playing = false
			sess.mu.Unlock()
			return true
		}
		if busyGone && !timeOk && !loggedEarly {
			remain := time.Until(earliest).Round(time.Millisecond)
			log.Printf("[Xiaozhi][TTS] busy cleared early — wait %v more (~%dms)", remain, pcmBytes/32)
			loggedEarly = true
		}
		// Anim posted but never Completed → busy stuck; open mic after PCM duration + grace.
		// Also cancel ExternalAudio: after camera/TTS underrun, busy file can linger while
		// Wwise is starved — next turn then has silent/scratchy speakers.
		if !busyGone && timeOk && !time.Now().Before(stuckBusyDeadline) {
			if !loggedStuck {
				log.Println("[Xiaozhi] busy stuck after PCM drain — forcing clear for relisten")
				loggedStuck = true
			}
			if err := RequestCancelPlayback(); err != nil {
				log.Println("[Xiaozhi] cancel after busy-stuck:", err)
			}
			CleanupPlaybackFiles()
			ClearCancelFlags()
			SetPlaying(false)
			time.Sleep(200 * time.Millisecond)
			return false
		}
		time.Sleep(40 * time.Millisecond)
	}
	log.Println("[Xiaozhi] playback idle wait timed out; forcing busy clear")
	SetPlaying(false)
	return false
}

// IsBusy reports whether a Xiaozhi turn or playback is in progress.
func IsBusy() bool {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.turnCancel != nil || sess.playing {
		return true
	}
	if _, err := os.Stat(BusyPath); err == nil {
		return true
	}
	return false
}

// ActiveSessionID returns the current WSS session_id, or empty if disconnected.
func ActiveSessionID() string {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.client != nil && sess.client.Alive() {
		return sess.client.SessionID()
	}
	return ""
}

// SessionAlive is true while the persistent Xiaozhi WSS is open.
// After server bye / turn_end close, this is false — continuous mode must NOT
// FakeTrigger (that would open mic and dial a new WebSocket).
func SessionAlive() bool {
	return ActiveSessionID() != ""
}

// BargeIn interrupts the current Xiaozhi turn/playback like xiaozhi-esp32 AbortSpeaking:
// send abort on the open WSS (same session), cancel the turn, stop ExternalAudio,
// wait briefly for the speaker to go idle, then return so a new listen can start.
// The WSS session is kept open.
//
// If only an idle persistent session exists (continuous mode between rounds), this is a
// no-op: sending abort then would make the server close the socket and the next listen
// fails with "websocket: close sent".
func BargeIn(reason string) bool {
	sess.mu.Lock()
	client := sess.client
	cancel := sess.turnCancel
	playing := sess.playing
	busyFile := false
	if _, err := os.Stat(BusyPath); err == nil {
		busyFile = true
	}
	sess.mu.Unlock()

	// Only barge-in while TTS is actually playing (or anim busy flag).
	// turnCancel is set for the whole turn (STT/LLM/wait-for-first-Opus) — treating
	// that as "active" made wake-word abort TTS before any audio left the speaker
	// (seen as "Hey Vector then robot never speaks").
	active := playing || busyFile
	if !active {
		return false
	}

	// Hotword often fires during the long wait for first Opus and is delivered
	// in the same tick as SetPlaying(true) — ignore barge-in briefly so the
	// robot actually speaks instead of immediately aborting.
	const bargeInGrace = 600 * time.Millisecond
	sess.mu.Lock()
	since := sess.playingSince
	sess.mu.Unlock()
	if playing && !since.IsZero() && time.Since(since) < bargeInGrace {
		log.Println("[Xiaozhi] barge-in ignored (TTS just started):", reason)
		return false
	}

	log.Println("[Xiaozhi] barge-in:", reason, "session:", ActiveSessionID())

	if client != nil && client.Alive() {
		if err := client.SendAbortSpeaking(reason); err != nil {
			log.Println("[Xiaozhi] abort send:", err)
			// Socket is dead — drop it so the next listen dials fresh.
			InvalidateSession("abort_failed")
		}
	} else if client != nil {
		InvalidateSession("dead_on_bargein")
	}
	if cancel != nil {
		cancel()
	}

	_ = os.Remove(RelistenPendingPath)
	_ = os.Remove(RelistenFlagPath)
	_ = os.Remove(PlayFlagPath)
	_ = os.Remove(PlayFlagLegacy)
	_ = os.Remove(StreamFlagPath)
	_ = os.Remove(StreamFlagLegacy)
	_ = os.Remove(StreamEndFlagPath)
	_ = os.Remove(StreamEndFlagLegacy)

	if err := RequestCancelPlayback(); err != nil {
		log.Println("[Xiaozhi] cancel playback:", err)
	}

	deadline := time.Now().Add(900 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(BusyPath); err != nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	SetPlaying(false)
	sess.mu.Lock()
	sess.ttsPending = false
	sess.mu.Unlock()

	// Drop leftover cancel flags so the next StreamStart is not eaten on the same
	// anim tick (cancel path unlinks stream flags before StartStreamPCM).
	ClearCancelFlags()

	// Small settle so the mic does not capture residual speaker audio.
	time.Sleep(120 * time.Millisecond)
	return true
}

// ClearCancelFlags removes barge-in cancel markers (both runtime paths).
func ClearCancelFlags() {
	_ = os.Remove(CancelFlagPath)
	_ = os.Remove("/run/xiaozhi-cancel")
}

// ActiveClient returns the live Xiaozhi WSS client, or nil.
func ActiveClient() *Client {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.client != nil && sess.client.Alive() {
		return sess.client
	}
	return nil
}

// AbortServerSpeaking asks the cloud TTS to stop (same session). Used before
// analyze_photo capture so leftover intro Opus cannot steal the post-camera turn.
func AbortServerSpeaking(reason string) {
	c := ActiveClient()
	if c == nil {
		return
	}
	if err := c.SendAbortSpeaking(reason); err != nil {
		log.Println("[Xiaozhi] abort speaking:", err)
	} else {
		log.Println("[Xiaozhi] abort speaking:", reason)
	}
}

// RequestCancelPlayback asks anim to stop ExternalAudio now.
// Plan B (ALSA): also kills tinyplay immediately.
func RequestCancelPlayback() error {
	AlsaCancel()
	return os.WriteFile(CancelFlagPath, []byte("1"), 0644)
}

// Deferred robot intent: when MCP tools/call arrives AFTER intent_system_noaudio,
// ReactToVoiceCommand has already exited and a late pending intent is ForceCleared
// in ~3 engine ticks. Queue the action and FakeTrigger a fresh listen so the intent
// is the FIRST Result while ReactToVoiceCommand is active again.

var (
	deferredMu     sync.Mutex
	deferredIntent string
	deferredParams map[string]string

	// Early MCP delivery: when tools/call arrives before the ~9s noaudio deadline,
	// deliver the robot intent on the SAME listen (instead of noaudio + FakeTrigger).
	// FakeTrigger ListeningGetIn was racing the Seasonal anim and cutting fireworks.
	earlyMu            sync.Mutex
	earlyIntent        string
	earlyParams        map[string]string
	earlyDeliveredIntent string
)

// NoteMCPIntentQueued records a tools/call so the delayed noaudio path can deliver
// the robot intent on the open ReactToVoiceCommand instead of FakeTrigger later.
func NoteMCPIntentQueued(intent string, params map[string]string) {
	if intent == "" {
		return
	}
	earlyMu.Lock()
	earlyIntent = intent
	earlyParams = params
	earlyDeliveredIntent = ""
	earlyMu.Unlock()
}

// PeekMCPIntentQueued returns the queued tools/call intent without clearing it.
func PeekMCPIntentQueued() (intent string, params map[string]string, ok bool) {
	earlyMu.Lock()
	defer earlyMu.Unlock()
	if earlyIntent == "" {
		return "", nil, false
	}
	return earlyIntent, earlyParams, true
}

// ClearPendingMCPIntent drops any queued/stale same-listen MCP intent (call at turn start
// and after post-TTS dispatch so the next chat cannot re-fire an old blackjack/etc.).
func ClearPendingMCPIntent(reason string) {
	earlyMu.Lock()
	had := earlyIntent != "" || earlyDeliveredIntent != ""
	earlyIntent = ""
	earlyParams = nil
	earlyDeliveredIntent = ""
	earlyMu.Unlock()
	if had {
		log.Println("[Xiaozhi] cleared pending MCP intent queue; reason:", reason)
	}
}

// TakeMCPIntentForSameStreamDelivery returns a queued MCP intent for delivery in
// place of intent_system_noaudio (same listen window).
// Photo (and similar) intents are left queued so they run after TTS — early delivery
// mid-speech used to drop take_a_photo (and raced ExternalAudio/vision).
func TakeMCPIntentForSameStreamDelivery() (intent string, params map[string]string, ok bool) {
	earlyMu.Lock()
	defer earlyMu.Unlock()
	if earlyIntent == "" {
		return "", nil, false
	}
	if mcpDeferUntilAfterTTS(earlyIntent) {
		return "", nil, false
	}
	intent = earlyIntent
	params = earlyParams
	earlyIntent = ""
	earlyParams = nil
	earlyDeliveredIntent = intent
	return intent, params, true
}

// mcpDeferUntilAfterTTS: intents that need full robot control after confirmation TTS.
func mcpDeferUntilAfterTTS(intent string) bool {
	switch intent {
	case "intent_photo_take_extend":
		return true
	default:
		return false
	}
}

// MCPAlreadyDeliveredOnSameStream reports that early same-stream delivery already ran
// for this turn's robot intent (skip FakeTrigger after TTS).
func MCPAlreadyDeliveredOnSameStream(intent string) bool {
	earlyMu.Lock()
	defer earlyMu.Unlock()
	return intent != "" && earlyDeliveredIntent == intent
}

// SetDeferredIntent stores a self-control intent for the next Xiaozhi listen round.
func SetDeferredIntent(intent string, params map[string]string) {
	if intent == "" {
		return
	}
	deferredMu.Lock()
	deferredIntent = intent
	deferredParams = params
	deferredMu.Unlock()
	log.Println("[Xiaozhi] deferred self-control intent for next listen:", intent)
}

// TakeDeferredIntent returns and clears any deferred self-control intent.
func TakeDeferredIntent() (intent string, params map[string]string, ok bool) {
	deferredMu.Lock()
	defer deferredMu.Unlock()
	if deferredIntent == "" {
		return "", nil, false
	}
	intent = deferredIntent
	params = deferredParams
	deferredIntent = ""
	deferredParams = nil
	return intent, params, true
}
