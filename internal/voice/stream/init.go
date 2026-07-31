package stream

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	chippergrpc2 "github.com/digital-dream-labs/api/go/chipperpb"
	"github.com/digital-dream-labs/vector-cloud/internal/clad/cloud"
	"github.com/digital-dream-labs/vector-cloud/internal/log"
	"github.com/digital-dream-labs/vector-cloud/internal/voice/vtr"
	"github.com/digital-dream-labs/vector-cloud/internal/xiaozhi"
)

// it works well at 533MHz, but transcription is instant at 730
var doFreqStuff bool = true

// WIRE: main entrypoint for a request!
// we are keeping the OG code commented in case we want to make some sort of hybrid solution

func (strm *Streamer) init(streamSize int) {
	// set up error response if context times out/is canceled
	go strm.cancelResponse()

	// start routine to buffer communication between main routine and upload routine
	go strm.bufferRoutine(streamSize)

	go func() {
		// Reload config each turn so :8080 Enable takes effect after vic restart / without stale cache issues.
		_ = xiaozhi.LoadConfig()

		// Deferred MCP intent must be delivered before Xiaozhi/Vosk branching so
		// blackjack FakeTrigger (Normal) still works after CloseSession.
		if strm.deliverDeferredSelfControlIfAny() {
			return
		}

		if xiaozhi.Enabled() {
			// Blackjack streams use local Vosk only. If Vosk cannot run (OOM refuse,
			// idle cooldown, prepare fail), drop the stream — never fall through to
			// Xiaozhi or chat runs in parallel with the engine blackjack UI.
			if xiaozhi.ShouldUseLocalVosk(strm.opts.mode) {
				log.Println("[Game] local Vosk path; mode:", strm.opts.mode)
				strm.runLocalVoskTurn()
				return
			}
			if strm.opts.mode == cloud.StreamType_Blackjack {
				xiaozhi.DisarmRelistenPending()
				log.Println("[Game] drop Blackjack stream — Vosk unavailable; no Xiaozhi parallel")
				strm.respOnce.Do(func() {
					strm.receiver.OnError(cloud.ErrorType_Server, fmt.Errorf("blackjack STT unavailable"))
				})
				return
			}
			log.Println("[Xiaozhi] enabled — entering runXiaozhiTurn")
			strm.runXiaozhiTurn()
			return
		}
		log.Println("[Xiaozhi] disabled — using local Vosk")
		strm.runLocalVoskTurn()
	}()
}

// deliverDeferredSelfControlIfAny sends a queued MCP intent as the first Result.
// Fallback only if same-listen delivery could not run (rare). Prefer no FakeTrigger.
func (strm *Streamer) deliverDeferredSelfControlIfAny() bool {
	intent, params, ok := xiaozhi.TakeDeferredIntent()
	if !ok {
		return false
	}
	strm.receiver.OnStreamOpen("xiaozhi")
	paramsJSON := xiaozhi.EncodeIntentParamsJSON(params)
	log.Printf("[Xiaozhi][KeepawayFlow] step=deliver_on_new_stream intent=%s", intent)
	log.Println("[Xiaozhi] delivering deferred self-control intent:", intent)
	xiaozhi.MaybeEnterBlackjackFromIntent(intent)
	strm.respOnce.Do(func() {
		strm.receiver.OnIntent(&cloud.IntentResult{
			Intent:     intent,
			Parameters: paramsJSON,
		})
	})
	log.Printf("[Xiaozhi][KeepawayFlow] step=intent_sent_to_engine intent=%s (mic stays closed)", intent)
	log.Println("[Xiaozhi] self-control done — mic closed until manual wake; session:", xiaozhi.ActiveSessionID())
	return true
}

