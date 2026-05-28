package vector

// EffectiveTopN selects the candidate-pool size for a single search call
// based on the query-type classification. Per-type overrides in cfg
// (TopNLookup, TopNEnumeration, TopNComplexReasoning) are sentinel-encoded:
// a value <= 0 means "inherit cfg.DefaultTopK". Empty / unknown query
// types always inherit. Pure function — no observability dependency,
// fully unit-testable.
func EffectiveTopN(cfg KBVectorConfig, queryType string) int {
	var override int
	switch queryType {
	case QueryTypeLookup:
		override = cfg.TopNLookup
	case QueryTypeEnumeration:
		override = cfg.TopNEnumeration
	case QueryTypeComplexReasoning:
		override = cfg.TopNComplexReasoning
	default:
		return cfg.DefaultTopK
	}
	if override <= 0 {
		return cfg.DefaultTopK
	}
	return override
}
