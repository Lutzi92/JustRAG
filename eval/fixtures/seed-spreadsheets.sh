#!/usr/bin/env bash
# Seeds a KB named "Spreadsheet Fixtures" with every internal/sheetsource
# testdata fixture, waits for ingestion to finish, and writes a
# KB-id-resolved copy of the Phase 4 spreadsheet golden set.
#
# Spec: .superpowers/sdd/2026-09-05-spreadsheet-ingest-phase4/task-9-brief.md
# See eval/golden/README.md "Spreadsheet set" section for the full
# seed -> run -> acceptance procedure.
#
# Usage:
#   JUSTRAG_ADMIN_PASSWORD=... ./eval/fixtures/seed-spreadsheets.sh
#
# Env:
#   JUSTRAG_URL              Base URL of a running JustRAG instance.
#                            Default: http://localhost:3000
#   JUSTRAG_ADMIN_USER       Admin username. Default: admin
#                            (go-backend/internal/migrate/seed.go always
#                            seeds the superadmin as "admin"; the password
#                            comes from the ADMIN_PASSWORD env var the
#                            instance was started with.)
#   JUSTRAG_ADMIN_PASSWORD   Admin password. Required, no default — never
#                            invent one.
#
# Requires: bash, curl, jq. Does NOT run npm install or touch node_modules.
#
# Output:
#   - prints the seeded KB id to stdout
#   - writes eval/golden/spreadsheets-de.local.jsonl (gitignored): the
#     committed spreadsheets-de.jsonl with kb_id rewritten from the
#     REPLACE_WITH_FIXTURE_KB_ID placeholder to the real KB id.
set -euo pipefail

JUSTRAG_URL="${JUSTRAG_URL:-http://localhost:3000}"
JUSTRAG_ADMIN_USER="${JUSTRAG_ADMIN_USER:-admin}"
JUSTRAG_ADMIN_PASSWORD="${JUSTRAG_ADMIN_PASSWORD:-}"

KB_NAME="Spreadsheet Fixtures"

# Resolve paths relative to this script's location so it can be run from
# any working directory.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
TESTDATA_DIR="${REPO_ROOT}/go-backend/internal/sheetsource/testdata"
GOLDEN_SRC="${REPO_ROOT}/eval/golden/spreadsheets-de.jsonl"
GOLDEN_OUT="${REPO_ROOT}/eval/golden/spreadsheets-de.local.jsonl"

log() { printf '%s\n' "$*" >&2; }
die() { log "error: $*"; exit 1; }

command -v curl >/dev/null 2>&1 || die "curl is required"
command -v jq   >/dev/null 2>&1 || die "jq is required"

if [ -z "${JUSTRAG_ADMIN_PASSWORD}" ]; then
	die "JUSTRAG_ADMIN_PASSWORD is required (no default — never invent one)"
fi
if [ ! -d "${TESTDATA_DIR}" ]; then
	die "testdata dir not found: ${TESTDATA_DIR}"
fi
if [ ! -f "${GOLDEN_SRC}" ]; then
	die "golden set not found: ${GOLDEN_SRC}"
fi

log "Seeding against ${JUSTRAG_URL} ..."

# ---------------------------------------------------------------------------
# 1. Login
# ---------------------------------------------------------------------------
# The login body is piped into curl via stdin (--data-binary @-) rather than
# passed as a -d argument: a -d value is visible in `ps` for as long as curl
# is running (the network round trip), which would put the admin password
# in the process table. jq still builds the JSON via --arg (its own argv
# briefly carries the password too, but jq -n exits almost immediately,
# unlike curl).
login_response="$(jq -n --arg u "${JUSTRAG_ADMIN_USER}" --arg p "${JUSTRAG_ADMIN_PASSWORD}" \
		'{username: $u, password: $p}' \
	| curl -fsS -X POST "${JUSTRAG_URL}/api/auth/login" \
		-H 'Content-Type: application/json' \
		--data-binary @-)" \
	|| die "login request failed"

TOKEN="$(printf '%s' "${login_response}" | jq -r '.token // empty')"
if [ -z "${TOKEN}" ]; then
	die "login did not return a token: ${login_response}"
fi
log "Logged in as ${JUSTRAG_ADMIN_USER}."

auth_curl() {
	curl -fsS -H "Authorization: Bearer ${TOKEN}" "$@"
}

# ---------------------------------------------------------------------------
# 2. Create or reuse the KB
# ---------------------------------------------------------------------------
existing_kb_id="$(auth_curl "${JUSTRAG_URL}/api/kb?limit=100" \
	| jq -r --arg name "${KB_NAME}" '[.[] | select(.name == $name)][0].id // empty')"

