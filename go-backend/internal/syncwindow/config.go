package syncwindow

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// Site config keys. Global only — there is no per-KB override: the window
// exists to protect one shared worker pool, so one KB cannot opt out of it.
const (
	KeyStartHour = "sync_window_start_hour"
	KeyEndHour   = "sync_window_end_hour"
	KeyTimezone  = "sync_window_timezone"
)

const (
	defaultStartHour = 1
	defaultEndHour   = 5
	defaultTimezone  = "Europe/Berlin"
)

// SiteConfigReader is the minimal site_config interface this package needs.
type SiteConfigReader interface {
	GetSiteConfigValue(ctx context.Context, key string) (*string, error)
}

// WindowFrom reads the night window from site config. Every failure mode —
// nil reader, read error, missing key, unparseable or out-of-range value,
// unknown timezone — falls back to the default rather than erroring: a
// misconfigured window must not stop scheduled syncs entirely.
func WindowFrom(ctx context.Context, r SiteConfigReader) Window {
	return Window{
		StartHour: readHour(ctx, r, KeyStartHour, defaultStartHour),
		EndHour:   readHour(ctx, r, KeyEndHour, defaultEndHour),
		Loc:       readLocation(ctx, r),
	}
}

func readHour(ctx context.Context, r SiteConfigReader, key string, def int) int {
	raw := readString(ctx, r, key)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 || n > 23 {
		slog.Warn("invalid sync window hour, using default", "key", key, "value", raw, "default", def)
		return def
	}
	return n
}

func readLocation(ctx context.Context, r SiteConfigReader) *time.Location {
	name := readString(ctx, r, KeyTimezone)
	if name == "" {
		name = defaultTimezone
	}
	loc, err := time.LoadLocation(name)
	if err == nil {
		return loc
	}
	slog.Warn("unknown sync window timezone, falling back", "value", name, "fallback", defaultTimezone)
	if loc, err = time.LoadLocation(defaultTimezone); err == nil {
		return loc
	}
	// No tzdata at all — cmd/server and cmd/worker embed it, so this only
	// happens in an oddly built binary. UTC keeps scheduling working.
	slog.Warn("tzdata unavailable, scheduling in UTC")
	return time.UTC
}

func readString(ctx context.Context, r SiteConfigReader, key string) string {
	if r == nil {
		return ""
	}
	v, err := r.GetSiteConfigValue(ctx, key)
	if err != nil || v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}
