#!/usr/bin/env bash
# Seeds a KB named "CERT Fixtures" with the 40-file synthetic dated CERT
# advisory corpus (eval/fixtures/cert-advisories/), backdates each file's
# files.created_at per manifest.tsv so the recency-listing / recency-boost
# mechanisms have a real date spread to exercise, and writes a
# KB-id-resolved copy of the recency golden set.
#
# Spec: .superpowers/sdd/2026-09-05-rag-sota-wave2/task-8-brief.md
# Rulings: W2-R8 (amended — only files.created_at needs backdating; the
# date-window filter (fileIDsInDateRange) and the recency boost
# (fileCreatedTimes) both read files.created_at on the MAIN DB, so there is
# no vector-DB row to touch), W2-R11.
#
# Usage:
#   JUSTRAG_ADMIN_PASSWORD=... ./eval/fixtures/seed-cert.sh
#   JUSTRAG_ADMIN_PASSWORD=... ./eval/fixtures/seed-cert.sh --restamp --kb-id <uuid>
#
# Every run (fresh seed or --restamp) re-runs the backdating UPDATEs
# relative to `now()`, so a previously seeded KB stays valid for the
# 7-day recency-listing window no matter how much wall-clock time has
# passed since the initial seed (W2-R8).
#
# Env:
#   JUSTRAG_URL              Base URL of a running JustRAG instance.
#                            Default: http://localhost:3000
#   JUSTRAG_ADMIN_USER       Admin username. Default: admin
#   JUSTRAG_ADMIN_PASSWORD   Admin password. Required, no default — never
#                            invent one.
#   PSQL_CMD                 Command (as a single string, split on
#                            whitespace) used to run SQL against the main
#                            DB, fed the SQL script on stdin. Default:
#                            "docker compose -p justrag exec -T db psql -U postgres -d rag_db"
#
# Flags:
#   --restamp        Skip ingestion entirely; only re-run the backdating
#                     UPDATEs (and regenerate the .local.jsonl). Requires
#                     --kb-id.
#   --kb-id <uuid>    Target this KB instead of looking one up / creating
#                     one by name. Required with --restamp.
#
# Requires: bash, curl, jq, date (GNU coreutils; -d flag). Does NOT run
# npm install or touch node_modules.
#
# Output:
#   - prints the seeded/targeted KB id to stdout
#   - writes eval/golden/cert-recency-de.local.jsonl (gitignored): the
#     committed cert-recency-de.jsonl with kb_id rewritten from the
#     REPLACE_WITH_FIXTURE_KB_ID placeholder to the real KB id, and any
#     "{TODAY-N}" placeholder in a question string rewritten to the ISO
#     date N days before today.
set -euo pipefail

JUSTRAG_URL="${JUSTRAG_URL:-http://localhost:3000}"
JUSTRAG_ADMIN_USER="${JUSTRAG_ADMIN_USER:-admin}"
JUSTRAG_ADMIN_PASSWORD="${JUSTRAG_ADMIN_PASSWORD:-}"
PSQL_CMD="${PSQL_CMD:-docker compose -p justrag exec -T db psql -U postgres -d rag_db}"

KB_NAME="CERT Fixtures"

RESTAMP=0
KB_ID_ARG=""
while [ $# -gt 0 ]; do
	case "$1" in
		--restamp)
			RESTAMP=1
			shift
			;;
		--kb-id)
			KB_ID_ARG="${2:-}"
			shift 2
			;;
		*)
			echo "error: unknown argument: $1" >&2
			exit 2
			;;
	esac
done

# Resolve paths relative to this script's location so it can be run from
# any working directory.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
CORPUS_DIR="${REPO_ROOT}/eval/fixtures/cert-advisories"
MANIFEST="${CORPUS_DIR}/manifest.tsv"
GOLDEN_SRC="${REPO_ROOT}/eval/golden/cert-recency-de.jsonl"
GOLDEN_OUT="${REPO_ROOT}/eval/golden/cert-recency-de.local.jsonl"

log() { printf '%s\n' "$*" >&2; }
die() { log "error: $*"; exit 1; }

