//go:build ignore

// fixture-dump prints every sheet's rows of a spreadsheet fixture via
// sheetsource.Open + ReadSheet, so golden-set authors can read exact
// expected values instead of guessing them.
//
// Usage (must run from inside the go-backend module; this file carries
// //go:build ignore so `go run ./cmd/fixture-dump` — the package-directory
// form — fails with "build constraints exclude all Go files"; name the
// file directly instead):
//
//	cd go-backend && go run ./cmd/fixture-dump/main.go internal/sheetsource/testdata/<file>
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/justrag/go-backend/internal/sheetsource"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: go run ./cmd/fixture-dump/main.go <path-to-fixture>")
		os.Exit(1)
	}
	path := os.Args[1]
	fileName := filepath.Base(path)

	src, err := sheetsource.Open(path, fileName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open %s: %v\n", path, err)
		os.Exit(1)
	}
	defer src.Close()

	fmt.Printf("=== %s ===\n", fileName)
	for _, sheet := range src.Sheets() {
		fmt.Printf("\n--- sheet %d: %q (hidden=%v, declaredRowCount=%d) ---\n",
			sheet.Index, sheet.Name, sheet.Hidden, sheet.RowCount)

		extras, err := src.ReadSheet(sheet.Index, func(rowIdx int, cells []sheetsource.Cell) error {
			if cells == nil {
				fmt.Printf("row %3d: <gap>\n", rowIdx)
				return nil
			}
			fmt.Printf("row %3d:", rowIdx)
			for col, c := range cells {
				colName := sheetsource.ColumnName(col)
				if c.IsEmpty() && !c.IsFormula {
					continue
				}
				extra := ""
				if c.IsFormula {
					extra = fmt.Sprintf(" formula=%q", c.Formula)
				}
				fmt.Printf(" [%s]=%q(kind=%d,fmt=%q)%s", colName, c.Raw, c.Kind, c.Formatted, extra)
			}
			fmt.Println()
			return nil
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "read sheet %d (%s): %v\n", sheet.Index, sheet.Name, err)
			continue
		}
		fmt.Printf("extras: merged=%v maxCol=%d rowCount=%d\n", extras.Merged, extras.MaxCol, extras.RowCount)
		if len(extras.Validations) > 0 {
			fmt.Printf("validations:\n")
			for _, v := range extras.Validations {
				fmt.Printf("  sqref=%v ref=%q values=%v\n", v.Sqref, v.Ref, v.Values)
			}
		}
	}
}
