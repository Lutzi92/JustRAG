// Package store defines shared sentinel errors used across the
// repository layer. Stores in domain-specific packages (kb, chat,
// adminconfigs, etc.) return these sentinels so handlers can match
// with errors.Is instead of comparing error message strings.
//
// Wrapping convention: stores may wrap with %w to add context,
// e.g. fmt.Errorf("RemoveKBShare: %w", store.ErrNotFound). Callers
// MUST use errors.Is, never err.Error() == "...".
package store

import "errors"

// ErrNotFound signals that a lookup, update, or delete touched no
// rows. Domain stores translate driver-level "no rows" outcomes
// (pgx.ErrNoRows from a Scan, RowsAffected() == 0 from an Exec) into
// this sentinel so the rest of the codebase can match a single value.
//
// Return convention for store methods:
//
//   - Single-row lookups that return *T: prefer (nil, store.ErrNotFound)
//     when the row is absent. Callers branch with errors.Is.
//   - Mutations that return the updated row (UPDATE … RETURNING):
//     same — (nil, store.ErrNotFound) when no row matched the WHERE
//     clause. Distinguishes "row missing" from "row found, no changes".
//   - Mutations without a return value (DELETE, UPDATE without
//     RETURNING): return a wrapped ErrNotFound (e.g.
//     fmt.Errorf("kb_share kb=%s user=%s: %w", ..., store.ErrNotFound))
//     when RowsAffected() == 0.
//
// pgxutil.QueryOne intentionally returns (nil, nil) on no rows — that
// helper is one layer below this convention. Stores that build on
// QueryOne translate the nil pointer into ErrNotFound at the public
// method boundary when the caller treats "missing" as an error
// condition (most do; the exceptions — GetKBChunkConfig,
// GetKBSystemPrompt — document why "missing" is the same as "no
// override" and are explicit about returning nil error).
var ErrNotFound = errors.New("not found")

// ErrConflict signals that a write violated a uniqueness constraint
// (duplicate username, KB name, slug, etc.). Stores translate the
// pgconn.PgError code "23505" into this sentinel at the boundary so
// handlers can match with errors.Is and return HTTP 409 without
// importing the postgres driver.
var ErrConflict = errors.New("conflict")