command -v curl >/dev/null 2>&1 || die "curl is required"
command -v jq   >/dev/null 2>&1 || die "jq is required"
command -v date >/dev/null 2>&1 || die "date is required"

if [ -z "${JUSTRAG_ADMIN_PASSWORD}" ]; then
	die "JUSTRAG_ADMIN_PASSWORD is required (no default — never invent one)"
fi
if [ ! -f "${MANIFEST}" ]; then
	die "manifest not found: ${MANIFEST}"
fi
if [ ! -f "${GOLDEN_SRC}" ]; then
	die "golden set not found: ${GOLDEN_SRC}"
fi
if [ "${RESTAMP}" -eq 1 ] && [ -z "${KB_ID_ARG}" ]; then
	die "--restamp requires --kb-id <uuid>"
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
# 2. Resolve the KB: --kb-id wins; otherwise create/reuse by name.
# ---------------------------------------------------------------------------
if [ -n "${KB_ID_ARG}" ]; then
	KB_ID="${KB_ID_ARG}"
	log "Using KB id ${KB_ID} (from --kb-id)."
else
	existing_kb_id="$(auth_curl "${JUSTRAG_URL}/api/kb?limit=100" \
		| jq -r --arg name "${KB_NAME}" '[.[] | select(.name == $name)][0].id // empty')"

	if [ -n "${existing_kb_id}" ]; then
		KB_ID="${existing_kb_id}"
		log "Reusing existing KB \"${KB_NAME}\" (${KB_ID})."
	else
		create_response="$(auth_curl -X POST "${JUSTRAG_URL}/api/kb" \
			-H 'Content-Type: application/json' \
			-d "$(jq -n --arg name "${KB_NAME}" \
				'{name: $name, description: "Wave 2 Task 8 dated CERT-advisory fixtures (eval/fixtures/cert-advisories). Seeded by eval/fixtures/seed-cert.sh."}')")" \
			|| die "KB create request failed"
		KB_ID="$(printf '%s' "${create_response}" | jq -r '.id // empty')"
		if [ -z "${KB_ID}" ]; then
			die "KB create did not return an id: ${create_response}"
		fi
		log "Created KB \"${KB_NAME}\" (${KB_ID})."
	fi
fi

# ---------------------------------------------------------------------------
# 3. Ingest (skipped under --restamp).
# ---------------------------------------------------------------------------
if [ "${RESTAMP}" -eq 0 ]; then
	uploaded=0
	skipped=0
	while IFS=$'\t' read -r fname days_ago title; do
		[ -z "${fname}" ] && continue
		src="${CORPUS_DIR}/${fname}"
		[ -f "${src}" ] || die "manifest references missing file: ${fname}"

		# Text sources land as files.name = req.Title, VERBATIM (not the
		# corpus ".md" filename, and — contrary to an earlier assumption —
		# not run through sanitizeTitle/SafeNameSegment either: that
		# transform only shapes the on-disk *storage path*
		# (internal/files/http_ingest.go AddTextSource step 3); the
		# `files.name` DB column set in step 5 is `req.Title` unchanged.
		# Confirmed by inspecting a live-seeded row before relying on it.
		# So the expected ingested name is just the title itself; verified
		# for real in step 4 below regardless.
		expected_name="${title}"

		already="$(auth_curl "${JUSTRAG_URL}/api/kb/${KB_ID}/files?limit=1000" \
			| jq -r --arg name "${expected_name}" '[.[] | select(.name == $name)][0].id // empty')"
		if [ -n "${already}" ]; then
			skipped=$((skipped + 1))
			continue
		fi

		log "Ingesting ${fname} ..."
		jq -n --arg title "${title}" --rawfile content "${src}" '{title: $title, content: $content}' \
			| auth_curl -X POST "${JUSTRAG_URL}/api/kb/${KB_ID}/text" \
				-H 'Content-Type: application/json' \
				--data-binary @- >/dev/null \
			|| die "text-source ingest failed: ${fname}"
		uploaded=$((uploaded + 1))
	done < "${MANIFEST}"
	log "Ingested ${uploaded} advisory(ies), skipped ${skipped} (already present)."

	# -------------------------------------------------------------------
	# 4. Poll until every file in the KB is out of pending/processing.
	# -------------------------------------------------------------------
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

	# -------------------------------------------------------------------
	# 5. Verify every expected name landed exactly as computed. Fail
	#    loudly rather than silently backdating/testing against the wrong
	#    (or missing) files — if files.name ever stopped being req.Title
	#    verbatim, this would otherwise be a silent no-op on the
	#    backdating UPDATE below (0 rows affected).
	# -------------------------------------------------------------------
	actual_names="$(printf '%s' "${files_json}" | jq -r '.[].name' | sort -u)"
	missing=0
	while IFS=$'\t' read -r fname days_ago title; do
		[ -z "${fname}" ] && continue
		expected_name="${title}"
		if ! grep -qxF "${expected_name}" <<<"${actual_names}"; then
			log "MISSING expected file name in KB: ${expected_name} (from manifest row: ${fname})"
			missing=$((missing + 1))
		fi
	done < "${MANIFEST}"
	if [ "${missing}" -gt 0 ]; then
		die "${missing} manifest row(s) did not land under their expected name — see MISSING lines above"
	fi
	log "Verified: every manifest row's expected file name is present in the KB."
