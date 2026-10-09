package globalsearch

// Exported for the EXPLAIN tests in the integration test files, which must
// plan the exact production SQL rather than a copy that could drift from it.
var (
	TopicsSQL         = topicsSQL
	SourcesSQL        = sourcesSQL
	ChatsSQL          = chatsSQL
	MessagesSQL       = messagesSQL
	SnippetOptions    = snippetOptions
	PrefixTSQuery     = prefixTSQuery
	SetFuzzyThreshold = setFuzzyThreshold
)
