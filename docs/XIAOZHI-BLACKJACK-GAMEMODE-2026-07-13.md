# Xiaozhi + Blackjack gameMode (steps 2–4)

Date: 2026-07-13  
Depends on: `XIAOZHI-SELF-CONTROL-NO-RELISTEN-2026-07-13.md` (step 1 — no auto-mic after MCP)

## Behavior

| Event | Action |
|-------|--------|
| MCP `intent_play_blackjack` | `CloseSession` Xiaozhi WSS, `EnsureVosk`, `gameMode=blackjack` |
| Engine `StreamType_Blackjack` mic (hit/stand/play again) | Local Vosk → yes/no/hit/stand intents; Acapela TTS stays in engine |
| Other MCP self-control | No auto-relisten (step 1); no Vosk |
| Chat without MCP | Continuous Xiaozhi as before |
| Normal wake while `gameMode` and last Blackjack mic **&lt; 45s** ago | Stay on Vosk (mid-game Hey Vector) |
| Normal wake while `gameMode` and idle **≥ 45s** since last Blackjack mic | `UnloadVosk`, clear `gameMode`, this wake uses Xiaozhi again |
| `gameMode` older than 20 min | Force clear + unload Vosk |

## Files

| File | Role |
|------|------|
| `internal/xiaozhi/gamemode.go` | Enter/Exit/ShouldUseLocalVosk |
| `internal/voice/vtr/vosk.go` | `EnsureVosk` / `UnloadVosk` (+ mutex) |
| `internal/voice/vtr/intents.go` | Idempotent `loadIntents` |
| `internal/voice/stream/init.go` | Deferred deliver first; Vosk branch; blackjack enter on MCP |

## Test plan

1. Chat → continuous mic OK.
2. Fireworks MCP → runs, mic stays closed until Hey Vector / button.
3. “Play blackjack” MCP → log `blackjack gameMode ON`, WSS closed, Vosk loaded.
4. Hit/stand prompts → log `[Vosk] matched:` affirmative/negative/hit/stand.
5. Play again → say **no** → quit. Wait **≥ 45s**, then Hey Vector → log `gameMode OFF`, Xiaozhi chat works.
6. Watch RSS: Vosk should not stay loaded after step 5.

## Revert

```bash
cd anki/vic-cloudless
# remove game mode + restore init/vosk/intents from before this change
git checkout -- \
  internal/xiaozhi/gamemode.go \
  internal/voice/vtr/vosk.go \
  internal/voice/vtr/intents.go \
  internal/voice/stream/init.go
```

Or delete `gamemode.go` and restore the three other files from backup / prior commit. Keep step-1 no-relisten patches in `init.go` if you only want to drop blackjack.

## Crash fix 2026-07-13 — blackjack EnsureVosk panic

**Symptom:** `intent_play_blackjack` → `CloseSession` mid-TTS + `EnsureVosk` → `GetGrammerList` called `model.FindWord` on nil → SIGSEGV, vic-cloud restart loop.

**Fix:**
1. Assign package `model` before `GetGrammerList`; nil-guard `FindWord`.
2. `MaybeEnterBlackjackFromIntent` only **marks** gameMode; `PrepareBlackjackSTT` (CloseSession + EnsureVosk) runs **after TTS** / on first Blackjack mic.
