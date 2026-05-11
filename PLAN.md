# m3tal-api Plan (Control Interface)

## Purpose
Expose system control + state.

NOT the brain.

---

## Goals
- Provide API for dashboard + CLI
- No decision-making logic
- No direct Docker control

---

## Endpoints

GET  /status
GET  /metrics
POST /action/restart/{container}
POST /action/scan/{service}

---

## Rules

### 1. No logic

Do NOT:
- run agents
- make decisions
- duplicate core logic

---

### 2. No direct Docker access

Bad:

docker restart radarr

Good:

exec.Command("../m3tal-core/run.sh")

---

### 3. Read from core state

Read:

../m3tal-core/state/system.json

---

### 4. Trigger actions via core

- Call scripts
- Call CLI
- Never act directly

---

## Done When

- API only reads + triggers
- No system logic exists here
- Works even if core changes internally
