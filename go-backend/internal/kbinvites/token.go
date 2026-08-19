// Package kbinvites implements permanent, revocable KB invite links: a KB
// admin mints a link carrying a role, anyone who opens it and is signed in
// redeems it and receives that role on the KB.
//
// The redeem path is deliberately NOT KB-gated (see http.go): the person
// redeeming has no role on the KB yet, so a kbViewChain would 403 them
// before they could ever join.
package kbinvites

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// tokenBytes is the raw entropy per link. 32 bytes render as 43 base64url
// characters and make guessing infeasible, which is what lets the token live
// in the database in plaintext (the link must stay re-copyable).
const tokenBytes = 32

// NewToken returns a fresh URL-safe invite token.
func NewToken() (string, error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("kbinvites: read random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
