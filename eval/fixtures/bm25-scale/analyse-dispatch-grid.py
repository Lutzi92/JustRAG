#!/usr/bin/env python3
"""Wave 4 Task 6 grid analysis (W4-R8): ts_rank (A) vs bm25 (C, C5) with
--orchestrator-dispatch on, 3 repeats per cell. Reads the 9 `t6-<cell>.json` /
`t6-<cell>.log` grid artifacts (workspace-only, gitignored, produced by
`.superpowers/sdd/2026-09-06-rag-sota-wave4/t6-grid-resume.sh`) and prints a
Markdown report:

  - cells table: errors, plan_execute count, agent.orchestrator distribution,
    keyword_mode line counts (from the driver log's `rag.search.stages` lines)
  - per route (overall/lookup/enumeration/complex_reasoning) x metric
    (recall/MRR/nDCG): mean +/- max-spread over the 3 repeats, in pp, plus the
    ts_rank noise band (max spread across A1..A3, floored at 1.0 pp)
  - W4-R8 decision: bm25_scoring_mode flips only if C or C5 beats A's mean
    lookup MRR beyond the band AND no route's mean recall/MRR drops beyond it
  - the Wave-2 "-7pp complex_reasoning MRR under bm25" claim, settled with
    the measured plan-execute-path number and band
  - top-5 complex_reasoning questions by |delta reciprocal-rank| between the
    A-mean and C-mean, to show what drives the route MRR difference

A cell is REJECTED (script exits 2) if it has errors > 0, or its log carries
a keyword_mode line that doesn't match its cell's mode (A must be all
ts_rank, C/C5 must be all bm25) -- per the handoff's per-cell validity rules.

Usage: python3 analyse-dispatch-grid.py [workspace-dir]
Default workspace-dir: .superpowers/sdd/2026-09-06-rag-sota-wave4 (relative
to the repo root -- run this script from the worktree root).
"""
import json
import os
import re
import sys
from collections import defaultdict

DEFAULT_WORKSPACE = os.path.join(
    ".superpowers", "sdd", "2026-09-06-rag-sota-wave4"
)
W = sys.argv[1] if len(sys.argv) > 1 else DEFAULT_WORKSPACE
CELLS = {"A": ["A1", "A2", "A3"], "C": ["C1", "C2", "C3"], "C5": ["C51", "C52", "C53"]}
MODE = {"A": "ts_rank", "C": "bm25", "C5": "bm25"}
ROUTES = ["overall", "lookup", "enumeration", "complex_reasoning"]
METRICS = [("recall", "mean_recall"), ("mrr", "mrr"), ("ndcg", "mean_ndcg")]
FLOOR_PP = 1.0


def load(cell):
    p = os.path.join(W, f"t6-{cell}.json")
    if not os.path.exists(p):
        return None
    r = json.load(open(p))
    log_path = os.path.join(W, f"t6-{cell}.log")
    log = open(log_path, errors="replace").read() if os.path.exists(log_path) else ""
    modes = re.findall(r'"keyword_mode":"([a-z0-9_]+)"', log)
    agents = {}
    for q in r["questions"]:
        o = (q.get("agent") or {}).get("orchestrator")
        agents[o] = agents.get(o, 0) + 1
    return {
        "errors": len(r.get("errors") or []),
        "agents": agents,
        "modes": {m: modes.count(m) for m in set(modes)},
        "agg": r["aggregate"],
        "routes": r["route_aggregates"],
        "questions": r["questions"],
    }


def val(d, route, key):
    return d["agg"][key] if route == "overall" else d["routes"][route][key]


data, bad = {}, []
for grp, cells in CELLS.items():
    for c in cells:
        d = load(c)
        if d is None:
            continue
        data[c] = d
        wrong = [m for m in d["modes"] if m != MODE[grp]]
        if d["errors"] or wrong:
            bad.append((c, d["errors"], wrong))
if bad:
    print("REJECTED cells (errors or wrong keyword_mode):", bad)
    sys.exit(2)

print("## Cells\n")
print("| cell | mode | errors | plan_execute | agent.orchestrator | keyword_mode lines |")
print("|---|---|---|---|---|---|")
for grp, cells in CELLS.items():
    for c in cells:
        if c in data:
            d = data[c]
            print(
                f"| {c} | {MODE[grp]} | {d['errors']} | {d['agents'].get('plan_execute', 0)} "
                f"| {d['agents']} | {d['modes']} |"
            )


def stats(grp, route, key):
    vals = [val(data[c], route, key) for c in CELLS[grp] if c in data]
    if not vals:
        return None
    return (sum(vals) / len(vals), max(vals) - min(vals), len(vals))


