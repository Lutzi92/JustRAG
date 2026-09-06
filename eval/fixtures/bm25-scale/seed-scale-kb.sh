#!/usr/bin/env bash
# Seeds a THROWAWAY, obviously synthetic knowledge base with ~100k chunks so
# the keyword arm's SQL can be cost-checked an order of magnitude above the
# production eval fixture (PPM-Eval, ~1.8k chunks).
#
# Spec: .superpowers/sdd/2026-09-06-rag-sota-wave3/task-7-brief.md, step 2.
# Ruling W3-R14: the check is synthetic — the corpus is the PPM-Eval KB's
# chunk text duplicated N times, each copy carrying a numeric salt token, so
# per-term document frequencies scale with the corpus (realistic IDF) instead
# of every lexeme being present in 100% of documents.
#
# SQL-only: no HTTP, no ingestion pipeline, no admin credentials, no LLM call.
# The rows are keyword-only — `embedding` stays NULL (it is nullable), which
# is all the keyword arm reads. The KB is therefore NOT usable for a real
# search; it exists to be EXPLAINed and dropped.
#
# Usage:
#   eval/fixtures/bm25-scale/seed-scale-kb.sh              # seed (default 55 copies)
#   eval/fixtures/bm25-scale/seed-scale-kb.sh --copies 20
#   eval/fixtures/bm25-scale/seed-scale-kb.sh --stats-only # only refresh BM25 stats
#   eval/fixtures/bm25-scale/seed-scale-kb.sh --drop       # remove everything it created
#
# Env:
#   SCALE_KB_ID       Throwaway KB id. Default: 5ca1e000-0000-4000-8000-000000000001
#                     (fixed so the fixture scripts, the golden stub and --drop
#                     all agree without passing ids around).
#   SOURCE_KB_ID      KB whose chunk text is duplicated.
#                     Default: 83262307-3a1b-49bc-bd08-3b925a868a92 (PPM-Eval).
#   SOURCE_DIM        Dim table holding SOURCE_KB_ID's chunks. Default: 4096.
#   TARGET_DIM        Dim table the copies land in. Default: 768 (empty on the
#                     dev stack, so the synthetic corpus never shares a table
#                     with real data).
#   PG_CONFIG         Text-search config for the copied tsvectors. Default:
#                     german. MUST match what the query path resolves for the
#                     scale KB's language (PgTextSearchConfig('de') = german).
#   PSQL_MAIN         Command (split on whitespace) that runs SQL against the
#                     MAIN db on stdin.
#                     Default: "docker exec -i justrag-db-1 psql -U postgres -d rag_db"
#   PSQL_VECTOR       Same for the VECTOR db.
#                     Default: "docker exec -i justrag-vectordb-1 psql -U postgres -d rag_vector_db"
#   DB_PASSWORD, VECTOR_DB_PASSWORD, JWT_SECRET
#                     Required for the BM25 stats-refresh step only (the Go
#                     binary opens its own connections; the psql steps go
#                     through docker exec). No defaults — pass them at
#                     invocation, never store them in a file. The remaining
#                     DB_* / VECTOR_DB_* vars default to the compose values.
#
# The BM25 stats refresh runs the REAL refresher (vector.BM25StatsRefresher via
# `cmd/eval --refresh-bm25-stats`, fed the one-question golden stub next to this
# script) rather than a hand-written transcription of its SQL, so the stats the
# cost check measures against cannot drift from the ones production writes. The
# stub question is never meant to score — the refresh happens before the run,
# and the run's own exit code is ignored.
#
# Requires: bash, docker (or a PSQL_* override), go (for the stats refresh).
set -euo pipefail

SCALE_KB_ID="${SCALE_KB_ID:-5ca1e000-0000-4000-8000-000000000001}"
SOURCE_KB_ID="${SOURCE_KB_ID:-83262307-3a1b-49bc-bd08-3b925a868a92}"
SOURCE_DIM="${SOURCE_DIM:-4096}"
TARGET_DIM="${TARGET_DIM:-768}"
PG_CONFIG="${PG_CONFIG:-german}"
PSQL_MAIN="${PSQL_MAIN:-docker exec -i justrag-db-1 psql -U postgres -d rag_db}"
PSQL_VECTOR="${PSQL_VECTOR:-docker exec -i justrag-vectordb-1 psql -U postgres -d rag_vector_db}"

