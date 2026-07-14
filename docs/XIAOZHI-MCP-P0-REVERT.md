# Xiaozhi MCP P0 — revert guide (2026-07-14)

P0 fix: MCP `initialize` / `tools/list` no longer get dropped after silence timeouts or
`DrainEventChannels`, so the server keeps self-control tools (`self.camera.take_photo`, …).

If this build misbehaves (TTS hangs, double tool calls, MCP spam), revert as below.

Robot: restore previous `/anki/bin/vic-cloud` if you kept a backup; otherwise rebuild from
the pre-P0 tree and re-run `./deploy.sh <IP>`.

---

## Symptom this fixed

After a new WSS session + silence STT timeout, Xiaozhi said “không có chức năng chụp hình”
even though an earlier turn had successfully called `self.camera.take_photo`.

Cause: MCP handshake messages sat on `mcpCh`, then were discarded by `DrainEventChannels`
on the next listen; tools were never registered for that session.

---

## Files touched (P0)

| File | Change |
|------|--------|
| `internal/xiaozhi/client.go` | `DrainEventChannels` **no longer** drains `mcpCh` |
| `internal/xiaozhi/mcp.go` | Handshake flags + logs for initialize / tools/list |
| `internal/xiaozhi/mcp_pump.go` | **NEW** — session-lifetime MCP pump |
| `internal/xiaozhi/session.go` | Start/stop pump on Dial/close; wait handshake after Dial; `PeekMCPIntentQueued` |
| `internal/xiaozhi/turn.go` | Use session MCP pump; peek queue after STT / during TTS; no per-turn `NewMCPHandler` racing `mcpCh` |

---

## How to revert (source)

From `anki/vic-cloudless`:

```bash
# If this tree is under git:
git checkout -- \
  internal/xiaozhi/client.go \
  internal/xiaozhi/mcp.go \
  internal/xiaozhi/session.go \
  internal/xiaozhi/turn.go
rm -f internal/xiaozhi/mcp_pump.go

# Then rebuild + deploy
make vic-cloud   # or your usual make target
./deploy.sh <ROBOT_IP>
```

If **not** using git, restore these behaviors manually:

1. **`client.go` `DrainEventChannels`** — put `case <-c.mcpCh:` back in the select drain loop.
2. **Delete** `mcp_pump.go`.
3. **`session.go`** — remove `mcp` / `mcpCancel`, `startMCPPumpLocked`, `stopMCPPumpLocked`,
   `SessionMCP`, `PeekMCPIntentQueued`, and handshake wait in `EnsureConnected`.
   On Dial only assign `sess.client` (no pump). `closeSessionLocked` without `stopMCPPumpLocked`.
4. **`mcp.go`** — remove `context` import, handshake fields (`hsMu`, `initDone`, `toolsDone`,
   `toolsReady`), `WaitToolsListed` / `ToolsListed` / mark helpers; `handleInitialize` /
   `HandleToolsList` go back to plain `sendResult` without the `[Xiaozhi] MCP ← …` logs.
5. **`turn.go`** — restore:
   - `mcpHandler := NewMCPHandler(client)` after drain (and after redial)
   - post-STT `for { select { case mcpMsg := <-client.MCPCh(): … } }`
   - TTS `case mcpMsg := <-client.MCPCh():` using `mcpHandler` (no fallback/`Peek` idle sync)

---

## How to confirm P0 is working

On robot logs after wake / new session you should see, **before or early in listen**:

```text
[Xiaozhi] MCP pump started; session: …
[Xiaozhi] MCP ← initialize → replied …
[Xiaozhi] MCP ← tools/list → replied (N tools)
[Xiaozhi] MCP handshake OK …
```

Regression test:

1. Wake → silence until STT timeout (“mic closed until Hey Vector”).
2. Wake again → “chụp hình đi”.
3. Expect `MCP self.camera.take_photo` (or pump “MCP action queued…”), **not** “không có camera”.

---

## Binary backup tip (recommended before deploy)

```bash
ssh root@<IP> 'cp -a /anki/bin/vic-cloud /anki/bin/vic-cloud.bak-pre-mcp-p0'
# revert on robot:
ssh root@<IP> 'mount -o rw,remount /; cp -a /anki/bin/vic-cloud.bak-pre-mcp-p0 /anki/bin/vic-cloud; systemctl restart anki-robot.target'
```