if [ -n "${existing_kb_id}" ]; then
	KB_ID="${existing_kb_id}"
	log "Reusing existing KB \"${KB_NAME}\" (${KB_ID})."
else
	create_response="$(auth_curl -X POST "${JUSTRAG_URL}/api/kb" \
		-H 'Content-Type: application/json' \
		-d "$(jq -n --arg name "${KB_NAME}" \
			'{name: $name, description: "Phase 4 spreadsheet-ingest release acceptance fixtures (internal/sheetsource/testdata). Seeded by eval/fixtures/seed-spreadsheets.sh."}')")" \
		|| die "KB create request failed"
	KB_ID="$(printf '%s' "${create_response}" | jq -r '.id // empty')"
	if [ -z "${KB_ID}" ]; then
		die "KB create did not return an id: ${create_response}"
	fi
	log "Created KB \"${KB_NAME}\" (${KB_ID})."
fi

# ---------------------------------------------------------------------------
# 3. Upload every fixture (xlsx/xls/ods/csv), skipping any "large_synthetic"
#    fixture (the 125MB manual-test ledger — never checked in here, but the
#    exclusion is kept in case one shows up locally) and the non-spreadsheet
#    helper files (README.md, regen.sh, gen/).
# ---------------------------------------------------------------------------
uploaded=0
skipped=0
shopt -s nullglob
for f in "${TESTDATA_DIR}"/*.xlsx "${TESTDATA_DIR}"/*.xls "${TESTDATA_DIR}"/*.ods "${TESTDATA_DIR}"/*.csv; do
	base="$(basename "${f}")"
	case "${base}" in
		*large_synthetic*)
			log "Skipping ${base} (large_synthetic excluded)."
			skipped=$((skipped + 1))
			continue
			;;
	esac

	# Skip a fixture that's already present in the KB by name, so re-running
	# the script against an already-seeded KB is a cheap no-op per file.
	already="$(auth_curl "${JUSTRAG_URL}/api/kb/${KB_ID}/files?limit=1000" \
		| jq -r --arg name "${base}" '[.[] | select(.name == $name)][0].id // empty')"
	if [ -n "${already}" ]; then
		log "Skipping ${base} (already present in KB)."
		skipped=$((skipped + 1))
		continue
	fi

	log "Uploading ${base} ..."
	auth_curl -X POST "${JUSTRAG_URL}/api/kb/${KB_ID}/files" \
		-F "file=@${f};filename=${base}" >/dev/null \
		|| die "upload failed: ${base}"
	uploaded=$((uploaded + 1))
done
shopt -u nullglob

log "Uploaded ${uploaded} fixture(s), skipped ${skipped}."

# ---------------------------------------------------------------------------
# 4. Poll until every file in the KB is out of pending/processing.
# ---------------------------------------------------------------------------
log "Waiting for ingestion to finish (polling every 5s) ..."
while true; do
	files_json="$(auth_curl "${JUSTRAG_URL}/api/kb/${KB_ID}/files?limit=1000")"

	pending_count="$(printf '%s' "${files_json}" \
		| jq '[.[] | select(.status == "pending" or .status == "processing")] | length')"

	if [ "${pending_count}" -eq 0 ]; then
		break
	fi
	log "  ${pending_count} file(s) still pending/processing ..."
	sleep 5
done

error_rows="$(printf '%s' "${files_json}" | jq -c '[.[] | select(.status == "error")]')"
error_count="$(printf '%s' "${error_rows}" | jq 'length')"
if [ "${error_count}" -gt 0 ]; then
	log "The following file(s) failed ingestion:"
	printf '%s' "${error_rows}" | jq -r '.[] | "  - \(.name): \(.errorMessage // "no error message")"' >&2
	die "${error_count} file(s) errored during ingestion"
fi

completed_count="$(printf '%s' "${files_json}" | jq '[.[] | select(.status == "completed")] | length')"
log "All files finished ingesting (${completed_count} completed, 0 errored)."

# ---------------------------------------------------------------------------
# 5. Write the KB-id-resolved golden set copy.
# ---------------------------------------------------------------------------
jq -c --arg kb "${KB_ID}" '.kb_id = $kb' "${GOLDEN_SRC}" > "${GOLDEN_OUT}"
log "Wrote $(wc -l < "${GOLDEN_OUT}" | tr -d ' ') question(s) to ${GOLDEN_OUT}."

echo "${KB_ID}"
