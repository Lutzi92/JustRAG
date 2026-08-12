# Runbook — draining wedged `rag-quick` jobs

**Symptom:** the admin KB-Overview / System-Health panel shows a non-zero
`active` count on `rag-quick` that does not move for hours, while nothing
visibly progresses. The panel only renders counts (`waiting` / `active` /
`failed`), so the task type and age are not visible there — you have to look
in Redis.

## Why a task can stay active indefinitely

`jobs.TimeoutFor` caps every task type at ≤ 4 h, but that cap is only applied
if the **enqueue site** passes `asynq.Timeout(...)`. An enqueue without it
gives the handler a `context.Context` with **no deadline**, and the worker pod
keeps renewing the task's 30 s lease for as long as the process lives. A
handler wedged on a non-cancellable call (an HTTP request with no client
timeout, a subprocess, a blocked egress proxy) therefore stays `active`
forever — asynq never reclaims it.

Two sites had this gap and were fixed (`confluence/sync.go`,
`gitrepo/sync.go`, both `TypeFileProcessing` → `rag-quick`). Verify no new
ones appear:

```bash
cd go-backend
for loc in $(grep -rn --include=*.go "\.Enqueue(" internal cmd | grep -v _test | cut -d: -f1,2); do
  f=${loc%%:*}; l=${loc##*:}
  sed -n "${l},$((l+7))p" "$f" | grep -q "asynq.Timeout" || echo "NO TIMEOUT: $loc"
done
```

## Two traps

1. **`kubectl rollout restart` does not delete them.** On SIGTERM asynq waits
   `ShutdownTimeout` (30 s, `internal/worker/worker.go`) and then *requeues*
   in-flight tasks back to `pending` — without incrementing the retry count.
   They get picked straight back up and re-wedge, forever.
2. **`DeleteTask` refuses active tasks** (`cannot delete task in active
   state`). They must be moved out of `active` first.

The working sequence is therefore: **pause → restart (tasks fall into
`pending`) → delete those exact IDs → unpause.**

## Redis key layout (asynq v0.26)

Redis is external (`REDIS_HOST` in `k8s/configmap.yml`), not a pod, so
`kubectl port-forward` does not apply — run the probe from inside the cluster.

| Key | Contents |
|---|---|
| `asynq:{rag-quick}:active` | list of task IDs currently held by a worker |
| `asynq:{rag-quick}:pending` | list of task IDs waiting to be dequeued |
| `asynq:{rag-quick}:lease` | zset id → lease expiry (unix s); renewed every 30 s |
| `asynq:{rag-quick}:t:<id>` | hash with `state` + protobuf `msg` |
| `asynq:{rag-quick}:paused` | presence of this key makes workers skip the queue |

## Phase 0 — diagnose (read-only)

Start a throwaway `redis-cli` pod. The `--overrides` form pulls the password
from the existing secret, so it never lands in your shell history or in a
pod spec you typed:

```bash
kubectl -n justrag run redis-probe --rm -it --restart=Never \
  --image=redis:7-alpine \
  --overrides='{"spec":{"containers":[{"name":"redis-probe","image":"redis:7-alpine","stdin":true,"tty":true,"command":["sh"],"env":[{"name":"REDISCLI_AUTH","valueFrom":{"secretKeyRef":{"name":"worker-secrets","key":"REDIS_PASSWORD"}}}]}]}}' \
  -- sh
```

Inside the pod (adjust host/port/db to `REDIS_HOST` / `REDIS_PORT` /
`REDIS_DB` from `worker-config`):

```sh
alias q="redis-cli -h 10.60.3.25 -p 6379 -n 0"

q LLEN   'asynq:{rag-quick}:active'          # how many are stuck
q LRANGE 'asynq:{rag-quick}:active' 0 -1     # >>> RECORD THESE IDs <<<
q TIME                                        # server clock, for the next line
q ZRANGE 'asynq:{rag-quick}:lease' 0 -1 WITHSCORES
q --no-raw HGETALL 'asynq:{rag-quick}:t:<ID>'
```

