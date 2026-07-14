# Xiaozhi self-control — no auto-relisten after MCP

Date: 2026-07-13  
Change: after MCP / self-control `intent_*` runs, **do not** open the mic again. User must **Hey Vector** or press the backpack button. Normal chat (no MCP) keeps continuous FakeTrigger as before.

Blackjack Vosk/gameMode (lazy load / CloseSession) is **not** in this change — planned separately.

## Behavior

| Path | After turn | Mic |
|------|------------|-----|
| Chat TTS only (no `RobotIntent`) | `TriggerRelisten` / `ArmRelistenAfterPlay` | Continuous — same as before |
| STT miss / no mic uplink (continuous) | `TriggerRelisten` | Continuous — same as before |
| MCP self-control delivered on current stream | **No** `TriggerRelisten` | Closed until manual wake |
| MCP after TTS confirm (deferred) | **One** FakeTrigger only to deliver deferred `OnIntent`, then **no** further relisten | Brief open for delivery, then closed |
| After deferred intent delivered | **No** 5s sleep + relisten | Closed until manual wake |

## Code touched

File: `internal/voice/stream/init.go` → `runXiaozhiTurn()`

1. `TakeDeferredIntent` success path — removed `Sleep(5s)` + `TriggerRelisten`.
2. Direct `OnIntent` when `RobotIntent` already delivered — removed `Sleep(5s)` + `TriggerRelisten`.
3. Defer + FakeTrigger when TTS confirm / noaudio already sent — **kept one** `TriggerRelisten` so engine still receives the intent; logs say “no continuous after”.

## How to test

1. Chat: “xin chào” → TTS → mic should open again (continuous).
2. Self-control: “bắn pháo hoa” → MCP → confirm TTS (if any) → fireworks runs → **mic stays closed**.
3. Wake with Hey Vector or backpack → chat works again.
4. Confirm fireworks is **not** cut mid-animation by a 5s FakeTrigger.

## Revert

Restore continuous auto-mic after self-control by putting back the blocks below in `runXiaozhiTurn()`.

### 1) After `TakeDeferredIntent` deliver

Replace the early-return after deferred deliver with:

```go
		if xiaozhi.ContinuousMode() {
			time.Sleep(5 * time.Second) // let SeasonalHappyNewYear / dance / etc. run
			_ = xiaozhi.TriggerRelisten()
		}
		return
```

(Remove the log line `self-control done — mic closed until manual wake` if you want the old log noise only.)

### 2) After direct MCP `OnIntent` (`delivered == true`)

After the `if !delivered { ... return }` block, restore:

```go
		if xiaozhi.ContinuousMode() {
			time.Sleep(5 * time.Second)
			_ = xiaozhi.TriggerRelisten()
		}
		log.Println("[Xiaozhi] turn done (MCP intent dispatched); session:", xiaozhi.ActiveSessionID())
		return
```

### 3) Deferred FakeTrigger paths (optional)

Current code always FakeTriggers once to deliver deferred intent (even if continuous mode is off). Old code wrapped those in `if xiaozhi.ContinuousMode()`. To match old gating exactly:

```go
			if xiaozhi.ContinuousMode() {
				time.Sleep(400 * time.Millisecond)
				_ = xiaozhi.TriggerRelisten()
			}
```

Note: with continuous off, deferred delivery then needs another manual wake — usually worse. Prefer keeping the one-shot FakeTrigger for delivery.

### Git-style revert (if committed)

```bash
cd anki/vic-cloudless
git checkout -- internal/voice/stream/init.go
# or revert the commit that introduced this doc + change
```

### Deploy reminder

After rebuild `vic-cloud`, copy via `/data` if rootfs is RO, then restart cloud (and switchboard order as usual).

## Follow-up 2026-07-13 — self-control delivery (no FakeTrigger)

**Working path (user-confirmed):** When MCP is queued before the ~9s listen deadline, deliver `intent_*` **on the same listen** instead of `noaudio`. No FakeTrigger. Mic stays closed after (manual wake).

Log: `MCP intent delivered on same listen (no FakeTrigger)` then after TTS `already delivered on same listen — no FakeTrigger`.

Fallback if same-listen missed: follow-up `OnIntent` on kept-open stream after TTS (still no FakeTrigger).

## Not changed

- `xiaozhi.ContinuousMode()` for pure chat
- Keyword / local self-control bypass (still removed — MCP only)
- Blackjack gameMode / Vosk lazy load / `CloseSession` on blackjack
- Engine / Acapela / ActiveFeature flag