fi

# ---------------------------------------------------------------------------
# 6. Backdate files.created_at per manifest.tsv, relative to now() — every
#    run (fresh seed or --restamp) re-stamps so the 7-day recency-listing
#    window stays valid no matter how much wall-clock time has passed
#    since the corpus was first seeded (W2-R8). Only files.created_at:
#    both the date-window filter (SearchService.fileIDsInDateRange) and
#    the recency boost (fileCreatedTimes) read files.created_at on the
#    MAIN DB — there is no document_chunks_<dim> row to touch (W2-R8
#    amendment, verified against internal/vector/recency_boost.go).
# ---------------------------------------------------------------------------
log "Backdating files.created_at for ${KB_ID} ..."
sql_script="$(mktemp)"
trap 'rm -f "${sql_script}"' EXIT

{
	echo "BEGIN;"
	while IFS=$'\t' read -r fname days_ago title; do
		[ -z "${fname}" ] && continue
		expected_name="${title}"
		# Escape single quotes for SQL string literals (none occur in our
		# generated titles, but this keeps the script correct if a future
		# manifest row's title ever contains one).
		escaped_name="${expected_name//\'/\'\'}"
		printf "UPDATE files SET created_at = now() - make_interval(days => %s) WHERE kb_id = '%s'::uuid AND name = '%s';\n" \
			"${days_ago}" "${KB_ID}" "${escaped_name}"
	done < "${MANIFEST}"
	echo "COMMIT;"
} > "${sql_script}"

if ! ${PSQL_CMD} -v ON_ERROR_STOP=1 < "${sql_script}"; then
	die "backdating SQL failed against ${PSQL_CMD}"
fi
log "Backdated $(($(wc -l < "${MANIFEST}") )) file(s)."

# ---------------------------------------------------------------------------
# 7. Write the KB-id-resolved golden set copy, with any "{TODAY-N}"
#    placeholder rewritten to the ISO date N days before today.
# ---------------------------------------------------------------------------
tmp_golden="$(mktemp)"
jq -c --arg kb "${KB_ID}" '.kb_id = $kb' "${GOLDEN_SRC}" > "${tmp_golden}"

placeholders="$(grep -oE '\{TODAY-[0-9]+\}' "${tmp_golden}" | sort -u || true)"
for ph in ${placeholders}; do
	n="${ph#\{TODAY-}"
	n="${n%\}}"
	iso="$(date -u -d "-${n} days" +%F)"
	# Plain (unescaped) { and } are literal in GNU sed's basic regex syntax
	# — escaping them ("\{" / "\}") instead invokes BRE interval-expression
	# syntax and errors out ("Invalid preceding regular expression").
	sed -i "s/{TODAY-${n}}/${iso}/g" "${tmp_golden}"
done
mv "${tmp_golden}" "${GOLDEN_OUT}"
log "Wrote $(wc -l < "${GOLDEN_OUT}" | tr -d ' ') question(s) to ${GOLDEN_OUT}."

echo "${KB_ID}"
