package chat

// chunkRefsFromSources extracts the chunk IDs (and their source position)
// from a message's sources, skipping any source without a ChunkID or whose
// ChunkID is not a UUID. Returns nil slices when there is nothing to persist.
//
// The UUID filter matters because these IDs flow into an INSERT that runs
// inside the AddMessage transaction (message_chunks.chunk_id is uuid): a
// non-UUID value would fail the ::uuid cast and roll back the message
// persist itself. All current ChatSource producers set ChunkID from a
// chunk-row UUID, so this is a defensive guard, not a live filter — it keeps
// feedback-link capture fail-open relative to the core write path.
func chunkRefsFromSources(sources []ChatSource) (ids []string, positions []int) {
	for _, s := range sources {
		if !isUUID(s.ChunkID) {
			continue
		}
		ids = append(ids, s.ChunkID)
		positions = append(positions, s.Index)
	}
	return ids, positions
}

// isUUID reports whether s is shaped like a canonical RFC-4122 UUID
// (8-4-4-4-12 lowercase/uppercase hex). Dependency-free so this helper stays
// in the hot AddMessage path without pulling in a UUID package.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := range 36 {
		c := s[i]
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		if !isHex {
			return false
		}
	}
	return true
}
