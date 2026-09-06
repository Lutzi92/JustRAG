#!/usr/bin/env bash
# Times the keyword arm's SQL — BOTH scoring modes (ts_rank and bm25) — with
# EXPLAIN (ANALYZE, BUFFERS), for three query shapes against one KB.
#
# Spec: .superpowers/sdd/2026-09-06-rag-sota-wave3/task-7-brief.md, step 2.
#
# The statements are NOT retyped here: `cmd/eval --print-keyword-sql <q>
# --kb-id <id>` renders whatever the real builders produce for that KB
# (chunk table, text-search config, simple arm, tiered boost, k1/b, dim-keyed
# stats tables) and hands back an `executable_sql` with the placeholders
# inlined. Run it against the ~100k-chunk synthetic KB
# (eval/fixtures/bm25-scale/seed-scale-kb.sh) and against the ~1.8k-chunk
# production fixture KB to get the growth factor.
#
# Usage:
#   eval/fixtures/bm25-scale/time-keyword-sql.sh --kb-id <uuid> --out <dir> [--runs 3] [--label scale100k]
#
# Env: same DB_* / VECTOR_DB_* / JWT_SECRET contract as
# eval/fixtures/bm25-scale/seed-scale-kb.sh (the renderer connects to both
# DBs); PSQL_VECTOR as there.
#
# Output (per query shape, per mode, per run):
#   <out>/<label>-<shape>-<mode>-run<N>.txt   full EXPLAIN (ANALYZE, BUFFERS) plan
#   <out>/<label>-sql.json                    the rendered statements
#   <out>/<label>-summary.tsv                 label, shape, mode, run, planning ms,
#                                             execution ms, candidate rows, gin used
#
# Requires: bash, go, python3, docker (or PSQL_VECTOR override).
set -euo pipefail

PSQL_VECTOR="${PSQL_VECTOR:-docker exec -i justrag-vectordb-1 psql -U postgres -d rag_vector_db}"
SCRIPT_DIR="$(dirname "$0")"
REPO_ROOT="$SCRIPT_DIR/../../.."

KB_ID=""
OUT_DIR=""
RUNS=3
LABEL=""
LIMIT=50
while [ $# -gt 0 ]; do
  case "$1" in
    --kb-id) KB_ID="$2"; shift 2 ;;
    --out) OUT_DIR="$2"; shift 2 ;;
    --runs) RUNS="$2"; shift 2 ;;
    --label) LABEL="$2"; shift 2 ;;
    --limit) LIMIT="$2"; shift 2 ;;
    -h|--help) sed -n '2,30p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
[ -n "$KB_ID" ] || { echo "--kb-id is required" >&2; exit 2; }
[ -n "$OUT_DIR" ] || { echo "--out is required" >&2; exit 2; }
LABEL="${LABEL:-kb}"
mkdir -p "$OUT_DIR"

# Three query shapes, chosen to bracket the keyword arm's cost profile:
#   rare   — a low-document-frequency term: the GIN index should prune hard.
#   common — a high-document-frequency German term: many candidates, so the
#            per-candidate scoring work (the bm25 tf/sc CTEs) dominates.
#   phrase — a quoted phrase: phraseto_tsquery, positional matching.
SHAPES="rare common phrase"
Q_rare='Stud.IP-Update Projektsteckbrief'
Q_common='die Verwaltung der Daten und Systeme'
Q_phrase='"Stud.IP" Zugriffsrechte'

# The renderer opens its own DB connections (the psql steps go through docker
# exec). Host/port/user/db default to the compose values; the secrets have no
# default and must come from the caller's environment.
missing=""
[ -n "${DB_PASSWORD:-}" ] || missing="$missing DB_PASSWORD"
[ -n "${JWT_SECRET:-}" ] || missing="$missing JWT_SECRET"
if [ -n "$missing" ]; then
  echo "--print-keyword-sql needs these env vars set at invocation:$missing" >&2
  exit 2
