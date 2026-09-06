package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/justrag/go-backend/internal/prompts"
)

// Pairwise preference judge (ruling W4-R4).
//
// The Likert answer-relevance judge saturated: two configurations that
// differ visibly to a reader both score ~0.9, so the metric cannot rank
// them. A PREFERENCE judge can — it never has to place an answer on an
// absolute scale, only to say which of two answers is better for the same
// question.
//
// The known failure mode of an LLM preference judge is position bias: it
// prefers whichever answer it read first, independently of content. The
// standard remedy, implemented here, is to judge every pair TWICE with the
// positions swapped and to count a win only when both orderings name the
// same answer; a pair the judge flips on is recorded as a tie. That turns
// position bias from a systematic offset into ties, which are reported
// separately and excluded from the win rate rather than silently averaged
// into it.
//
// This is an OFFLINE mode: it reads the persisted `judge.answer` of two
// eval reports. No retrieval, no answer generation, no DB writes.

// Winner values of a PairVerdict.
const (
	WinnerA   = "A"
	WinnerB   = "B"
	WinnerTie = "tie"
	// WinnerSkipped marks a pair that was not judged at all (a missing
	// answer on one side, or a judge transport error). Skipped pairs are
	// excluded from wins/ties/losses so they cannot dilute the win rate.
	WinnerSkipped = "skipped"
)

// RouteUnclassified is the per-route bucket for questions whose golden row
// carries no query_type.
const RouteUnclassified = "unclassified"

// wilsonZ is the 95 % two-sided normal quantile used for the score
// (Wilson) interval. Wilson rather than the normal approximation because
// the golden sets are small (10–30 pairs) and win rates land near the
// boundaries, where the normal interval leaves [0,1].
const wilsonZ = 1.96

// PairVerdict is the outcome for one question judged in both position
// orders. ReasoningAB/ReasoningBA are the judge's own justifications, kept
// so a reviewer can see WHY a pair flipped.
type PairVerdict struct {
	ID    string `json:"id"`
	Route string `json:"route"`
	// Winner is A | B | tie | skipped, always expressed in terms of the
	// A report and the B report — never in terms of the slot a given
	// judge call saw.
	Winner string `json:"winner"`
	// AgreeBothOrders is true when both position orders named the same
	// side (including when both said "tie"). A pair with
	// AgreeBothOrders=false is always recorded as a tie.
	AgreeBothOrders bool   `json:"agree_both_orders"`
	ReasoningAB     string `json:"reasoning_ab,omitempty"`
	ReasoningBA     string `json:"reasoning_ba,omitempty"`
	// Note explains a skipped pair (missing answer, judge error).
	Note string `json:"note,omitempty"`
}

// PairwiseCounts is the win/tie/loss tally plus its derived rates for one
// bucket (overall or one route).
type PairwiseCounts struct {
	Wins   int `json:"wins"`
	Ties   int `json:"ties"`
	Losses int `json:"losses"`
	// WinRate is wins / (wins + losses) — ties excluded, per W4-R4.
	WinRate float64 `json:"win_rate"`
	// TieRate is ties / (wins + ties + losses): how often the judge
	// flipped or genuinely could not separate the two answers. A high
	// tie rate means the win rate rests on few decided pairs.
	TieRate    float64 `json:"tie_rate"`
	WilsonLow  float64 `json:"wilson_low"`
	WilsonHigh float64 `json:"wilson_high"`
}

// PairwiseResult is the full output of RunPairwise.
type PairwiseResult struct {
	Pairs  []PairVerdict `json:"pairs"`
	Wins   int           `json:"wins"`
	Ties   int           `json:"ties"`
	Losses int           `json:"losses"`
	// WinRate / WilsonLow / WilsonHigh mirror the overall PairwiseCounts
	// fields; they are duplicated at the top level because they are the
	// headline numbers of this mode.
	WinRate    float64                   `json:"win_rate"`
	TieRate    float64                   `json:"tie_rate"`
	WilsonLow  float64                   `json:"wilson_low"`
	WilsonHigh float64                   `json:"wilson_high"`
	ByRoute    map[string]PairwiseCounts `json:"by_route,omitempty"`
	// Skipped counts pairs present in both reports but not judged
	// (missing answer or judge error); Errors counts the subset that
	// failed because of a judge error.
	Skipped int `json:"skipped"`
	Errors  int `json:"errors"`
	// OnlyInA / OnlyInB list question ids present in exactly one report.
	// Comparing reports from two different golden sets is a user error
	// this makes visible instead of silently shrinking the sample.
	OnlyInA []string `json:"only_in_a,omitempty"`
	OnlyInB []string `json:"only_in_b,omitempty"`
}