KB_NAME="ZZZ-SYNTHETIC bm25 scale check (throwaway)"
SCRIPT_DIR="$(dirname "$0")"
REPO_ROOT="$SCRIPT_DIR/../../.."
GOLDEN_STUB="$SCRIPT_DIR/scale-kb.golden.jsonl"

COPIES=55
MODE=seed
while [ $# -gt 0 ]; do
  case "$1" in
    --copies) COPIES="$2"; shift 2 ;;
    --drop) MODE=drop; shift ;;
    --stats-only) MODE=stats; shift ;;
    -h|--help) sed -n '2,50p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

SRC_TABLE="document_chunks_${SOURCE_DIM}"
DST_TABLE="document_chunks_${TARGET_DIM}"
KB_STATS="bm25_kb_stats_${TARGET_DIM}"
TERM_STATS="bm25_term_stats_${TARGET_DIM}"

main_sql() { $PSQL_MAIN -v ON_ERROR_STOP=1 "$@"; }
vector_sql() { $PSQL_VECTOR -v ON_ERROR_STOP=1 "$@"; }

refresh_stats() {
  # Build a throwaway binary (never overwrite the tracked cmd/eval/eval) and
  # let it run the production refresher for every dim table that has rows for
  # the scale KB.
  #
  # Unlike the psql steps (docker exec, trust auth) the Go binary opens its own
  # connections, so it needs credentials. Host/port/user/db fall back to the
  # compose defaults; the three secrets have NO default and must come from the
  # caller's environment — same convention as eval/fixtures/seed-cert.sh.
  local missing=""
  [ -n "${DB_PASSWORD:-}" ] || missing="$missing DB_PASSWORD"
  [ -n "${VECTOR_DB_PASSWORD:-${DB_PASSWORD:-}}" ] || missing="$missing VECTOR_DB_PASSWORD"
  [ -n "${JWT_SECRET:-}" ] || missing="$missing JWT_SECRET"
  if [ -n "$missing" ]; then
    echo "BM25 stats refresh needs these env vars set at invocation:$missing" >&2
    echo "(the chunks are seeded; re-run with --stats-only once they are set)" >&2
    exit 2
  fi
  export DB_HOST="${DB_HOST:-localhost}" DB_PORT="${DB_PORT:-5432}" DB_USER="${DB_USER:-postgres}" DB_NAME="${DB_NAME:-rag_db}"
  export VECTOR_DB_HOST="${VECTOR_DB_HOST:-localhost}" VECTOR_DB_PORT="${VECTOR_DB_PORT:-5433}" VECTOR_DB_USER="${VECTOR_DB_USER:-postgres}" VECTOR_DB_NAME="${VECTOR_DB_NAME:-rag_vector_db}"
  export VECTOR_DB_PASSWORD="${VECTOR_DB_PASSWORD:-$DB_PASSWORD}"

  local tmpdir bin
  tmpdir="$(mktemp -d)"
  bin="$tmpdir/eval-scale-refresh"
  ( cd "$REPO_ROOT/go-backend" && go build -o "$bin" ./cmd/eval )
  echo "refreshing BM25 stats for $SCALE_KB_ID (real refresher; the stub question's own result is ignored)" >&2
  "$bin" --golden "$GOLDEN_STUB" --refresh-bm25-stats --top-k 1 \
    --output "$tmpdir/scale-refresh-report.json" >/dev/null 2>&1 || true
  rm -rf "$tmpdir"
  vector_sql -c "SELECT arm, doc_count, round(avg_len::numeric, 2) AS avg_len FROM \"$KB_STATS\" WHERE kb_id = '$SCALE_KB_ID';"
  vector_sql -c "SELECT arm, count(*) AS terms FROM \"$TERM_STATS\" WHERE kb_id = '$SCALE_KB_ID' GROUP BY arm ORDER BY arm;"
}

case "$MODE" in
drop)
  echo "dropping synthetic scale KB $SCALE_KB_ID" >&2
  vector_sql <<SQL
