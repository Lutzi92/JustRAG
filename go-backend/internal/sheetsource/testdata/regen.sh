#!/usr/bin/env bash
# Regenerates the committed fixtures. Needs LibreOffice (soffice) locally.
set -euo pipefail
cd "$(dirname "$0")"
rm -rf gen-out && mkdir gen-out
# NOTE: main.go carries "//go:build ignore" so it is invisible to
# `go build ./...` / `go vet ./...`. Because of that same build tag, `go run`
# on the *package directory* fails ("build constraints exclude all Go
# files") -- the file must be named explicitly, which bypasses the
# constraint filter for files given directly on the command line.
( cd ../../.. && go run ./internal/sheetsource/testdata/gen/main.go ./internal/sheetsource/testdata/gen-out )
# Files that need cached formula values or another container format go through LibreOffice.
soffice --headless --convert-to xlsx --outdir gen-out/lo gen-out/multirow_header_formulas.xlsx
soffice --headless --convert-to xlsx --outdir gen-out/lo gen-out/totals_rows.xlsx
soffice --headless --convert-to xls  --outdir gen-out/lo gen-out/multirow_header_formulas.xlsx
soffice --headless --convert-to ods  --outdir gen-out/lo gen-out/merged_for_ods.xlsx
cp gen-out/lo/multirow_header_formulas.xlsx multirow_header_formulas.xlsx
cp gen-out/lo/totals_rows.xlsx totals_rows.xlsx
cp gen-out/lo/multirow_header_formulas.xls formulas.xls
cp gen-out/lo/merged_for_ods.ods covered_cells.ods
for f in header_row14_metadata uncached_formulas steckbrief_form ids_leading_zero numbers_formats multi_region injection_cells; do cp "gen-out/$f.xlsx" "$f.xlsx"; done
cp gen-out/bom_semicolon_cp1252.csv gen-out/decimal_comma.csv .
rm -rf gen-out
echo "fixtures regenerated"
