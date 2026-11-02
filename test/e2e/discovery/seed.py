"""Write demo Claude Code sessions under $HOME for the README recording.

Usage: python3 seed.py  (uses $HOME). Prints "project session_id" per line.
"""
import json, os, uuid
from datetime import datetime, timedelta, timezone

HOME = os.environ["HOME"]
NOW = datetime.now(timezone.utc)
MODEL = "claude-opus-5-5"

def ts(sec_ago):
    return (NOW - timedelta(seconds=sec_ago)).strftime("%Y-%m-%dT%H:%M:%S.000Z")

def usage(i, o, cr):
    return {"input_tokens": i, "output_tokens": o, "cache_creation_input_tokens": 0, "cache_read_input_tokens": cr}

class Session:
    def __init__(self, project, branch):
        self.sid, self.cwd, self.branch, self.lines, self.n = str(uuid.uuid4()), f"/work/{project}", branch, [], 0
        self.project = project
    def base(self, kind, t):
        return {"type": kind, "sessionId": self.sid, "cwd": self.cwd, "gitBranch": self.branch, "timestamp": ts(t)}
    def user(self, text, t):
        self.lines.append({**self.base("user", t), "message": {"role": "user", "content": text}})
    def say(self, text, t, stop="end_turn"):
        self.lines.append({**self.base("assistant", t), "message": {"model": MODEL, "role": "assistant", "stop_reason": stop,
            "content": [{"type": "text", "text": text}], "usage": usage(4200, 1900, 210000)}})
    def tool(self, name, inp, result, t, answered=True):
        self.n += 1; tid = f"toolu_{self.sid[:6]}_{self.n}"
        self.lines.append({**self.base("assistant", t), "message": {"model": MODEL, "role": "assistant", "stop_reason": "tool_use",
            "content": [{"type": "tool_use", "id": tid, "name": name, "input": inp}], "usage": usage(6800, 2600, 340000)}})
        if answered:
            self.lines.append({**self.base("user", t - 2), "message": {"role": "user",
                "content": [{"type": "tool_result", "tool_use_id": tid, "content": result}]}})
    def write(self):
        d = os.path.join(HOME, ".claude", "projects", self.cwd.replace("/", "-"))
        os.makedirs(d, exist_ok=True)
        with open(os.path.join(d, self.sid + ".jsonl"), "w") as f:
            for l in self.lines: f.write(json.dumps(l) + "\n")
        os.makedirs(self.cwd, exist_ok=True)
        print(self.project, self.sid)

# payments-api: active, mid-task
s = Session("payments-api", "feat/idempotency")
s.user("Add idempotency keys to POST /charges so retries never double-charge", 900)
s.tool("Read", {"file_path": "/work/payments-api/handlers/charges.go"}, "func CreateCharge(w http.ResponseWriter, r *http.Request) { ... }", 880)
s.tool("Edit", {"file_path": "/work/payments-api/handlers/charges.go",
    "old_string": "func CreateCharge(w http.ResponseWriter, r *http.Request) {",
    "new_string": "func CreateCharge(w http.ResponseWriter, r *http.Request) {\n\tkey := r.Header.Get(\"Idempotency-Key\")\n\tif prior, ok := store.Lookup(key); ok {\n\t\twriteJSON(w, prior)\n\t\treturn\n\t}"},
    "Applied edit to handlers/charges.go", 840)
s.tool("Bash", {"command": "go test ./handlers/..."}, "ok  \tpayments-api/handlers\t0.412s", 790)
s.say("Idempotency keys are stored for 24h; a retried request returns the original charge. Adding a migration next.", 760)
s.user("Good. Also cover the refund endpoint", 120)
s.tool("Edit", {"file_path": "/work/payments-api/handlers/refunds.go", "old_string": "func CreateRefund(", "new_string": "func CreateRefund( // idempotent\n"}, "Applied edit to handlers/refunds.go", 30)
s.write()

# web: idle, finished
s = Session("web", "fix/flaky-checkout")
s.user("The checkout e2e test fails one run in five. Find out why", 3600)
s.tool("Bash", {"command": "npx playwright test checkout --repeat-each=10"}, "8 passed, 2 failed: timeout waiting for #pay-button", 3500)
s.tool("Edit", {"file_path": "/work/web/tests/checkout.spec.ts", "old_string": "await page.click('#pay-button')", "new_string": "await expect(page.locator('#pay-button')).toBeEnabled()\nawait page.click('#pay-button')"}, "Applied edit", 3400)
s.tool("Bash", {"command": "npx playwright test checkout --repeat-each=20"}, "20 passed", 3300)
s.say("The test clicked Pay before the price request finished. It now waits for the button to be enabled; 20 of 20 runs pass.", 3250)
s.write()

# infra: waiting on approval
s = Session("infra", "main")
s.user("Rotate the staging database password and update the secret", 400)
s.tool("Bash", {"command": "terraform plan -target=module.staging_db"}, "Plan: 0 to add, 1 to change, 0 to destroy.", 360)
s.tool("Bash", {"command": "terraform apply -target=module.staging_db -auto-approve"}, "", 300, answered=False)
s.write()

# docs: idle
s = Session("docs", "main")
s.user("Document the new idempotency header in the API reference", 7200)
s.tool("Write", {"file_path": "/work/docs/api/idempotency.md", "content": "# Idempotency keys\n\nSend `Idempotency-Key` on POST requests..."}, "File written", 7100)
s.say("Added api/idempotency.md and linked it from the charges page.", 7050)
s.write()
