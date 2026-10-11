#!/usr/bin/env bash
# OCC spike: create Configuration + Agent, grant Secret, deploy, time it.
# Prereqs: port-forward OCC API on 127.0.0.1:3300, service key at $SP/service-key.json,
# OCC Secret "spike-openai" already created (by the user) in the default Namespace.
set -euo pipefail
SP="$(cd "$(dirname "$0")" && pwd)"
OCC="$SP/openclaw-enterprise/bin/occ"
export OCC_URL=http://127.0.0.1:3300 OCC_SERVICE_KEY_FILE="$SP/service-key.json"
MODEL="${SPIKE_MODEL:-gpt-6-astra}"
NAME="${1:-spike-agent}"
now() { python3 -c 'import time; print(f"{time.time():.3f}")'; }
j() { python3 -c "import json,sys; print(json.load(sys.stdin)$1)"; }
log() { printf '%s %s\n' "$(date +%H:%M:%S)" "$*" | tee -a "$SP/measure.log"; }

NAMESPACE_ID="$($OCC namespace list -o json | python3 -c 'import json,sys; d=json.load(sys.stdin); d=d.get("items",d) if isinstance(d,dict) else d; print([n["id"] for n in d if n["name"]=="default"][0])')"
export OCC_NAMESPACE="$NAMESPACE_ID"
log "namespace $NAMESPACE_ID"

SECRET_ID="$($OCC secret list -o json | python3 -c 'import json,sys; d=json.load(sys.stdin); d=d.get("items",d) if isinstance(d,dict) else d; print([s["id"] for s in d if s["name"]=="spike-openai"][0])')"
log "secret $SECRET_ID"

cat > "$SP/configuration.json" <<JSON
{"kind":"agent","values":{
 "gateway":{"mode":"local","bind":"lan","controlUi":{"enabled":false},
  "auth":{"password":{"source":"env","provider":"default","id":"OPENCLAW_GATEWAY_PASSWORD"}},
  "http":{"endpoints":{"chatCompletions":{"enabled":true}}}},
 "agents":{"defaults":{"model":"openai/$MODEL","skipBootstrap":true,
  "models":{"openai/$MODEL":{"agentRuntime":{"id":"openclaw"}}}}},
 "tools":{"deny":["*"]},
 "models":{"providers":{"openai":{"baseUrl":"https://api.openai.com/v1","api":"openai-responses",
  "models":[{"id":"$MODEL","name":"$MODEL"}]}}}}}
JSON
CONFIGURATION_ID="$($OCC configuration create --file "$SP/configuration.json" -o json | j '["id"]')"
log "configuration $CONFIGURATION_ID"

printf '{"name":"%s","configurationId":"%s","executionMode":"embedded","harnessAuth":{"method":"api_key","source":{"kind":"secret","namespaceId":"%s","id":"%s"}}}\n' \
  "$NAME" "$CONFIGURATION_ID" "$NAMESPACE_ID" "$SECRET_ID" > "$SP/agent.json"
AGENT_JSON="$($OCC agent create --file "$SP/agent.json" -o json)"
AGENT_ID="$(printf '%s' "$AGENT_JSON" | j '["id"]')"
SP_ID="$(printf '%s' "$AGENT_JSON" | j '["servicePrincipalId"]')"
log "agent $AGENT_ID principal $SP_ID"

ROLE_ID="$($OCC iam role list -o json | python3 -c 'import json,sys; d=json.load(sys.stdin); d=d.get("items",d) if isinstance(d,dict) else d; r=[x["id"] for x in d if x["name"]=="Use a model Secret"]; print(r[0] if r else "")')"
if [ -z "$ROLE_ID" ]; then
  echo '{"name":"Use a model Secret","permissions":[{"action":"operate","resourceKind":"secret"}]}' > "$SP/role.json"
  ROLE_ID="$($OCC iam role create --file "$SP/role.json" -o json | j '["id"]')"
fi
printf '{"subjectKind":"identity","subjectId":"%s","roleId":"%s","resourceKind":"secret","resourceId":"%s"}\n' "$SP_ID" "$ROLE_ID" "$SECRET_ID" > "$SP/binding.json"
$OCC iam access-binding create --file "$SP/binding.json" -o json >/dev/null
log "secret grant done"

KEY="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["data"]["key"])' "$OCC_SERVICE_KEY_FILE")"
t0=$(now)
curl -fsS -X POST -H "x-api-key: $KEY" -H 'content-type: application/json' -d '{}' \
  "$OCC_URL/namespaces/$NAMESPACE_ID/agents/$AGENT_ID/runtime-credentials" >/dev/null
t1=$(now); log "runtime-credentials $(python3 -c "print(round($t1-$t0,2))")s"

t_deploy=$(now)
REV_ID="$($OCC agent deploy "$AGENT_ID" -o json | j '["id"]')"
t_accepted=$(now); log "deploy accepted rev=$REV_ID in $(python3 -c "print(round($t_accepted-$t_deploy,2))")s"
last=""
while :; do
  st="$($OCC agent deployment-status "$AGENT_ID" -o json 2>/dev/null | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("status",""), d.get("error",{}) and d["error"].get("code",""))' || true)"
  [ "$st" != "$last" ] && log "status: $st (+$(python3 -c "print(round($(now)-$t_deploy,1))")s)" && last="$st"
  case "$st" in succeeded*|failed*) break;; esac
  sleep 1
done
t_done=$(now); log "DEPLOY total $(python3 -c "print(round($t_done-$t_deploy,1))")s"
printf 'NAMESPACE_ID=%s\nAGENT_ID=%s\nREV_ID=%s\n' "$NAMESPACE_ID" "$AGENT_ID" "$REV_ID" > "$SP/agent.env"
