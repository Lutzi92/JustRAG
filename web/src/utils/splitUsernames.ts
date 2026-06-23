// Splits a pasted blob of usernames on any run of commas, whitespace, or
// newlines. Trims, drops empties, and dedupes case-insensitively while keeping
// the first-seen casing for display. The backend re-normalizes to lowercase.
export function splitUsernames(text: string): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const raw of text.split(/[\s,]+/)) {
    const u = raw.trim();
    if (!u) continue;
    const key = u.toLowerCase();
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(u);
  }
  return out;
}
