package files

// SetMaxUploadSizeForTest overrides the transport cap for the duration of a
// test and returns a function that restores the previous value. Test-only —
// kept out of http.go (and thus out of the shipped API surface) via Go's
// export_test.go convention: this file is compiled only for `go test`, so
// files.SetMaxUploadSizeForTest is reachable from the external files_test
// package in tests but does not exist in a production build.
func SetMaxUploadSizeForTest(n int64) (restore func()) {
	old := maxUploadSize
	maxUploadSize = n
	return func() { maxUploadSize = old }
}
