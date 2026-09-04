package builtin

import (
	"regexp"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/tabular/render"
)

// TestTableQueryDescriptionTeachesTheRealMarker pins I3. The Phase-1
// renderer emitted one marker per row ("[tabular.<table> row <id>]"); the
// Phase-2 renderer emits one marker per BLOCK of rows with an inclusive
// range ("[tabular.<table> rows <a>–<b>]", en dash). The tool description is
// the only place a model learns to read that marker, so a stale description
// teaches it to filter on an id that no longer appears anywhere — and the
// two can only be kept honest by testing them against each other.
func TestTableQueryDescriptionTeachesTheRealMarker(t *testing.T) {
	tool := newTableQueryWithDeps(nil, nil, nil)
	desc := tool.Description

	if !strings.Contains(desc, "rows") {
		t.Error("description must name the plural `rows` marker, not the Phase-1 single-row one")
	}
	if !strings.Contains(desc, "BETWEEN") {
		t.Error("description must teach the range filter (WHERE _rowid BETWEEN a AND b)")
	}
	if regexp.MustCompile(`\[tabular\.<table> row <id>\]`).MatchString(desc) {
		t.Error("description still shows the Phase-1 single-id marker")
	}

	// The shape the description teaches, as a regexp — and a marker the
	// renderer would actually emit, which must match it.
	taught := regexp.MustCompile(`\[tabular\.<table> rows <a>–<b>\]`)
	if !taught.MatchString(desc) {
		t.Fatalf("description no longer shows the marker template:\n%s", desc)
	}
	real := render.MarkerPrefix("sheet_abc_0_0") + " 1–20]"
	shape := regexp.MustCompile(`^\[tabular\.[a-z0-9_]+ rows \d+–\d+\]$`)
	if !shape.MatchString(real) {
		t.Errorf("renderer marker %q does not have the shape the description teaches", real)
	}
	// And the render package's own prefix is literally the one in the text.
	if !strings.Contains(desc, "[tabular.") || !strings.Contains(render.MarkerPrefix("x"), "[tabular.") {
		t.Error("description and render.MarkerPrefix disagree on the marker prefix")
	}
}