print("\n## Per route / metric (mean +/- max-spread, pp)\n")
print("| route | metric | " + " | ".join(f"{g} (n)" for g in CELLS) + " | band A (pp) |")
print("|---|---|" + "---|" * len(CELLS) + "---|")
band = {}
for route in ROUTES:
    for m, key in METRICS:
        cols = []
        for grp in CELLS:
            s = stats(grp, route, key)
            cols.append("-" if s is None else f"{s[0]:.3f} +/- {100*s[1]:.1f} ({s[2]})")
        sA = stats("A", route, key)
        b = max(FLOOR_PP, 100 * sA[1]) if sA else None
        band[(route, m)] = b
        b_str = f"{b:.1f}" if b is not None else "-"
        print(f"| {route} | {m} | " + " | ".join(cols) + f" | {b_str} |")

print("\n## Lookup / enumeration recall gains (bm25 vs ts_rank)\n")
for route in ("lookup", "enumeration"):
    sa = stats("A", route, "mean_recall")
    for grp in ("C", "C5"):
        sg = stats(grp, route, "mean_recall")
        if sa and sg:
            gain = 100 * (sg[0] - sa[0])
            b = band[(route, "recall")]
            print(
                f"- {route} recall, {grp} vs A: {gain:+.1f} pp (A {sa[0]:.3f} +/- {100*sa[1]:.1f}, "
                f"{grp} {sg[0]:.3f} +/- {100*sg[1]:.1f}) vs band {b:.1f} -> "
                f"{'beyond band' if abs(gain) > b else 'within band'}"
            )

print("\n## Decision (W4-R8)\n")
sA_lookup = stats("A", "lookup", "mrr")
for grp in ("C", "C5"):
    s = stats(grp, "lookup", "mrr")
    if s is None or sA_lookup is None:
        print(f"- {grp}: incomplete")
        continue
    gain = 100 * (s[0] - sA_lookup[0])
    b = band[("lookup", "mrr")]
    wins_lookup = gain > b
    losses = []
    for route in ROUTES:
        for m, key in (("recall", "mean_recall"), ("mrr", "mrr")):
            sa, sg = stats("A", route, key), stats(grp, route, key)
            if sa and sg:
                drop = 100 * (sa[0] - sg[0])
                if drop > band[(route, m)]:
                    losses.append(f"{route} {m} -{drop:.1f} pp (band {band[(route, m)]:.1f})")
    verdict = "WINS" if (wins_lookup and not losses) else "does not win"
    print(
        f"- {grp}: lookup MRR {gain:+.1f} pp vs band {b:.1f} -> "
        f"{'beyond' if wins_lookup else 'within'} band; losses beyond band: {losses or 'none'} -> **{verdict}**"
    )

sa, sc = stats("A", "complex_reasoning", "mrr"), stats("C", "complex_reasoning", "mrr")
if sa and sc:
    d = 100 * (sc[0] - sa[0])
    b = band[("complex_reasoning", "mrr")]
    print(
        f"\n## Wave-2 claim (complex_reasoning MRR under bm25 on the plan-execute path)\n\n"
        f"- ts_rank {sa[0]:.3f} +/- {100*sa[1]:.1f} pp (n={sa[2]}), bm25 {sc[0]:.3f} +/- {100*sc[1]:.1f} pp (n={sc[2]}), "
        f"difference {d:+.1f} pp vs band {b:.1f} pp -> the Wave-2 -7 pp is "
        f"{'CONFIRMED in direction (loss beyond band)' if -d > b else 'NOT confirmed (difference within band)'}; "
        f"magnitude {d:+.1f} pp vs Wave-2 -7.0 pp."
    )

# ---------------------------------------------------------------------------
# Per-question drivers: top 5 complex_reasoning questions by |delta reciprocal
# rank| between the A-mean (3 reps) and C-mean (3 reps).
# ---------------------------------------------------------------------------
def load_rr(cells, query_type):
    out = defaultdict(list)
    meta = {}
    for c in cells:
        if c not in data:
            continue
        for q in data[c]["questions"]:
            qi = q["question"]
            if qi.get("query_type") != query_type:
                continue
            qid = qi["id"]
            out[qid].append(q["metrics"]["reciprocal_rank"])
            meta[qid] = qi["question"]
    return out, meta


a_rr, a_meta = load_rr(CELLS["A"], "complex_reasoning")
c_rr, c_meta = load_rr(CELLS["C"], "complex_reasoning")
ids = set(a_rr) & set(c_rr)
rows = []
for qid in ids:
    am = sum(a_rr[qid]) / len(a_rr[qid])
    cm = sum(c_rr[qid]) / len(c_rr[qid])
    rows.append((abs(am - cm), qid, am, cm, a_meta[qid]))
rows.sort(reverse=True)

print(
    f"\n## Per-question drivers: complex_reasoning MRR, A vs C "
    f"(top 5 of {len(ids)} shared question ids by |delta mean reciprocal rank|)\n"
)
print("| question id | |delta RR| | A mean RR | C mean RR | question |")
print("|---|---|---|---|---|")
for r in rows[:5]:
    q_text = r[4].replace("|", "\\|")
    print(f"| {r[1]} | {r[0]:.3f} | {r[2]:.3f} | {r[3]:.3f} | {q_text} |")
