package syncwindow

import (
	"context"
	"errors"
	"testing"
)

type fakeReader struct {
	values map[string]string
	err    error
}

func (f *fakeReader) GetSiteConfigValue(_ context.Context, key string) (*string, error) {
	if f.err != nil {
		return nil, f.err
	}
	v, ok := f.values[key]
	if !ok {
		return nil, nil
	}
	return &v, nil
}

func TestWindowFrom_Defaults(t *testing.T) {
	w := WindowFrom(context.Background(), &fakeReader{values: map[string]string{}})
	if w.StartHour != 1 || w.EndHour != 5 {
		t.Fatalf("expected 1-5, got %d-%d", w.StartHour, w.EndHour)
	}
	if w.Loc == nil || w.Loc.String() != "Europe/Berlin" {
		t.Fatalf("expected Europe/Berlin, got %v", w.Loc)
	}
}

func TestWindowFrom_NilReaderYieldsDefaults(t *testing.T) {
	w := WindowFrom(context.Background(), nil)
	if w.StartHour != 1 || w.EndHour != 5 || w.Loc == nil {
		t.Fatalf("nil reader must yield defaults, got %+v", w)
	}
}

func TestWindowFrom_ReadsConfiguredValues(t *testing.T) {
	w := WindowFrom(context.Background(), &fakeReader{values: map[string]string{
		"sync_window_start_hour": "22",
		"sync_window_end_hour":   "4",
		"sync_window_timezone":   "UTC",
	}})
	if w.StartHour != 22 || w.EndHour != 4 {
		t.Fatalf("expected 22-4, got %d-%d", w.StartHour, w.EndHour)
	}
	if w.Loc.String() != "UTC" {
		t.Fatalf("expected UTC, got %v", w.Loc)
	}
}

func TestWindowFrom_RejectsOutOfRangeAndUnparseableHours(t *testing.T) {
	w := WindowFrom(context.Background(), &fakeReader{values: map[string]string{
		"sync_window_start_hour": "24",
		"sync_window_end_hour":   "abc",
	}})
	if w.StartHour != 1 || w.EndHour != 5 {
		t.Fatalf("invalid hours must fall back to 1-5, got %d-%d", w.StartHour, w.EndHour)
	}
}

func TestWindowFrom_UnknownTimezoneFallsBack(t *testing.T) {
	w := WindowFrom(context.Background(), &fakeReader{values: map[string]string{
		"sync_window_timezone": "Not/AZone",
	}})
	if w.Loc == nil || w.Loc.String() == "Not/AZone" {
		t.Fatalf("unknown timezone must fall back, got %v", w.Loc)
	}
}

func TestWindowFrom_ReaderErrorYieldsDefaults(t *testing.T) {
	w := WindowFrom(context.Background(), &fakeReader{err: errors.New("db down")})
	if w.StartHour != 1 || w.EndHour != 5 {
		t.Fatalf("reader error must yield defaults, got %d-%d", w.StartHour, w.EndHour)
	}
}