// RunPairwise compares the persisted answers of two judged reports question
// by question. Every question id present in both reports with a non-empty
// judge.answer on both sides is judged twice (A,B and B,A); the swapped
// verdict is mapped back before the two are compared.
//
// lang is the fallback prompt language for questions whose golden row
// carries none. An error is returned only for an unusable input pair (no
// overlapping questions at all); per-pair failures are recorded in the
// result, because a measurement run that dies on question 7 of 30 is worse
// than one that reports 29 pairs and one error.
func RunPairwise(ctx context.Context, completer Completer, a, b *Report, lang string) (*PairwiseResult, error) {
	if a == nil || b == nil {
		return nil, fmt.Errorf("pairwise: both reports are required")
	}
	if completer == nil {
		return nil, fmt.Errorf("pairwise: completer is required")
	}

	byIDB := make(map[string]QuestionReport, len(b.Questions))
	for _, qr := range b.Questions {
		if qr.Question.ID == "" {
			continue
		}
		if _, dup := byIDB[qr.Question.ID]; !dup {
			byIDB[qr.Question.ID] = qr
		}
	}

	res := &PairwiseResult{ByRoute: map[string]PairwiseCounts{}}
	routeCounts := map[string]*PairwiseCounts{}
	seenA := make(map[string]struct{}, len(a.Questions))

	for _, qa := range a.Questions {
		id := qa.Question.ID
		if id == "" {
			continue
		}
		if _, dup := seenA[id]; dup {
			continue
		}
		seenA[id] = struct{}{}

		qb, ok := byIDB[id]
		if !ok {
			res.OnlyInA = append(res.OnlyInA, id)
			continue
		}

		route := qa.Question.QueryType
		if route == "" {
			route = qb.Question.QueryType
		}
		if route == "" {
			route = RouteUnclassified
		}

		v := PairVerdict{ID: id, Route: route}

		answerA, answerB := judgeAnswer(qa), judgeAnswer(qb)
		if answerA == "" || answerB == "" {
			v.Winner = WinnerSkipped
			v.Note = missingAnswerNote(answerA, answerB)
			res.Pairs = append(res.Pairs, v)
			res.Skipped++
			continue
		}

		qlang := qa.Question.Language
		if qlang == "" {
			qlang = lang
		}
		question := qa.Question.Question
		if question == "" {
			question = qb.Question.Question
		}
		notes := qa.Question.Notes
		if notes == "" {
			notes = qb.Question.Notes
		}

		sys := prompts.PairwiseSystemPrompt(qlang)

		// Order 1: the A report's answer in slot A.
		winAB, reasonAB, err := judgePair(ctx, completer, sys, question, notes, answerA, answerB)
		if err != nil {
			v.Winner = WinnerSkipped
			v.Note = fmt.Sprintf("judge (A,B) failed: %v", err)
			res.Pairs = append(res.Pairs, v)
			res.Skipped++
			res.Errors++
			continue
		}
		// Order 2: positions swapped. This is the position debias — without
		// it a judge that always prefers whatever it reads first would hand
		// the A report a 100 % win rate.
		winBA, reasonBA, err := judgePair(ctx, completer, sys, question, notes, answerB, answerA)
		if err != nil {
			v.Winner = WinnerSkipped
			v.Note = fmt.Sprintf("judge (B,A) failed: %v", err)
			v.ReasoningAB = reasonAB
			res.Pairs = append(res.Pairs, v)
			res.Skipped++
			res.Errors++
			continue
		}

		v.ReasoningAB, v.ReasoningBA = reasonAB, reasonBA
		// Map the swapped verdict back into A/B report terms.
		mapped := flipWinner(winBA)
		v.AgreeBothOrders = winAB == mapped
		if v.AgreeBothOrders {
			v.Winner = winAB
		} else {
			v.Winner = WinnerTie
		}

		res.Pairs = append(res.Pairs, v)
		rc := routeCounts[route]
		if rc == nil {
			rc = &PairwiseCounts{}
			routeCounts[route] = rc
		}
		switch v.Winner {
		case WinnerA:
			res.Wins++
			rc.Wins++
		case WinnerB:
			res.Losses++
			rc.Losses++
		default:
			res.Ties++
			rc.Ties++
		}
	}

	for id := range byIDB {
		if _, ok := seenA[id]; !ok {
			res.OnlyInB = append(res.OnlyInB, id)
		}
	}
	sort.Strings(res.OnlyInA)
	sort.Strings(res.OnlyInB)

	overall := PairwiseCounts{Wins: res.Wins, Ties: res.Ties, Losses: res.Losses}
	finalizeCounts(&overall)
	res.WinRate, res.TieRate = overall.WinRate, overall.TieRate
	res.WilsonLow, res.WilsonHigh = overall.WilsonLow, overall.WilsonHigh
	for route, rc := range routeCounts {
		finalizeCounts(rc)
		res.ByRoute[route] = *rc
	}

	if len(res.Pairs) == 0 {
		return res, fmt.Errorf("pairwise: the two reports share no question ids (%d only in A, %d only in B) — are they runs of the same golden set?", len(res.OnlyInA), len(res.OnlyInB))
	}
	return res, nil
}

