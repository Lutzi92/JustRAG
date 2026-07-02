// Embedding-dimension field semantics (ai_models.dimensions):
// 0 = "auto" — use the model's native output size; the backend omits the
// `dimensions` request parameter entirely.
// > 0 = request MRL-truncated vectors of that size.
//
// The input must never coerce empty/invalid to a concrete number (the old
// `|| 1536` behavior): an accidental save would silently truncate embeddings
// and point queries at a different dim-keyed vector table.

/** Parses the dimensions text-input value. Empty/invalid/negative → 0 (auto). */
export function parseDimensionsInput(value: string): number {
    const n = parseInt(value, 10);
    if (Number.isNaN(n) || n < 0) return 0;
    return n;
}

/** Formats a stored dimensions value for the input; 0/unset renders empty so the "auto" placeholder shows. */
export function formatDimensionsValue(dims: number | undefined): string {
    if (!dims || dims <= 0) return '';
    return String(dims);
}
