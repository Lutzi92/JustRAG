package sheetsource

import (
	"errors"
	"path/filepath"
	"strings"
)

var ErrUnsupported = errors.New("sheetsource: unsupported file type")

func Open(path, fileName string) (Source, error) {
	ext := strings.ToLower(filepath.Ext(fileName))
	if ext == "" {
		ext = strings.ToLower(filepath.Ext(path))
	}
	switch ext {
	case ".xlsx":
		return OpenXLSX(path)
	case ".xls":
		return OpenXLS(path)
	case ".ods":
		return OpenODS(path)
	case ".csv", ".tsv":
		return OpenCSV(path)
	}
	return nil, ErrUnsupported
}