// judgeAnswer returns the persisted answer of a question report, or "".
func judgeAnswer(qr QuestionReport) string {
	if qr.Judge == nil {
		return ""
	}
	return strings.TrimSpace(qr.Judge.Answer)
}

func missingAnswerNote(answerA, answerB string) string {
	switch {
	case answerA == "" && answerB == "":
		return "no judge.answer in either report (was the run made with --judge?)"
	case answerA == "":
		return "no judge.answer in report A"
	default:
		return "no judge.answer in report B"
	}
}

// judgePair runs one ordering and returns the winner in SLOT terms
// ("A" = the answer passed as first), plus the judge's reasoning.
func judgePair(ctx context.Context, completer Completer, sys, question, notes, first, second string) (string, string, error) {
	resp, err := completer.Complete(ctx, prompts.PairwiseUserPrompt(question, notes, first, second), sys)
	if err != nil {
		return "", "", err
	}
	return parsePairwiseVerdict(resp)
}

// flipWinner maps a slot-terms verdict from the swapped ordering back into
// report terms.
func flipWinner(w string) string {
	switch w {
	case WinnerA:
		return WinnerB
	case WinnerB:
		return WinnerA
	default:
		return WinnerTie
	}
}

// parsePairwiseVerdict parses {"winner":...,"reasoning":...} tolerantly, in
// the same spirit as parseJudgeScore: models emit "A", "a", " b ", "1"/"2"
// and bare 1/2 for the same intent, and wrap the object in prose.
func parsePairwiseVerdict(resp string) (string, string, error) {
	var parsed struct {
		Winner    json.RawMessage `json:"winner"`
		Reasoning string          `json:"reasoning"`
	}
	if err := unmarshalStrict(resp, &parsed); err != nil {
		return "", "", err
	}
	winner, err := parsePairwiseWinner(parsed.Winner)
	if err != nil {
		return "", "", err
	}
	return winner, parsed.Reasoning, nil
}

func parsePairwiseWinner(raw json.RawMessage) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		var f float64
		if err2 := json.Unmarshal(raw, &f); err2 != nil {
			return "", fmt.Errorf("winner: not a string or number: %q", string(raw))
		}
		s = fmt.Sprintf("%g", f)
	}
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "a", "1", "answer a", "antwort a":
		return WinnerA, nil
	case "b", "2", "answer b", "antwort b":
		return WinnerB, nil
	case "tie", "0", "draw", "equal", "gleich", "unentschieden", "none", "neither":
		return WinnerTie, nil
	}
	return "", fmt.Errorf("winner: unrecognised value %q", s)
}

// finalizeCounts fills WinRate/TieRate and the Wilson interval in place.
func finalizeCounts(c *PairwiseCounts) {
	decided := c.Wins + c.Losses
	total := decided + c.Ties
	if total > 0 {
		c.TieRate = float64(c.Ties) / float64(total)
	}
	if decided == 0 {
		// No decided pair: the win rate is undefined, and the honest
		// interval is the whole unit range.
		c.WinRate, c.WilsonLow, c.WilsonHigh = 0, 0, 1
		return
	}
	c.WinRate = float64(c.Wins) / float64(decided)
	c.WilsonLow, c.WilsonHigh = WilsonInterval(c.Wins, decided, wilsonZ)
}

// WilsonInterval returns the Wilson score interval for successes out of n
// trials at the given z. Exported because the pairwise printer and any
// later coverage/abstention rate reporting want the same interval.
func WilsonInterval(successes, n int, z float64) (low, high float64) {
	if n <= 0 {
		return 0, 1
	}
	p := float64(successes) / float64(n)
	nf := float64(n)
	z2 := z * z
	denom := 1 + z2/nf
	center := (p + z2/(2*nf)) / denom
	margin := z * math.Sqrt(p*(1-p)/nf+z2/(4*nf*nf)) / denom
	low, high = center-margin, center+margin
	if low < 0 {
		low = 0
	}
	if high > 1 {
		high = 1
	}
	return low, high
}