`HGETALL` prints the protobuf `msg` blob; the task type
(`file-processing`, …) and the JSON payload (file ID, KB ID, filename) are
plain text inside it — enough to identify the culprit document.

**Reading the lease score:**

- score ≈ `now + ≤30 s` → a **live pod is still holding it**; the handler is
  wedged. This is the no-timeout case. Proceed to Phase 1.
- score **in the past** → the task is **orphaned**. A running worker's
  recoverer requeues orphans within ~1 min, so if this persists it means
  nothing is consuming `rag-quick` — check `kubectl -n justrag get pods -l
  component=worker-quick` and the pod logs instead; do **not** run Phase 1.

Correlate with the pods before touching anything:

```bash
kubectl -n justrag get pods -l component=worker-quick
kubectl -n justrag logs -l component=worker-quick --since=24h --tail=200 | grep -i "task.start\|task.end\|error"
```

## Phase 1 — drain

Pausing stops **all** `rag-quick` work, so keep the window short (a rollout is
typically 30–120 s; `terminationGracePeriodSeconds` is 120).

```sh
# 1. pause — workers stop dequeuing, in-flight tasks are unaffected
q SETNX 'asynq:{rag-quick}:paused' 1
q EXISTS 'asynq:{rag-quick}:paused'          # must be 1 before continuing
```

```bash
# 2. restart the pods; asynq requeues the wedged tasks into `pending`
kubectl -n justrag rollout restart deploy/worker-quick
kubectl -n justrag rollout status  deploy/worker-quick --timeout=5m
```

Do **not** scale to 0 instead: the HPA (`minReplicas: 4`) will scale it back
up and fight you.

```sh
# 3. confirm active is empty and the IDs moved to pending
q LLEN   'asynq:{rag-quick}:active'          # expect 0
q LRANGE 'asynq:{rag-quick}:pending' 0 -1

# 4. delete ONLY the IDs recorded in Phase 0 — pending may also hold
#    legitimate work queued while the queue was paused
q LREM 'asynq:{rag-quick}:pending' 0 <ID>    # expect 1
q DEL  'asynq:{rag-quick}:t:<ID>'            # expect 1

# 5. unpause
q DEL 'asynq:{rag-quick}:paused'
q LLEN 'asynq:{rag-quick}:pending'
```

Steps 4–5 are exactly what asynq's own `deleteTaskCmd` does for a task in
`pending` state (the `unique_key` branch does not apply — no enqueue site
uses `asynq.Unique`).

Exit the pod (`exit`) — `--rm` deletes it.

## Phase 2 — database side

Usually already handled: `checkStuckFiles` on **worker-batch** (the only
deployment with `WORKER_MAINTENANCE=true`) flips `files` rows stuck in
`processing` to `status='error'`, `error_stage='timeout'` after
`StuckFileTimeout` (default 10 min). Confirm nothing is left behind:

```sql
SELECT id, kb_id, original_name, status, error_stage, created_at
FROM files WHERE status = 'processing' AND created_at < now() - interval '1 hour';
```

If rows persist, verify worker-batch is running and that its maintenance loop
logged `marked stuck files as error`. Re-ingest the affected files once the
underlying wedge is understood — retry the file from the KB view rather than
re-uploading, so the file UUID stays stable for the golden sets.

## Phase 3 — root cause

The timeout gap explains why the task never *died*; it does not explain why
the handler *hung*. Check, in order:

1. Egress — worker pods need `HTTP(S)_PROXY` for outbound calls; a blocked
   proxy hangs any HTTP call whose client has no timeout.
2. Docling sidecar reachability, if `docling_enabled`.
3. The model backend — an ingest burst can saturate vLLM/embeddings; cap it
   with `AI_MAX_CONCURRENT_REQUESTS`.
4. `kubectl -n justrag logs <pod> --previous` around the task's start.