func (strm *Streamer) runLocalVoskTurn() {
	// Blackjack uses the tiny grammar already prepared; never EnsureVosk() full model
	// (that reloads ~90MB after an OOM Exit Unload and fights Xiaozhi).
	if xiaozhi.InBlackjackGameMode() {
		xiaozhi.NoteBlackjackVoskActivity("vosk listen start")
		if !xiaozhi.BlackjackSTTReady() {
			if !xiaozhi.PrepareBlackjackSTT("vosk turn") {
				log.Println("[Game] Vosk not ready — abort blackjack listen")
				strm.respOnce.Do(func() {
					strm.receiver.OnError(cloud.ErrorType_Server, fmt.Errorf("blackjack STT unavailable"))
				})
				return
			}
		}
	} else if err := vtr.EnsureVosk(); err != nil {
		log.Println("[Vosk] EnsureVosk:", err)
		strm.respOnce.Do(func() {
			strm.receiver.OnError(cloud.ErrorType_Server, err)
		})
		return
	}
	strm.receiver.OnStreamOpen("vosk")

	var curFreq string
	var underClockAfter bool
	if doFreqStuff {
		curFreq = vtr.GetFreq()
		o, err := strconv.Atoi(curFreq)
		if err == nil {
			if o < 729600 {
				underClockAfter = true
				go vtr.SetFreq("729600", "600000")
			}
		}
	}
	for data := range strm.audioStream {
		text := vtr.Process(data)
		if text != "" {
			intent, iParam, _ := vtr.ProcessTextAll(text, vtr.IntentList)
			log.Println("[Vosk] matched:", intent, "text:", text)
			if xiaozhi.InBlackjackGameMode() {
				xiaozhi.NoteBlackjackVoskActivity("vosk matched " + intent)
			}
			sendIntentGraphResponse(&chippergrpc2.IntentGraphResponse{
				ResponseType: chippergrpc2.IntentGraphMode_INTENT,
				IsFinal:      true,
				IntentResult: &chippergrpc2.IntentResult{
					Action:     intent,
					Parameters: iParam,
				},
			}, strm.receiver)
			if doFreqStuff {
				if underClockAfter {
					vtr.SetFreq(curFreq, "400000")
				}
			}
			return
		}
	}
}

