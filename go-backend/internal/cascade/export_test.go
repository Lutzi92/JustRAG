package cascade

import "context"

// SetBeforeVectorDeleteHook installs the before-vector-delete test seam and
// returns a restore func.
func SetBeforeVectorDeleteHook(h func(ctx context.Context, fileIDs []string)) func() {
	prev := beforeVectorDeleteHook
	beforeVectorDeleteHook = h
	return func() { beforeVectorDeleteHook = prev }
}
