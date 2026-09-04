package sheetsource

import (
	"bytes"
	"encoding/csv"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

func decodeCSVBytes(b []byte) ([]byte, string) {
	b = bytes.TrimPrefix(b, utf8BOM)
	if utf8.Valid(b) {
		return b, "utf-8"
	}
	out, err := charmap.Windows1252.NewDecoder().Bytes(b)
	if err != nil {
		return b, "utf-8"
	}
	return out, "windows-1252"
}

func sniffDelimiter(head []byte) rune {
	lines := strings.Split(string(head), "\n")
	var sample []string
	for _, l := range lines {
		if l = strings.TrimRight(l, "\r"); strings.TrimSpace(l) != "" {
			sample = append(sample, l)
		}
		if len(sample) == 20 {
			break
		}
	}
	if len(sample) == 0 {
		return ','
	}
	best, bestVar, bestMean := ',', -1.0, 0.0
	for _, cand := range []rune{',', ';', '\t', '|'} {
		counts := make([]float64, len(sample))
		ok := true
		for i, l := range sample {
			n, inQ := 0, false
			for _, r := range l {
				if r == '"' {
					inQ = !inQ
				} else if r == cand && !inQ {
					n++
				}
			}
			if n == 0 {
				ok = false
				break
			}
			counts[i] = float64(n)
		}
		if !ok {
			continue
		}
		mean := 0.0
		for _, c := range counts {
			mean += c
		}
		mean /= float64(len(counts))
		v := 0.0
		for _, c := range counts {
			v += (c - mean) * (c - mean)
		}
		if bestVar < 0 || v < bestVar || (v == bestVar && mean > bestMean) {
			best, bestVar, bestMean = cand, v, mean
		}
	}
	return best
}

type CSVSource struct {
	path  string
	delim rune
	enc   string // reflects only the 64 KiB sniff sample; ReadSheet re-decides on the full file
}

func OpenCSV(path string) (src *CSVSource, err error) {
	defer recoverToErr(&err, "OpenCSV")
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	head := make([]byte, 64*1024)
	n, _ := io.ReadFull(f, head)
	decoded, enc := decodeCSVBytes(head[:n])
	return &CSVSource{path: path, delim: sniffDelimiter(decoded), enc: enc}, nil
}

func (s *CSVSource) Close() error { return nil }

func (s *CSVSource) Sheets() []SheetInfo {
	name := strings.TrimSuffix(filepath.Base(s.path), filepath.Ext(s.path))
	return []SheetInfo{{Index: 0, Name: name}}
}

func (s *CSVSource) ReadSheet(index int, fn RowFunc) (ex SheetExtras, err error) {
	defer recoverToErr(&err, "CSVSource.ReadSheet")
	if index != 0 {
		return ex, errors.New("sheetsource: csv has one sheet")
	}
	raw, err := os.ReadFile(s.path) // CSVs ingested here are bounded by the upload limit; streaming decode is a Phase-2 follow-up
	if err != nil {
		return ex, err
	}
	decoded, _ := decodeCSVBytes(raw)
	r := csv.NewReader(bytes.NewReader(decoded))
	r.Comma = s.delim
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	r.TrimLeadingSpace = s.delim != '\t'
	lastRowIdx := -1
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return ex, err
		}
		line, _ := r.FieldPos(0) // 1-based line number
		physicalIdx := line - 1  // convert to 0-based
		// Deliver gap rows (nil) for skipped indices
		for i := lastRowIdx + 1; i < physicalIdx; i++ {
			if err := fn(i, nil); err != nil {
				if errors.Is(err, ErrStop) {
					ex.RowCount = i + 1
					return ex, nil
				}
				return ex, err
			}
		}
		// Deliver the current record
		cells := make([]Cell, len(rec))
		for j, v := range rec {
			cells[j] = Cell{Kind: KindText, Raw: v, Formatted: v}
			if strings.TrimSpace(v) == "" {
				cells[j].Kind = KindEmpty
			}
		}
		if len(cells) > ex.MaxCol {
			ex.MaxCol = len(cells)
		}
		if err := fn(physicalIdx, cells); err != nil {
			if errors.Is(err, ErrStop) {
				ex.RowCount = physicalIdx + 1
				return ex, nil
			}
			return ex, err
		}
		lastRowIdx = physicalIdx
		ex.RowCount = physicalIdx + 1
	}
	return ex, nil
}
