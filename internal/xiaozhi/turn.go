package xiaozhi

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/digital-dream-labs/vector-cloud/internal/log"
)

// TurnResult holds the outcome of a Xiaozhi voice turn.
type TurnResult struct {
	// STTText is the transcribed text from the mic audio.
	STTText string
	// ResponseText is the LLM/TTS sentence text (what the robot should say).
	ResponseText string
	// RobotIntent is the robot intent string from an MCP tool call (may be empty).
	RobotIntent string
	// RobotIntentParams are engine parameters for RobotIntent (JSON object keys).
	RobotIntentParams map[string]string
	// PCMChunks is legacy one-shot PCM (unused when StreamedAudio is set).
	PCMChunks [][]byte
	// StreamedAudio is true when TTS was streamed to the speaker during RunTurn
	// (ESP32-style); caller must not re-trigger playback.
	StreamedAudio bool
	// PCMBytes is total 16kHz s16le PCM appended during streaming (for playback wait).
	PCMBytes int
	// AudioStartedAt is when the first TTS PCM frame was streamed (zero if none).
	AudioStartedAt time.Time
	// EndConversation is true after server goodbye / WSS drop on bye — caller must
	// NOT continuous-relisten (would reopen mic + WebSocket).
	EndConversation bool
}

// RunTurn executes a complete Xiaozhi voice turn:
//  1. Connect to server (or reuse), send hello
//  2. ListenStart, stream audio from audioBuf, ListenStop
//  3. Wait for STT text
//  4. Send text, wait for TTS; decode Opus→PCM and stream to speaker as frames arrive
//
// audioStream is a channel of PCM 16kHz s16le chunks (same as vtr.Process input).
// afterSTT, if non-nil, is called once STT text is available (before TTS wait) so the
// caller can close the Vector listening UI before the engine's ~10s stream timeout.
func RunTurn(ctx context.Context, audioStream <-chan []byte, cfg Config, afterSTT func(stt string)) (*TurnResult, error) {
	// Own budget for dial/STT/TTS. Mic end is signalled by audioStream closing
	// (CloseSend→micDone). Barge-in cancels turnCtx via BindTurn only —
	// do NOT tie turnCancel to strm.ctx: afterSTT→OnIntent closes the streamer
	// and would abort TTS before playback (false "barge-in").
	_ = ctx
	turnCtx, turnCancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer turnCancel()

	keepSession := ContinuousMode()
	forceClose := false
	endConversation := false

	// Reuse persistent WSS in continuous mode (ESP32-style same session_id).
	client, err := EnsureConnected(turnCtx, cfg)
	if err != nil {
		return nil, fmt.Errorf("xiaozhi dial: %w", err)
	}
	turnGen := BindTurn(turnCancel)
	defer func() {
		UnbindTurn(turnGen)
		if turnCtx.Err() == context.Canceled {
			// Never wipe PCM/flags on barge-in — BargeIn already stopped anim;
			// CleanupPlaybackFiles races the next turn's StreamStart (silent TTS).
			// MarkActivity is a no-op while the replacement turn is bound/playing,
			// so we cannot re-arm the 30s idle timer under live music.
			if keepSession {
				MarkActivity()
			}
			return
		}
		FinishTurn(turnGen, keepSession, forceClose)
	}()

	log.Println("[Xiaozhi][Mic] listen round; session:", client.SessionID(), "persistent:", keepSession)
	ResetMicUplink()

	// Only abort if a prior turn truncated server TTS. Aborting before every listen
	// confuses tenclass and yields no STT on the next utterance.
	if ConsumePendingServerAbort() {
		if err := client.SendAbortSpeaking("prior_tts_truncated"); err != nil {
			log.Println("[Xiaozhi] abort before listen:", err)
		}
	}
	client.DrainEventChannels()

	// MCP pump runs on the session (started in EnsureConnected). Do not create a
	// second handler that races on mcpCh — that used to drop handshake replies.
	if mcp := SessionMCP(); mcp != nil && !mcp.ToolsListed() {
		log.Println("[Xiaozhi] MCP tools not listed yet — waiting briefly before listen")
		waitMCPHandshake(turnCtx, mcp, 2*time.Second)
	}

	enc, encErr := NewEncoder()
	dec, decErr := NewDecoder()

	// --- Phase 1: stream mic audio to server ---
	if err := client.ListenStart(); err != nil {
		log.Println("[Xiaozhi] listen start failed, redialing:", err)
		InvalidateSession("listen_start_failed")
		client, err = EnsureConnected(turnCtx, cfg)
		if err != nil {
			forceClose = true
			return nil, fmt.Errorf("xiaozhi redial: %w", err)
		}
		log.Println("[Xiaozhi] listen round on session:", client.SessionID(), "persistent:", keepSession, "(after redial)")
		if err := client.ListenStart(); err != nil {
			forceClose = true
			return nil, fmt.Errorf("listen start: %w", err)
		}
	}

	idleTimeout := time.Duration(cfg.IdleTimeoutSec) * time.Second
	if idleTimeout < 5*time.Second {
		idleTimeout = 5 * time.Second
	}

	// Accumulate opus-encoding buffer (we need EncFrameSamples*2 bytes per frame).
	encBuf := make([]byte, 0, EncFrameSamples*2*4)
	audioSendCount := 0
	// If FakeTrigger opened cloud-listen but anim never streams mic, do not hang forever.
	var micWaitDeadline <-chan time.Time = time.After(12 * time.Second)
	listenStartAt := time.Now()

streamLoop:
	for {
		select {
		case chunk, ok := <-audioStream:
			if !ok {
				MarkMicListenEnded()
				break streamLoop
			}
			if encErr != nil || enc == nil {
				// No opus encoder — send raw PCM as a fallback note; server will see silence.
				// This path only happens in non-vicos builds (dev).
				continue
			}
			encBuf = append(encBuf, chunk...)
			// Drain complete Opus frames from buffer.
			for len(encBuf) >= EncFrameSamples*2 {
				frame := encBuf[:EncFrameSamples*2]
				encBuf = encBuf[EncFrameSamples*2:]
				opusFrame, err := enc.EncodeFrame(frame)
				if err != nil {
					log.Println("[Xiaozhi] encode error:", err)
					continue
				}
				if serr := client.SendAudio(opusFrame); serr != nil {
					log.Println("[Xiaozhi] send audio error:", serr)
					InvalidateSession("send_audio_failed")
					forceClose = true
					return nil, fmt.Errorf("send audio: %w", serr)
				}
				audioSendCount++
				if audioSendCount == 1 {
					MarkMicUplink()
				}
			}
		// Do NOT drain TTS/LLM/Audio here. Tenclass often starts TTS as soon as STT
		// is ready — while mic uplink is still running. Discarding = missing head.
		case <-micWaitDeadline:
			if audioSendCount == 0 {
				log.Printf("[Xiaozhi] no mic audio for %v after listen start — aborting stuck listen",
					time.Since(listenStartAt).Round(time.Millisecond))
				forceClose = false
				return nil, fmt.Errorf("no mic uplink after listen start")
			}
			// Uplink already alive; disable further mic-wait wakes by draining to nil channel.
			micWaitDeadline = nil
		case err := <-client.ErrCh():
			forceClose = true
			return nil, fmt.Errorf("WSS died during listen: %w", err)
		case <-turnCtx.Done():
			if turnCtx.Err() == context.Canceled {
				return nil, ErrAborted
			}
			return nil, turnCtx.Err()
		}
	}

	if err := client.ListenStop(); err != nil {
		log.Println("[Xiaozhi] listen stop error:", err)
	}
	MarkMicListenEnded()

	// Beat engine Intent timeout (NoCloud) as soon as mic closes — Xiaozhi STT
	// continues on WSS independently of the Vector cloud stream.
	if afterSTT != nil {
		afterSTT("")
	}

	// --- Phase 2: wait for STT ---
	// Short utterance / silence after FakeTrigger: don't hang 30s on NoCloud.
	sttTimeout := idleTimeout + 10*time.Second
	if audioSendCount < 150 {
		sttTimeout = 8 * time.Second
	}
	var sttText string
	sttTimer := time.NewTimer(sttTimeout)
	defer sttTimer.Stop()
	log.Printf("[Xiaozhi][Mic] waiting for STT (sent %d frames, timeout %v)", audioSendCount, sttTimeout)
sttWait:
	for {
		select {
		case sttText = <-client.STTCh():
			log.Printf("[Xiaozhi][Mic] STT: %s", sttText)
			break sttWait
		// Do NOT drain TTS/LLM/Audio while waiting for STT — server may already be
		// streaming the reply (see TRACE: binary #1..N before "waiting for STT").
		case err := <-client.ErrCh():
			return nil, fmt.Errorf("waiting for STT: %w", err)
		case <-sttTimer.C:
			return nil, fmt.Errorf("timeout waiting for STT (sent %d frames)", audioSendCount)
		case <-turnCtx.Done():
			if turnCtx.Err() == context.Canceled {
				return nil, ErrAborted
			}
			return nil, turnCtx.Err()
		}
	}

	if sttText == "" {
		return nil, fmt.Errorf("empty STT result")
	}

	// Self-control is ESP32-style only: STT → LLM → tools/call (self.vector.*).
	// Do NOT keyword-match here — that bypasses the LLM like Vosk, which is not what we want.
	//
	// When tools/call arrives, QUEUE the robot intent and still wait for TTS confirmation
	// ("mình sẽ bắn pháo hoa…"). Returning early here skipped speech and looked like keyword match.

	var pendingRobotIntent string
	var pendingRobotParams map[string]string
	mcpBeforeSendText := false

	// MCP pump may have already queued tools/call (and handshake) during listen/STT.
	if intent, params, ok := PeekMCPIntentQueued(); ok {
		pendingRobotIntent = intent
		pendingRobotParams = params
		mcpBeforeSendText = true
		log.Println("[Xiaozhi] MCP action already queued before SendText:", pendingRobotIntent)
	}

	// NEVER DrainEventChannels after STT. Tenclass already pushed tts/start + Opus
	// before SendText; draining here was deleting the spoken head ("Tao sẽ kể…").
	// Stale prior-turn events were cleared at listen start.

	// Do NOT release listen UI before stream arm — that starts getout ~3s before
	// ExternalAudio is ready and was pairing with "silent speaker" after early-TTS queue.
	listenUIReleased := false
	releaseListenUI := func(reason string) {
		_ = reason
		if afterSTT == nil || listenUIReleased {
			return
		}
		listenUIReleased = true
		afterSTT(sttText)
	}

	// --- Phase 3: send STT text unless server already ran tools for this utterance ---
	if mcpBeforeSendText {
		log.Println("[Xiaozhi] skip SendText — server already issued MCP; waiting for TTS")
	} else if err := client.SendText(sttText); err != nil {
		return nil, fmt.Errorf("send text: %w", err)
	}

	doStream := cfg.TTSMode == "xiaozhi"
	var responseText string
	var ttsSentences []string // every sentence_start.text from server (in order)
	var llmFullText strings.Builder
	var allPCM []byte // only used when not streaming (acapela / decode fail path)
	ttsStarted := false
	sawTTSStart := false
	ttsStopped := false
	streamStarted := false
	streamEnded := false
	headPadDone := false // Plan A: one head silence pad per stream before real PCM
	// Mitigate pack: soft-cap ~250s of continuous server TTS/music to protect
	// ALSA/Wwise on 426MB Vector (was 10m — multi-minute streams crashed anim).
	const ttsAbsoluteMax = 250 * time.Second
	const ttsIdleGap = 45 * time.Second
	const ttsSoftCapPCMBytes = 250 * 16000 * 2 // ~250s of 16kHz s16le
	ttsAbsoluteDeadline := time.After(ttsAbsoluteMax)
	idleTicker := time.NewTicker(1 * time.Second)
	defer idleTicker.Stop()
	ttsPhaseStart := time.Now()
	var firstAudioAt time.Time
	lastAudioAt := time.Time{}
	audioFrames := 0
	audioOpusBytes := 0
	pcmBytesStreamed := 0

	abortServerTTS := func(reason string) {
		MarkPendingServerAbort()
		if err := client.SendAbortSpeaking(reason); err != nil {
			log.Println("[Xiaozhi] abort speaking:", err)
		}
		client.DrainEventChannels()
	}

	endStream := func() {
		if streamStarted && !streamEnded {
			_ = StreamEnd()
			streamEnded = true
		}
	}

	defer func() {
		if turnCtx.Err() == context.Canceled {
			return
		}
		endStream()
	}()

	resetStaleTTSBlip := func() {
		if streamStarted {
			_ = RequestCancelPlayback()
			streamStarted = false
			streamEnded = false
			SetPlaying(false)
		}
		audioFrames = 0
		audioOpusBytes = 0
		pcmBytesStreamed = 0
		firstAudioAt = time.Time{}
		lastAudioAt = time.Time{}
		sawTTSStart = false
		ttsStarted = false
		ttsSentences = nil
		responseText = ""
		llmFullText.Reset()
		headPadDone = false
	}

	for !ttsStopped {
		select {
		case ttsMsg := <-client.TTSCh():
			switch ttsMsg.State {
			case TTSStateStart:
				ttsStarted = true
				sawTTSStart = true
				MarkTTSPending()
				log.Println("[Xiaozhi][TTS] start (server)")
			case TTSStateSentenceStart:
				if ttsMsg.Text != "" {
					responseText = ttsMsg.Text
					ttsSentences = append(ttsSentences, ttsMsg.Text)
					// Long stories spam journalctl — log first sentence only.
					if len(ttsSentences) == 1 {
						log.Println("[Xiaozhi][TTS] sentence:", ttsMsg.Text)
					}
				}
			case TTSStateStop:
				// Stale stop from a prior truncated TTS (arrived after SendText).
				if audioFrames == 0 && !sawTTSStart {
					log.Println("[Xiaozhi][TTS] ignoring stale stop (no start/audio yet)")
					continue
				}
				if audioFrames < 5 && time.Since(ttsPhaseStart) < 800*time.Millisecond && responseText == "" {
					log.Printf("[Xiaozhi][TTS] ignoring likely-stale stop (%d frames in %v)",
						audioFrames, time.Since(ttsPhaseStart).Round(time.Millisecond))
					resetStaleTTSBlip()
					continue
				}
				// Intro stop during capture, or late stop after Soft StreamEnd while
				// waiting for analysis — ignore briefly. After tool reply, a stop with
				// no analysis stream means the server finished; do not hang 25s.
				if IgnorePrematureTTSStop() {
					// Only end when analysis stream was actually armed/playing.
					// audioFrames>0 alone is wrong: intro Opus still increments the
					// counter while DropPostCaptureOpus discards it — that made us
					// "end normally" with no "streaming analysis after camera" (silent).
					if PostCaptureToolDone() && streamStarted {
						log.Printf("[Xiaozhi][TTS] analysis stop after %d frames — ending normally", audioFrames)
						ClearAwaitPostCaptureStream()
						ttsStopped = true
						endStream()
						continue
					}
					if PostCaptureToolDone() && !streamStarted &&
						PostCaptureAwaitTimedOut(2*time.Second) {
						rx, dropped := client.AudioRXStats()
						log.Printf("[Xiaozhi][TTS] post-camera stop with no analysis — abort (rx=%d dropped=%d)",
							rx, dropped)
						abortServerTTS("post_camera_no_analysis")
						AbandonPostCaptureAwait("tts_stop_no_analysis")
						ttsStopped = true
						endStream()
						continue
					}
					log.Println("[Xiaozhi][TTS] ignoring premature stop during/after camera")
					// Clear intro frame counts so a later stop cannot look like analysis.
					if AwaitPostCaptureStream() || StreamAppendSuspended() {
						lastAudioAt = time.Time{}
						sawTTSStart = false
						audioFrames = 0
						pcmBytesStreamed = 0
						firstAudioAt = time.Time{}
						streamEnded = false
						SetPlaying(false)
					}
					continue
				}
				ttsStopped = true
				pcmMs := pcmBytesStreamed / 32
				if !doStream && len(allPCM) > pcmBytesStreamed {
					pcmMs = len(allPCM) / 32
				}
				if audioFrames > 0 {
					rx, dropped := client.AudioRXStats()
					log.Printf("[Xiaozhi][TTS] stop — ~%dms audio, %d sentences, first +%v total %v (rx=%d dropped=%d)",
						pcmMs, len(ttsSentences),
						firstAudioAt.Sub(ttsPhaseStart).Round(time.Millisecond),
						time.Since(ttsPhaseStart).Round(time.Millisecond),
						rx, dropped)
					if dropped > 0 {
						log.Printf("[Xiaozhi][TTS] WARNING: %d WSS frames dropped — possible missing head", dropped)
					}
				} else {
					rx, dropped := client.AudioRXStats()
					log.Printf("[Xiaozhi][TTS] stop — no audio (wss_rx=%d dropped=%d)", rx, dropped)
				}
				endStream()
			}

		case llmMsg := <-client.LLMCh():
			if llmMsg.Text != "" && responseText == "" {
				responseText = llmMsg.Text
				// Skip emoji-only LLM chatter in logs.
				if !(len([]rune(llmMsg.Text)) <= 2) {
					log.Println("[Xiaozhi][TTS] LLM:", llmMsg.Text)
				}
			}
			ttsStarted = true

		case opusFrame := <-client.AudioCh():
			ttsStarted = true

			// Decode Opus→16k PCM. Robot anim redirects StartStreamOpus to the
			// growing PCM file — writing Opus FIFOs produced silent speakers.
			if dec == nil || decErr != nil {
				continue
			}
			pcm24k, err := dec.DecodeFrame(opusFrame)
			if err != nil {
				log.Println("[Xiaozhi][TTS] opus decode error:", err)
				continue
			}
			pcm16k := Downsample24kTo16k(pcm24k)
			if !doStream {
				audioFrames++
				audioOpusBytes += len(opusFrame)
				lastAudioAt = time.Now()
				allPCM = append(allPCM, pcm16k...)
				continue
			}

			// Drop Opus while camera holds the speaker (streamSuspend).
			// Do not bump audioFrames here — intro leftovers must not look like analysis.
			if StreamAppendSuspended() || DropPostCaptureOpus() {
				if streamStarted {
					streamStarted = false
					streamEnded = false
					SetPlaying(false)
					headPadDone = false
				}
				continue
			}
			audioFrames++
			audioOpusBytes += len(opusFrame)
			lastAudioAt = time.Now()

			if !streamStarted {
				if AwaitPostCaptureStream() || PostCaptureToolDone() {
					if newDec, err := NewDecoder(); err == nil {
						dec = newDec
						decErr = nil
					}
					pcmBytesStreamed = 0
					ttsSentences = nil
					log.Println("[Xiaozhi][TTS] streaming analysis after camera")
				}
				primeBytes, err := ArmStreamWithSilencePrime()
				if err != nil {
					log.Println("[Xiaozhi][TTS] stream start:", err)
					doStream = false
					allPCM = append(allPCM, pcm16k...)
					continue
				}
				pcmBytesStreamed += primeBytes
				streamStarted = true
				firstAudioAt = time.Now()
				SetPlaying(true)
				ClearAwaitPostCaptureStream()
				releaseListenUI("stream_armed")
				log.Printf("[Xiaozhi][TTS] playing (prime=%dB) +%v",
					primeBytes, firstAudioAt.Sub(ttsPhaseStart).Round(time.Millisecond))
			}

			if streamStarted {
				if pcmBytesStreamed > ttsSoftCapPCMBytes {
					log.Printf("[Xiaozhi][TTS] soft-cap ~%ds PCM — aborting long stream for stability",
						ttsSoftCapPCMBytes/32000)
					abortServerTTS("soft_cap")
					_ = StreamEnd()
					ttsStopped = true
					continue
				}
				fsz, err := StreamAppend(pcm16k)
				if err != nil {
					if err == ErrPCMHardCap {
						log.Println("[Xiaozhi][TTS] PCM cap — aborting server TTS for RAM")
						abortServerTTS("pcm_cap")
						_ = StreamEnd()
						ttsStopped = true
						continue
					}
					log.Println("[Xiaozhi][TTS] pcm append:", err)
					_ = StreamEnd()
					SetPlaying(false)
					streamStarted = false
					doStream = false
					ttsStopped = true
					log.Println("[Xiaozhi][TTS] stream aborted")
				} else {
					pcmBytesStreamed += len(pcm16k)
					_ = fsz
					_ = headPadDone
				}
				continue
			}

			allPCM = append(allPCM, pcm16k...)

		case <-client.GoodbyeCh():
			ttsStopped = true
			forceClose = true
			endConversation = true
			log.Println("[Xiaozhi] goodbye received — end conversation (no relisten)")
			endStream()

		case err := <-client.ErrCh():
			log.Println("[Xiaozhi] server error during TTS:", err)
			ttsStopped = true
			forceClose = true
			// Server drop alone is enough; EndConversation is set when SessionAlive
			// is false after FinishTurn closes the WSS — no STT keyword matching.
			endStream()

		case <-idleTicker.C:
			// Sync tools/call that the MCP pump queued while we were streaming TTS.
			if intent, params, ok := PeekMCPIntentQueued(); ok {
				if pendingRobotIntent != intent {
					pendingRobotIntent = intent
					pendingRobotParams = params
					log.Println("[Xiaozhi] MCP action queued during TTS (let confirmation finish):", intent)
				}
			}
			if !ttsStarted {
				if time.Since(ttsPhaseStart) > ttsIdleGap {
					releaseListenUI("tts_timeout")
					return nil, fmt.Errorf("timeout waiting for TTS response (%v)", ttsIdleGap)
				}
				continue
			}
			// After tool reply: no analysis audio within ~12s → hard abort (LLM+TTS need a beat).
			if PostCaptureToolDone() && PostCaptureAwaitTimedOut(12*time.Second) && !streamStarted {
				rx, dropped := client.AudioRXStats()
				log.Printf("[Xiaozhi][TTS] post-camera await timeout — hard abort (rx=%d dropped=%d audioFrames=%d)",
					rx, dropped, audioFrames)
				abortServerTTS("post_camera_await_timeout")
				AbandonPostCaptureAwait("await_timeout_12s")
				ttsStopped = true
				endStream()
				continue
			}
			// Hold normal idle gap open only briefly while waiting for analysis Opus.
			if AwaitPostCaptureStream() {
				continue
			}
			// No audio yet: allow LLM thinking time up to absolute max (handled below).
			if lastAudioAt.IsZero() {
				continue
			}
			if time.Since(lastAudioAt) > ttsIdleGap {
				log.Printf("[Xiaozhi] TTS idle %v since last audio — ending stream (partial ok)", ttsIdleGap)
				ClearAwaitPostCaptureStream()
				abortServerTTS("idle_timeout")
				ttsStopped = true
				endStream()
			}

		case <-ttsAbsoluteDeadline:
			if ttsStarted {
				log.Println("[Xiaozhi] TTS absolute deadline reached (partial result ok)")
				abortServerTTS("max_duration")
				ttsStopped = true
				endStream()
			} else {
				releaseListenUI("tts_absolute_timeout")
				return nil, fmt.Errorf("timeout waiting for TTS response (%v)", ttsAbsoluteMax)
			}

		case <-turnCtx.Done():
			if turnCtx.Err() == context.Canceled {
				return nil, ErrAborted
			}
			return nil, turnCtx.Err()
		}
	}

	if turnCtx.Err() == context.Canceled {
		return nil, ErrAborted
	}

	releaseListenUI("turn_complete")

	finalText := responseText
	if finalText == "" && llmFullText.Len() > 0 {
		finalText = llmFullText.String()
	}

	result := &TurnResult{
		STTText:           sttText,
		ResponseText:      finalText,
		RobotIntent:       pendingRobotIntent,
		RobotIntentParams: pendingRobotParams,
		StreamedAudio:     streamStarted,
		PCMBytes:          pcmBytesStreamed,
		AudioStartedAt:    firstAudioAt,
		EndConversation:   endConversation,
	}
	if result.EndConversation {
		forceClose = true
		log.Println("[Xiaozhi] end conversation — WSS will close; no continuous relisten")
	}
	if !streamStarted && len(allPCM) > 0 {
		result.PCMChunks = ChunkPCM(allPCM)
	}
	if pendingRobotIntent != "" {
		log.Println("[Xiaozhi] turn complete with queued MCP intent:", pendingRobotIntent)
	}
	_ = ttsSentences
	_ = headPadDone
	return result, nil
}
