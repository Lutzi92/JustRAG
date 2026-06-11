#!/usr/bin/env node
// Verifies that every inline <script> in the built frontend (web/dist/index.html)
// has a matching 'sha256-…' token in middleware.DefaultCSP
// (go-backend/internal/middleware/security.go).
//
// A drifted hash silently breaks the SPA bootstrap in CSP-enforcing browsers —
// the server has no signal of the mismatch, so CI is the only place to catch
// it. Run after `npm run build` (CI does this in the web job).
//
// Exit codes: 0 = all inline scripts covered (or none exist), 1 = drift.

import { readFileSync } from "node:fs";
import { createHash } from "node:crypto";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const indexHtmlPath = join(root, "web", "dist", "index.html");
const securityGoPath = join(
  root,
  "go-backend",
  "internal",
  "middleware",
  "security.go",
);

const html = readFileSync(indexHtmlPath, "utf8");
const securityGo = readFileSync(securityGoPath, "utf8");

const cspMatch = securityGo.match(/const DefaultCSP = "((?:[^"\\]|\\.)*)"/);
if (!cspMatch) {
  console.error(`could not find DefaultCSP in ${securityGoPath}`);
  process.exit(1);
}
const csp = cspMatch[1];
const cspHashes = new Set(
  [...csp.matchAll(/'sha256-([A-Za-z0-9+/=]+)'/g)].map((m) => m[1]),
);

// Inline scripts only: a <script> tag without a src attribute. The browser
// hashes the exact bytes between the tags, no trimming.
const inlineScripts = [
  ...html.matchAll(/<script(?![^>]*\bsrc\s*=)[^>]*>([\s\S]*?)<\/script>/gi),
].map((m) => m[1]);

if (inlineScripts.length === 0) {
  console.log(
    `OK: no inline scripts in ${indexHtmlPath}; DefaultCSP hash tokens are unused but harmless.`,
  );
  process.exit(0);
}

let drift = false;
const seen = new Set();
for (const body of inlineScripts) {
  const hash = createHash("sha256").update(body, "utf8").digest("base64");
  seen.add(hash);
  if (cspHashes.has(hash)) {
    console.log(`OK: inline script covered by 'sha256-${hash}'`);
  } else {
    drift = true;
    console.error(
      `DRIFT: inline script (${body.length} bytes, starts ${JSON.stringify(
        body.slice(0, 60),
      )}) has hash 'sha256-${hash}' which is NOT in DefaultCSP.\n` +
        `Update the 'sha256-…' token in ${securityGoPath}.`,
    );
  }
}

for (const hash of cspHashes) {
  if (!seen.has(hash)) {
    console.log(
      `note: DefaultCSP allows 'sha256-${hash}' but no current inline script matches it (stale but harmless).`,
    );
  }
}

process.exit(drift ? 1 : 0);
