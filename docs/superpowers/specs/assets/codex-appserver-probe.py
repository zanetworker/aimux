#!/usr/bin/env python3
"""Throwaway probe: can codex app-server act as a stateless "model" for Claude Code?

Checks, against the real installed Codex:
  1. ephemeral thread + baseInstructions + dynamicTools + native tools off
  2. thread/inject_items with a prior function_call / function_call_output
  3. text streams as deltas, then the model calls our dynamic tool
  4. turn/interrupt right after item/tool/call ends the turn cleanly
Prints a compact event log; never prints auth material.
"""
import json, os, subprocess, sys, tempfile, threading, time, queue

DISABLE = ["shell_tool", "unified_exec", "unified_exec_tty", "view_image", "image_generation",
           "apps", "plugins", "browser_use", "browser_use_external", "computer_use", "multi_agent",
           "skill_search", "tool_suggest", "goals", "sleep_tool", "hooks", "in_app_browser",
           "realtime_conversation", "worktrees"]

args = ["codex-lean", "app-server"]
for f in DISABLE:
    args += ["--disable", f]
args += ["-c", 'web_search="disabled"', "-c", "tools.update_plan.enabled=false"]

workdir = tempfile.mkdtemp(prefix="codex-bridge-probe-")
p = subprocess.Popen(args, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                     text=True, cwd=workdir, bufsize=1)
msgs = queue.Queue()
threading.Thread(target=lambda: [msgs.put(json.loads(l)) for l in p.stdout if l.strip()], daemon=True).start()
errlines = []
threading.Thread(target=lambda: [errlines.append(l) for l in p.stderr], daemon=True).start()

nid = [0]
def send(method, params=None, rid=None):
    m = {"jsonrpc": "2.0", "method": method}
    if params is not None:
        m["params"] = params
    if rid is None:
        nid[0] += 1
        rid = nid[0]
    m["id"] = rid
    p.stdin.write(json.dumps(m) + "\n"); p.stdin.flush()
    return rid

def notify(method, params=None):
    m = {"jsonrpc": "2.0", "method": method}
    if params is not None:
        m["params"] = params
    p.stdin.write(json.dumps(m) + "\n"); p.stdin.flush()

def wait_result(rid, timeout=60):
    end = time.time() + timeout
    while time.time() < end:
        try:
            m = msgs.get(timeout=1)
        except queue.Empty:
            continue
        if m.get("id") == rid and ("result" in m or "error" in m):
            return m
        log(m)
    raise SystemExit(f"timeout waiting for {rid}; stderr tail: {''.join(errlines[-5:])}")

seen = {}
def log(m):
    meth = m.get("method", "response")
    seen[meth] = seen.get(meth, 0) + 1
    if meth in ("item/agentMessage/delta",):
        return
    if seen[meth] <= 2:
        print("  evt", meth, json.dumps(m.get("params", m.get("result", m.get("error"))))[:220])

t0 = time.time()
r = wait_result(send("initialize", {"clientInfo": {"name": "aimux-probe", "version": "0"},
                                    "capabilities": {"experimentalApi": True}}))
print("initialize:", "ok" if "result" in r else r)
notify("initialized")

read_tool = {"type": "function", "name": "Read", "description": "Read a file from the local filesystem.",
             "inputSchema": {"type": "object", "properties": {"file_path": {"type": "string"}},
                             "required": ["file_path"]}}
r = wait_result(send("thread/start", {
    "ephemeral": True, "cwd": workdir, "sandbox": "read-only", "approvalPolicy": "never",
    "model": "gpt-6.2-sol",
    "baseInstructions": "You are Claude Code, a coding agent. You can only act through the tools provided. "
                        "Use the Read tool to read files. Answer briefly.",
    "dynamicTools": [read_tool]}))
if "error" in r:
    raise SystemExit(f"thread/start error: {r['error']}")
tid = r["result"]["thread"]["id"]
print("thread/start: ok, ephemeral thread", tid[:8], f"({time.time()-t0:.1f}s since spawn)")

history = [
    {"type": "message", "role": "user", "content": [{"type": "input_text", "text": "Read /work/a.txt"}]},
    {"type": "function_call", "name": "Read", "call_id": "toolu_prev1", "arguments": json.dumps({"file_path": "/work/a.txt"})},
    {"type": "function_call_output", "call_id": "toolu_prev1", "output": "the secret fruit is PAPAYA"},
    {"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "a.txt says the secret fruit is PAPAYA."}]},
]
r = wait_result(send("thread/inject_items", {"threadId": tid, "items": history}))
print("thread/inject_items:", "ok" if "result" in r else r.get("error"))
failures = [] if "result" in r else ["thread/inject_items rejected"]

t1 = time.time()
rid = send("turn/start", {"threadId": tid, "effort": "low", "input": [{"type": "text", "text":
        "First say in one short sentence what the secret fruit was. Then read /work/b.txt with the Read tool."}]})
text, tool_calls, turn_id, first_delta = [], [], None, None
interrupt_id, interrupt_ok, status = None, False, None
end = time.time() + 120
while time.time() < end:
    try:
        m = msgs.get(timeout=1)
    except queue.Empty:
        continue
    meth = m.get("method")
    if m.get("id") == rid and "result" in m:
        turn_id = m["result"]["turn"]["id"]; continue
    if meth == "item/agentMessage/delta":
        if first_delta is None:
            first_delta = time.time() - t1
        text.append(m["params"].get("delta", ""))
        seen[meth] = seen.get(meth, 0) + 1
        continue
    if meth == "item/tool/call":
        tool_calls.append(m["params"])
        print("  SERVER REQUEST item/tool/call:", json.dumps(m["params"])[:300])
        interrupt_id = send("turn/interrupt", {"threadId": tid, "turnId": turn_id or m["params"].get("turnId")})
        continue
    if meth and meth.endswith("requestApproval"):
        print("  native approval request (native tool still active!):", meth)
        send_resp = {"jsonrpc": "2.0", "id": m["id"], "result": {"decision": "decline"}}
        p.stdin.write(json.dumps(send_resp) + "\n"); p.stdin.flush()
        continue
    if interrupt_id is not None and m.get("id") == interrupt_id:
        interrupt_ok = "result" in m
        print("  turn/interrupt response:", "ok" if interrupt_ok else m.get("error"))
        continue
    log(m)
    if meth == "turn/completed":
        status = m["params"].get("turn", {}).get("status")
        print("  turn/completed status:", status)
        break

print("\nRESULT")
print("  streamed text deltas:", seen.get("item/agentMessage/delta", 0), "first after", f"{first_delta:.1f}s" if first_delta else "n/a")
print("  text:", "".join(text)[:300])
print("  dynamic tool calls:", [(c.get("tool"), c.get("arguments")) for c in tool_calls])
print("  turn wall time:", f"{time.time()-t1:.1f}s")
print("  event counts:", {k: v for k, v in seen.items()})
p.terminate()
if status is None: failures.append("turn did not complete before the timeout")
if not text: failures.append("no streamed text")
if [(c.get("tool"), c.get("arguments")) for c in tool_calls] != [("Read", {"file_path": "/work/b.txt"})]:
    failures.append(f"expected exactly one Read(/work/b.txt) call, got {tool_calls}")
if not interrupt_ok: failures.append("turn/interrupt was not acknowledged")
if status != "interrupted": failures.append(f"turn status {status!r}, expected 'interrupted'")
if "PAPAYA" not in "".join(text).upper(): failures.append("injected history was not used (no PAPAYA)")
print("PROBE", "FAIL: " + "; ".join(failures) if failures else "PASS")
sys.exit(1 if failures else 0)