// runXiaozhiTurn replaces the vtr loop when Xiaozhi is enabled.
// It streams mic audio to Xiaozhi, waits for STT+TTS, then dispatches the result.
func (strm *Streamer) runXiaozhiTurn() {
	cfg := xiaozhi.GetConfig()
	log.Println("[Xiaozhi][Mic] turn start; tts:", cfg.TTSMode)

	turnID := xiaozhi.BeginXiaozhiTurn()
	xiaozhi.ClearPendingMCPIntent("xiaozhi turn start")

	// Tell the engine the "cloud" stream is open. Without this, listening times out
	// in ~5s and plays NoCloud even when Xiaozhi STT/TTS succeeds.
	strm.receiver.OnStreamOpen("xiaozhi")
	streamOpenedAt := time.Now()

	var afterSTT func(string)
	if cfg.TTSMode == "xiaozhi" {
		var afterSTTOnce sync.Once
		afterSTT = func(stt string) {
			_ = stt
			afterSTTOnce.Do(func() {
				// Fire soon after mic ends / call site. Waiting until +9s lost to the
				// engine ~5s Intent timeout (NoCloudIcon). Do not arm at turn start —
				// that closed the Vector mic mid-utterance.
				go func(id uint64) {
					deadline := streamOpenedAt.Add(4500 * time.Millisecond)
					if wait := time.Until(deadline); wait > 0 {
						time.Sleep(wait)
					}
					if xiaozhi.XiaozhiTurnCurrent() != id {
						log.Println("[Xiaozhi] skip stale afterSTT (turn superseded)")
						return
					}
					if intent, params, ok := xiaozhi.TakeMCPIntentForSameStreamDelivery(); ok {
						paramsJSON := xiaozhi.EncodeIntentParamsJSON(params)
						xiaozhi.MaybeEnterBlackjackFromIntent(intent)
						sent := false
						strm.respOnce.Do(func() {
							strm.receiver.OnIntent(&cloud.IntentResult{
								Intent:     intent,
								Parameters: paramsJSON,
							})
							sent = true
						})
						if sent {
							log.Println("[Xiaozhi] MCP intent delivered on same listen (no FakeTrigger):", intent)
							return
						}
						log.Println("[Xiaozhi] same-listen MCP delivery missed respOnce — will try after TTS:", intent)
						xiaozhi.SetDeferredIntent(intent, params)
						return
					}
					strm.respOnce.Do(func() {
						strm.receiver.OnIntent(&cloud.IntentResult{
							Intent:     "intent_system_noaudio",
							Parameters: "{}",
						})
					})
				}(turnID)
			})
		}
	}

	result, err := xiaozhi.RunTurn(strm.ctx, strm.audioStream, cfg, afterSTT)
	if err != nil {
		if errors.Is(err, xiaozhi.ErrAborted) {
			log.Println("[Xiaozhi] turn aborted (barge-in)")
			return
		}
		log.Println("[Xiaozhi] turn error:", err)
		// Stuck listen (cloud open, anim never streamed mic): kick another FakeTrigger.
		errText := err.Error()
		if xiaozhi.ContinuousMode() && !xiaozhi.InBlackjackGameMode() && strings.Contains(errText, "no mic uplink") {
			time.Sleep(800 * time.Millisecond)
			if rerr := xiaozhi.TriggerRelisten(); rerr != nil {
				log.Println("[Xiaozhi] relisten after mic miss:", rerr)
			} else {
				log.Println("[Xiaozhi] relisten armed after mic uplink miss")
			}
		}
		// STT timeout / empty after silence: end the turn. Do not FakeTrigger again —
		// that caused open→timeout→open spam when the user stayed quiet.
		// Exception: just left blackjack — reopen mic once so chat can start.
		if strings.Contains(errText, "timeout waiting for STT") ||
			strings.Contains(errText, "empty STT") {
			if xiaozhi.ConsumeReopenMicAfterBlackjack() {
				time.Sleep(400 * time.Millisecond)
				if rerr := xiaozhi.TriggerRelisten(); rerr != nil {
					log.Println("[Game] reopen mic after EXIT silence:", rerr)
				} else {
					log.Println("[Game] Xiaozhi mic reopened after blackjack EXIT — speak now")
				}
			} else {
				log.Println("[Xiaozhi] silence after listen — mic closed until Hey Vector / button")
			}
		}
		strm.respOnce.Do(func() {
			strm.receiver.OnError(cloud.ErrorType_Server, err)
		})
		return
	}

	// ESP32-style: play confirmation TTS first, then run MCP robot intent.
	if result.StreamedAudio {
		log.Printf("[Xiaozhi][TTS] wait speaker ~%dms; session: %s",
			result.PCMBytes/32, xiaozhi.ActiveSessionID())
		// Drop anim pending-relisten so OnAudioCompleted does not FakeTrigger early
		// while we still wait for PCM drain — that caused mic open → mic open again.
		xiaozhi.DisarmRelistenPending()
		idleOK := xiaozhi.WaitPlaybackIdle(result.PCMBytes, result.AudioStartedAt, 120*time.Second)
		// Free tmpfs PCM as soon as speaker is done (long stories otherwise linger).
		xiaozhi.CleanupPlaybackFiles()
		xiaozhi.SetPlaying(false)
		if !idleOK {
			// Speaker path left dirty (common after analyze_photo + underrun).
			_ = xiaozhi.RequestCancelPlayback()
			xiaozhi.ClearCancelFlags()
			time.Sleep(250 * time.Millisecond)
		}
	}

	if result.RobotIntent != "" {
		paramsJSON := xiaozhi.EncodeIntentParamsJSON(result.RobotIntentParams)
		xiaozhi.MaybeEnterBlackjackFromIntent(result.RobotIntent)
		if xiaozhi.MCPAlreadyDeliveredOnSameStream(result.RobotIntent) {
			if result.RobotIntent == "intent_play_blackjack" {
				xiaozhi.PrepareBlackjackSTT("after TTS, already delivered")
			}
			xiaozhi.ClearPendingMCPIntent("after TTS, already delivered")
			log.Println("[Xiaozhi] self-control already delivered on same listen — no FakeTrigger; mic closed until manual wake:", result.RobotIntent)
			return
		}
		// Cube games / photo: FakeTrigger a fresh listen so VoiceFeatures can claim the
		// intent. Same-stream follow-up after long TTS was ForceCleared (ReactToHand /
		// HeldInPalm) — Keepaway never reached FindCube.
		if xiaozhi.NeedsFreshListenDelivery(result.RobotIntent) {
			log.Printf("[Xiaozhi][KeepawayFlow] step=after_tts_defer intent=%s (FakeTrigger next)", result.RobotIntent)
			xiaozhi.SetDeferredIntent(result.RobotIntent, result.RobotIntentParams)
			xiaozhi.ClearPendingMCPIntent("defer cube/photo for fresh listen")
			time.Sleep(400 * time.Millisecond)
			if err := xiaozhi.TriggerRelisten(); err != nil {
				log.Printf("[Xiaozhi][KeepawayFlow] step=faketrigger_FAIL intent=%s err=%v", result.RobotIntent, err)
			} else {
				log.Printf("[Xiaozhi][KeepawayFlow] step=faketrigger_armed intent=%s — expect deliver_on_new_stream next", result.RobotIntent)
			}
			return
		}
		// Prefer same-stream OnIntent (no FakeTrigger). Stream may still be open after noaudio.
		if result.RobotIntent == "intent_play_keepaway" || result.RobotIntent == "intent_play_popawheelie" ||
			result.RobotIntent == "intent_play_rollcube" || result.RobotIntent == "intent_play_anytrick" {
			log.Printf("[Xiaozhi][KeepawayFlow] step=after_tts_same_stream intent=%s (no FakeTrigger, mic stays closed)", result.RobotIntent)
		}
		log.Println("[Xiaozhi] dispatching self-control intent (no FakeTrigger):", result.RobotIntent)
		delivered := false
		strm.respOnce.Do(func() {
			strm.receiver.OnIntent(&cloud.IntentResult{
				Intent:     result.RobotIntent,
				Parameters: paramsJSON,
			})
			delivered = true
		})
		if !delivered {
			// noaudio already consumed respOnce — send follow-up Result on kept-open stream.
			log.Println("[Xiaozhi] respOnce used — sending MCP intent as follow-up on same stream")
			strm.receiver.OnIntent(&cloud.IntentResult{
				Intent:     result.RobotIntent,
				Parameters: paramsJSON,
			})
		}
		if result.RobotIntent == "intent_play_blackjack" {
			xiaozhi.PrepareBlackjackSTT("after TTS dispatch")
		}
		xiaozhi.ClearPendingMCPIntent("after TTS self-control dispatch")
		if result.RobotIntent == "intent_play_keepaway" || result.RobotIntent == "intent_play_popawheelie" ||
			result.RobotIntent == "intent_play_rollcube" || result.RobotIntent == "intent_play_anytrick" {
			log.Printf("[Xiaozhi][KeepawayFlow] step=intent_sent_to_engine intent=%s (mic closed, no FakeTrigger)", result.RobotIntent)
		}
		log.Println("[Xiaozhi] self-control dispatched — mic closed until manual wake; session:", xiaozhi.ActiveSessionID())
		return
	}

	// Safety: early same-listen delivery may have raced Peek before TurnResult filled
	// RobotIntent — never continuous-relisten over an already-dispatched self-control.
	if intent, _, ok := xiaozhi.PeekMCPIntentQueued(); ok && xiaozhi.MCPAlreadyDeliveredOnSameStream(intent) {
		xiaozhi.ClearPendingMCPIntent("after TTS, delivered but RobotIntent empty")
		log.Println("[Xiaozhi] self-control already delivered (RobotIntent race) — mic closed until manual wake:", intent)
		return
	}

	if result.StreamedAudio {
		// Server bye closes WSS (log: closed WSS session … turn_end). That is the
		// flag: if session is gone, do not continuous-relisten / reopen mic.
		if result.EndConversation || !xiaozhi.SessionAlive() {
			xiaozhi.DisarmRelistenPending()
			log.Println("[Xiaozhi] WSS closed — mic closed until Hey Vector / button (no auto-relisten)")
			xiaozhi.ClearPendingMCPIntent("wss closed")
			log.Println("[Xiaozhi][TTS] turn done; session:", xiaozhi.ActiveSessionID())
			return
		}
		if xiaozhi.ContinuousMode() && !xiaozhi.InBlackjackGameMode() {
			if cool := xiaozhi.AnalyzeCooldownRemaining(); cool > 0 {
				log.Printf("[Xiaozhi] post-analyze cooldown %v before relisten (RAM recover)", cool.Round(time.Millisecond))
				xiaozhi.CleanupPlaybackFiles()
				xiaozhi.SetPlaying(false)
				time.Sleep(cool)
			}
			time.Sleep(350 * time.Millisecond)
			// Re-check: idle/goodbye may have closed WSS while we waited for speaker.
			if !xiaozhi.SessionAlive() {
				xiaozhi.DisarmRelistenPending()
				log.Println("[Xiaozhi] WSS closed during playback wait — skip relisten")
				xiaozhi.ClearPendingMCPIntent("wss closed after playback")
				return
			}
			if err := xiaozhi.TriggerRelisten(); err != nil {
				log.Println("[Xiaozhi] relisten trigger:", err)
			} else {
				xiaozhi.ClearReopenMicAfterBlackjack()
				log.Println("[Xiaozhi][Mic] relisten after playback")
			}
		}
		xiaozhi.ClearPendingMCPIntent("chat TTS done")
		log.Println("[Xiaozhi][TTS] turn done; session:", xiaozhi.ActiveSessionID())
		return
	}

	answerText := result.ResponseText
	if answerText == "" {
		answerText = result.STTText
	}
	if answerText == "" {
		answerText = "(no response)"
	}

	useXiaozhiTTS := cfg.TTSMode == "xiaozhi" && len(result.PCMChunks) > 0

	if useXiaozhiTTS {
		// Fallback one-shot path if streaming failed to start this turn.
		playStart := time.Now()
		var allPCM []byte
		for _, chunk := range result.PCMChunks {
			allPCM = append(allPCM, chunk...)
		}
		pcmMs := len(allPCM) / 32 // 16kHz s16le mono
		log.Printf("[Xiaozhi] preparing speaker (one-shot fallback): %d bytes PCM (~%dms)",
			len(allPCM), pcmMs)
		if err := xiaozhi.WritePCMFile(allPCM); err != nil {
			log.Println("[Xiaozhi] write PCM file:", err)
		} else {
			xiaozhi.DisarmRelistenPending()
			xiaozhi.SetPlaying(true)
			if err := xiaozhi.RequestExternalAudioPlay(); err != nil {
				xiaozhi.SetPlaying(false)
				log.Println("[Xiaozhi] request ExternalAudio play:", err)
			} else {
				log.Printf("[Xiaozhi] ExternalAudio play flag set (+%v)",
					time.Since(playStart).Round(time.Millisecond))
				_ = xiaozhi.WaitPlaybackIdle(len(allPCM), playStart, 120*time.Second)
				if result.EndConversation || !xiaozhi.SessionAlive() {
					xiaozhi.DisarmRelistenPending()
					log.Println("[Xiaozhi] WSS closed after one-shot — no relisten")
				} else if xiaozhi.ContinuousMode() && !xiaozhi.InBlackjackGameMode() {
					if err := xiaozhi.TriggerRelisten(); err != nil {
						log.Println("[Xiaozhi] relisten after one-shot:", err)
					} else {
						xiaozhi.ClearReopenMicAfterBlackjack()
					}
				}
			}
		}
		return
	}

	// Fallback / tts_mode=acapela: KnowledgeGraph + Acapela speaks the text.
	params := map[string]string{
		"answer":      answerText,
		"answer_type": "NoAnswerFound",
		"query_text":  result.STTText,
	}
	paramBytes, _ := json.Marshal(params)

	strm.respOnce.Do(func() {
		strm.receiver.OnIntent(&cloud.IntentResult{
			Intent:     "intent_knowledge_response_extend",
			Parameters: fmt.Sprintf("%s\n", string(paramBytes)),
		})
	})

	if xiaozhi.ContinuousMode() && !xiaozhi.InBlackjackGameMode() &&
		!result.EndConversation && xiaozhi.SessionAlive() {
		go func() {
			// Rough wait for Acapela to finish speaking before relisten.
			time.Sleep(4 * time.Second)
			if !xiaozhi.SessionAlive() {
				return
			}
			if err := xiaozhi.TriggerRelisten(); err != nil {
				log.Println("[Xiaozhi] relisten trigger:", err)
			} else {
				xiaozhi.ClearReopenMicAfterBlackjack()
			}
		}()
	}
}
