package processor

import (
	"context"

	"golang.org/x/sync/semaphore"
)

// LargeFileGate bounds how many "large" spreadsheets (per
// tabular_large_file_bytes) this process ingests concurrently
// (tabular_large_file_concurrency). ProcessFile stats each spreadsheet file
// on disk and only acquires a slot when the file exceeds the threshold —
// small spreadsheets bypass the gate entirely and always ingest immediately.
//
// One instance is constructed at worker startup with a fixed slot count
// (Ruling R64: the slot count is read once, not per file — re-reading it on
// every file would let concurrently-running Ingest calls observe different
// slot counts mid-flight, which a semaphore cannot express). The size
// threshold itself is read fresh for every file via
// chat.TabularLargeFileBytes, so an admin can retune it without a worker
// restart.
//
// A nil *LargeFileGate (an unwired Processor, e.g. in tests or the API
// server) is a no-op: every call site in ProcessFile checks p.largeGate !=
// nil before touching it, so Acquire/Release are never invoked on a nil
// receiver.
type LargeFileGate struct {
	sem   *semaphore.Weighted
	slots int64
}

// NewLargeFileGate creates a gate with the given number of concurrent
// slots. Fewer than 1 is clamped to 1 — a misconfigured 0 (or negative)
// slot count must degrade to "fully serialised large-file ingestion", not
// to "large spreadsheets can never acquire a slot at all" (a Weighted
// semaphore with 0 capacity blocks every Acquire forever).
func NewLargeFileGate(slots int) *LargeFileGate {
	if slots < 1 {
		slots = 1
	}
	n := int64(slots)
	return &LargeFileGate{sem: semaphore.NewWeighted(n), slots: n}
}

// Acquire blocks until a slot is free or ctx is done, in which case it
// returns ctx.Err() (via the underlying semaphore.Weighted).
func (g *LargeFileGate) Acquire(ctx context.Context) error {
	return g.sem.Acquire(ctx, 1)
}

// Release frees the slot taken by a prior successful Acquire. Must not be
// called without a matching successful Acquire.
func (g *LargeFileGate) Release() {
	g.sem.Release(1)
}

// stageDetailWaitingForSlot is the human-readable stage_detail ProcessFile
// records while blocked on LargeFileGate.Acquire for a large spreadsheet.
// Bilingual per the KB's raw language code, mirroring the `lang == "de"`
// convention used elsewhere for user-facing ingest strings (e.g.
// internal/chat/enumeration_classifier.go).
func stageDetailWaitingForSlot(rawLang string) string {
	if rawLang == "de" {
		return "Wartet auf einen Slot für große Dateien"
	}
	return "Waiting for a large-file slot"
}