DELETE FROM "$DST_TABLE" WHERE kb_id = '$SCALE_KB_ID';
DELETE FROM "$KB_STATS" WHERE kb_id = '$SCALE_KB_ID';
DELETE FROM "$TERM_STATS" WHERE kb_id = '$SCALE_KB_ID';
SQL
  # The main-DB row is the only thing the seed writes there; every table that
  # references knowledge_bases cascades, and the seed creates none of them.
  main_sql -c "DELETE FROM knowledge_bases WHERE id = '$SCALE_KB_ID';"
  echo "-- leftover rows (all counts must be 0):" >&2
  vector_sql -c "SELECT (SELECT count(*) FROM \"$DST_TABLE\" WHERE kb_id = '$SCALE_KB_ID') AS chunks, (SELECT count(*) FROM \"$KB_STATS\" WHERE kb_id = '$SCALE_KB_ID') AS kb_stats, (SELECT count(*) FROM \"$TERM_STATS\" WHERE kb_id = '$SCALE_KB_ID') AS term_stats;"
  main_sql -c "SELECT count(*) AS kb_rows FROM knowledge_bases WHERE id = '$SCALE_KB_ID';"
  exit 0
  ;;
stats)
  refresh_stats
  exit 0
  ;;
esac

echo "seeding synthetic scale KB $SCALE_KB_ID: $COPIES x $SRC_TABLE($SOURCE_KB_ID) -> $DST_TABLE" >&2

# Main DB: the KB row. user_id NULL (nullable), visibility private, language de
# so PgTextSearchConfig resolves to $PG_CONFIG at query time.
main_sql <<SQL
INSERT INTO knowledge_bases (id, name, description, language, visibility, is_published)
VALUES ('$SCALE_KB_ID', '$KB_NAME',
        'Throwaway synthetic corpus for the BM25 keyword-arm scale check (Wave-3 Task 7). Safe to delete.',
        'de', 'private', false)
ON CONFLICT (id) DO NOTHING;
SQL

# Vector DB: start from a clean slate so re-running is idempotent.
vector_sql -c "DELETE FROM \"$DST_TABLE\" WHERE kb_id = '$SCALE_KB_ID';"

# One INSERT per copy: ~1.8k rows each, so the tsvector computation and the
# two GIN index updates stay in bounded batches instead of one 100k-row
# statement.
#
# vector_index / vector_index_simple are plain NULLABLE tsvector columns (NOT
# generated) — ingest fills them inside its own INSERT with exactly this
# expression (internal/vector/chunks.go), and the salt makes the text differ
# per copy, so the tsvectors are recomputed rather than copied.
i=1
while [ "$i" -le "$COPIES" ]; do
  vector_sql -q <<SQL
INSERT INTO "$DST_TABLE"
  (id, kb_id, file_id, content, contextual_prefix, vector_index, vector_index_simple, metadata, created_at, node_kind, tree_level)
SELECT gen_random_uuid(),
       '$SCALE_KB_ID'::uuid,
       md5(src.file_id::text || ':$i')::uuid,
       src.content || ' salt$i',
       src.contextual_prefix,
       to_tsvector('$PG_CONFIG'::regconfig, COALESCE(src.contextual_prefix, '') || ' ' || src.content || ' salt$i'),
       to_tsvector('simple'::regconfig,     COALESCE(src.contextual_prefix, '') || ' ' || src.content || ' salt$i'),
       src.metadata,
       now(),
       src.node_kind,
       src.tree_level
FROM "$SRC_TABLE" src
WHERE src.kb_id = '$SOURCE_KB_ID'::uuid;
SQL
  if [ $((i % 10)) -eq 0 ] || [ "$i" -eq "$COPIES" ]; then
    echo "  copy $i/$COPIES done" >&2
  fi
  i=$((i + 1))
done

# EXPLAIN must plan against real statistics, not the estimates left over from
# an empty table.
vector_sql -c "ANALYZE \"$DST_TABLE\";"
vector_sql -c "SELECT count(*) AS chunks, count(DISTINCT file_id) AS files, round(avg(length(vector_index))::numeric, 1) AS avg_lexemes FROM \"$DST_TABLE\" WHERE kb_id = '$SCALE_KB_ID';"

refresh_stats

echo "$SCALE_KB_ID"
