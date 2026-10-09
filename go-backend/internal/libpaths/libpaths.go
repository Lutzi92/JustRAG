// Package libpaths holds the object-storage path conventions of the user file
// library. It is a leaf package so cascade (deletion) and processor (writing)
// can share them without importing each other.
package libpaths

// ParseCacheDir returns the storage prefix holding every cached parse of a
// library file: users/<ownerID>/parses/<userFileID>/.
func ParseCacheDir(ownerID, userFileID string) string {
	return "users/" + ownerID + "/parses/" + userFileID + "/"
}

// ChatTextKey returns users/<ownerID>/parses/<userFileID>/chat-text.json, the
// parsed text cached for KB-less library chat. It lives under ParseCacheDir, so
// deleting the library file's parse directory removes it too.
func ChatTextKey(ownerID, userFileID string) string {
	return ParseCacheDir(ownerID, userFileID) + "chat-text.json"
}

// ParseCacheKey returns users/<ownerID>/parses/<userFileID>/<configHash>.json.
func ParseCacheKey(ownerID, userFileID, configHash string) string {
	return ParseCacheDir(ownerID, userFileID) + configHash + ".json"
}