fi
export DB_HOST="${DB_HOST:-localhost}" DB_PORT="${DB_PORT:-5432}" DB_USER="${DB_USER:-postgres}" DB_NAME="${DB_NAME:-rag_db}"
export VECTOR_DB_HOST="${VECTOR_DB_HOST:-localhost}" VECTOR_DB_PORT="${VECTOR_DB_PORT:-5433}" VECTOR_DB_USER="${VECTOR_DB_USER:-postgres}" VECTOR_DB_NAME="${VECTOR_DB_NAME:-rag_vector_db}"
export VECTOR_DB_PASSWORD="${VECTOR_DB_PASSWORD:-$DB_PASSWORD}"

BIN_DIR="$(mktemp -d)"
trap 'rm -rf "$BIN_DIR"' EXIT
( cd "$REPO_ROOT/go-backend" && go build -o "$BIN_DIR/eval-print" ./cmd/eval )

SUMMARY="$OUT_DIR/$LABEL-summary.tsv"
printf 'label\tshape\tmode\trun\tplanning_ms\texecution_ms\tcandidate_rows\tgin_used\n' > "$SUMMARY"

for shape in $SHAPES; do
  qvar="Q_$shape"
  query="${!qvar}"
  sqljson="$OUT_DIR/$LABEL-$shape-sql.json"
  "$BIN_DIR/eval-print" --print-keyword-sql "$query" --kb-id "$KB_ID" --top-k "$LIMIT" > "$sqljson" 2>"$sqljson.log"

  for mode in ts_rank bm25; do
    stmt="$OUT_DIR/$LABEL-$shape-$mode.sql"
    python3 - "$sqljson" "$mode" "$stmt" <<'PY'
import json, sys
doc = json.load(open(sys.argv[1]))
mode, out = sys.argv[2], sys.argv[3]
for m in doc["modes"]:
    if m["mode"] == mode:
        if not m["ok"]:
            raise SystemExit(f"builder reported ok=false for mode {mode}")
        open(out, "w").write("EXPLAIN (ANALYZE, BUFFERS)\n" + m["executable_sql"] + ";\n")
        break
else:
    raise SystemExit(f"mode {mode} missing from the rendered output")
PY

    n=1
    while [ "$n" -le "$RUNS" ]; do
      plan="$OUT_DIR/$LABEL-$shape-$mode-run$n.txt"
      $PSQL_VECTOR -v ON_ERROR_STOP=1 -q -X -f - < "$stmt" > "$plan"
      python3 - "$plan" "$LABEL" "$shape" "$mode" "$n" >> "$SUMMARY" <<'PY'
import re, sys
plan = open(sys.argv[1]).read()
label, shape, mode, run = sys.argv[2:6]

def num(pattern):
    m = re.search(pattern, plan)
    return m.group(1) if m else ""

planning = num(r"Planning Time: ([\d.]+) ms")
execution = num(r"Execution Time: ([\d.]+) ms")

# Rows entering the scoring step: for bm25 that is the `cand` CTE; for
# ts_rank there is no CTE, so it is the rows the WHERE clause admitted,
# i.e. the input to the top-level sort/limit.
cand = ""
m = re.search(r"CTE cand.*?actual time=[\d.]+\.\.[\d.]+ rows=(\d+)", plan, re.S)
if m:
    cand = m.group(1)
else:
    m = re.search(r"Bitmap Heap Scan on \"?document_chunks[_\d]*\"?.*?rows=(\d+)", plan, re.S)
    if m:
        cand = m.group(1)
gin = "yes" if "Bitmap Index Scan on document_chunks" in plan or "vector_index_idx" in plan else "no"
print("\t".join([label, shape, mode, run, planning, execution, cand, gin]))
PY
      n=$((n + 1))
    done
  done
done

echo "--- $SUMMARY" >&2
cat "$SUMMARY"
