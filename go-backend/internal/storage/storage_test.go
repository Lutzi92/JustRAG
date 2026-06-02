package storage

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// fakeBucketAPI is a stand-in for the S3 client used by ensureBucket, so the
// startup preflight can be exercised without a live endpoint.
type fakeBucketAPI struct {
	headErr      error
	createErr    error
	createCalled bool
}

func (f *fakeBucketAPI) HeadBucket(_ context.Context, _ *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	return &s3.HeadBucketOutput{}, f.headErr
}

func (f *fakeBucketAPI) CreateBucket(_ context.Context, _ *s3.CreateBucketInput, _ ...func(*s3.Options)) (*s3.CreateBucketOutput, error) {
	f.createCalled = true
	return &s3.CreateBucketOutput{}, f.createErr
}

func TestEnsureBucketHeadOK(t *testing.T) {
	api := &fakeBucketAPI{}
	if err := ensureBucket(context.Background(), api, "rag-files", "us-east-1"); err != nil {
		t.Fatalf("ensureBucket: %v", err)
	}
	if api.createCalled {
		t.Error("CreateBucket called when bucket already exists")
	}
}

func TestEnsureBucketNotFoundCreates(t *testing.T) {
	api := &fakeBucketAPI{headErr: &types.NotFound{}}
	if err := ensureBucket(context.Background(), api, "rag-files", "eu-central-1"); err != nil {
		t.Fatalf("ensureBucket: %v", err)
	}
	if !api.createCalled {
		t.Error("CreateBucket not called for a genuinely missing (404) bucket")
	}
}

// A 403 from HeadBucket means the bucket is reachable but the credentials can't
// introspect it (e.g. a least-privilege key without s3:ListBucket). The worker's
// real job — object read/write — may be fine, so the startup preflight must fail
// open rather than crash-loop the process.
func TestEnsureBucketForbiddenFailsOpen(t *testing.T) {
	api := &fakeBucketAPI{headErr: &smithy.GenericAPIError{Code: "AccessDenied", Message: "forbidden"}}
	if err := ensureBucket(context.Background(), api, "rag-files", "us-east-1"); err != nil {
		t.Fatalf("ensureBucket should fail open on 403, got: %v", err)
	}
	if api.createCalled {
		t.Error("CreateBucket must not be called on a 403 (bucket is not missing)")
	}
}

// HeadBucket is an HTTP HEAD request, so a 403 response carries no body and thus
// no parseable S3 error code. ensureBucket must still fail open in that case.
func TestEnsureBucketForbiddenNoCodeFailsOpen(t *testing.T) {
	api := &fakeBucketAPI{headErr: errors.New("https response error StatusCode: 403, RequestID: ABC")}
	if err := ensureBucket(context.Background(), api, "rag-files", "us-east-1"); err != nil {
		t.Fatalf("ensureBucket should fail open on a code-less 403, got: %v", err)
	}
	if api.createCalled {
		t.Error("CreateBucket must not be called on a code-less 403")
	}
}

// A fresh install against bundled MinIO: HeadBucket says 404, but CreateBucket
// races with the server's concurrent boot and reports the bucket already owned.
// That must still succeed.
func TestEnsureBucketCreateAlreadyOwnedSucceeds(t *testing.T) {
	api := &fakeBucketAPI{headErr: &types.NotFound{}, createErr: &types.BucketAlreadyOwnedByYou{}}
	if err := ensureBucket(context.Background(), api, "rag-files", "us-east-1"); err != nil {
		t.Fatalf("ensureBucket: %v", err)
	}
}

// A genuine CreateBucket failure on a missing bucket is still fatal — we can't
// store anything if creation truly fails.
func TestEnsureBucketCreateFailsIsFatal(t *testing.T) {
	api := &fakeBucketAPI{headErr: &types.NotFound{}, createErr: errors.New("boom")}
	if err := ensureBucket(context.Background(), api, "rag-files", "us-east-1"); err == nil {
		t.Fatal("ensureBucket: expected error when CreateBucket fails, got nil")
	}
}

func newTestLocalStorage(t *testing.T) *localStorage {
	t.Helper()
	return &localStorage{dataDir: t.TempDir()}
}

func TestStoreAndReadFile(t *testing.T) {
	s := newTestLocalStorage(t)
	ctx := context.Background()

	content := []byte("hello, storage!")
	path := "user1/kb1/test.txt"

	if err := s.StoreFile(ctx, path, content, "text/plain"); err != nil {
		t.Fatalf("StoreFile: %v", err)
	}

	got, err := s.ReadFile(ctx, path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	if string(got) != string(content) {
		t.Errorf("ReadFile content mismatch: got %q, want %q", got, content)
	}
}

func TestStoreFileFromReader(t *testing.T) {
	s := newTestLocalStorage(t)
	ctx := context.Background()

	content := "hello from reader"
	path := "user1/kb1/reader.txt"

	if err := s.StoreFileFromReader(ctx, path, strings.NewReader(content), "text/plain"); err != nil {
		t.Fatalf("StoreFileFromReader: %v", err)
	}

	got, err := s.ReadFile(ctx, path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	if string(got) != content {
		t.Errorf("content mismatch: got %q, want %q", got, content)
	}
}

func TestReadFileStream(t *testing.T) {
	s := newTestLocalStorage(t)
	ctx := context.Background()

	content := []byte("stream content")
	path := "user1/kb1/stream.txt"

	if err := s.StoreFile(ctx, path, content, "application/octet-stream"); err != nil {
		t.Fatalf("StoreFile: %v", err)
	}

	rc, err := s.ReadFileStream(ctx, path)
	if err != nil {
		t.Fatalf("ReadFileStream: %v", err)
	}
	defer rc.Close()

	buf := make([]byte, len(content))
	n, err := rc.Read(buf)
	if err != nil && err.Error() != "EOF" {
		t.Fatalf("Read from stream: %v", err)
	}
	if string(buf[:n]) != string(content) {
		t.Errorf("stream content mismatch: got %q, want %q", buf[:n], content)
	}
}

func TestFileExists(t *testing.T) {
	s := newTestLocalStorage(t)
	ctx := context.Background()

	path := "user1/kb1/exists.txt"

	exists, err := s.FileExists(ctx, path)
	if err != nil {
		t.Fatalf("FileExists (before create): %v", err)
	}
	if exists {
		t.Error("FileExists returned true for non-existent file")
	}

	if err := s.StoreFile(ctx, path, []byte("data"), "text/plain"); err != nil {
		t.Fatalf("StoreFile: %v", err)
	}

	exists, err = s.FileExists(ctx, path)
	if err != nil {
		t.Fatalf("FileExists (after create): %v", err)
	}
	if !exists {
		t.Error("FileExists returned false for existing file")
	}
}

func TestDeleteFile(t *testing.T) {
	s := newTestLocalStorage(t)
	ctx := context.Background()

	path := "user1/kb1/delete-me.txt"

	if err := s.StoreFile(ctx, path, []byte("bye"), "text/plain"); err != nil {
		t.Fatalf("StoreFile: %v", err)
	}

	if err := s.DeleteFile(ctx, path); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}

	exists, err := s.FileExists(ctx, path)
	if err != nil {
		t.Fatalf("FileExists after delete: %v", err)
	}
	if exists {
		t.Error("file still exists after DeleteFile")
	}
}

func TestDeleteFiles(t *testing.T) {
	s := newTestLocalStorage(t)
	ctx := context.Background()

	paths := []string{
		"user1/kb1/a.txt",
		"user1/kb1/b.txt",
		"user1/kb1/sub/c.txt",
	}
	for _, p := range paths {
		if err := s.StoreFile(ctx, p, []byte("x"), "text/plain"); err != nil {
			t.Fatalf("StoreFile %s: %v", p, err)
		}
	}

	// Mix in an empty path (should be skipped, not raise an error).
	if err := s.DeleteFiles(ctx, append([]string{""}, paths...)); err != nil {
		t.Fatalf("DeleteFiles: %v", err)
	}

	for _, p := range paths {
		exists, err := s.FileExists(ctx, p)
		if err != nil {
			t.Fatalf("FileExists %s: %v", p, err)
		}
		if exists {
			t.Errorf("file %s still exists after DeleteFiles", p)
		}
	}

	// Nil / empty slice is a no-op.
	if err := s.DeleteFiles(ctx, nil); err != nil {
		t.Errorf("DeleteFiles(nil): %v", err)
	}

	// Missing path: surfaces an error (joined) but doesn't panic.
	if err := s.DeleteFiles(ctx, []string{"user1/kb1/does-not-exist.txt"}); err == nil {
		t.Errorf("DeleteFiles on missing path: expected error, got nil")
	}
}

func TestDeleteDirectory(t *testing.T) {
	s := newTestLocalStorage(t)
	ctx := context.Background()

	files := []string{
		"user1/kb1/a.txt",
		"user1/kb1/b.txt",
		"user1/kb1/sub/c.txt",
	}
	for _, f := range files {
		if err := s.StoreFile(ctx, f, []byte("x"), "text/plain"); err != nil {
			t.Fatalf("StoreFile %s: %v", f, err)
		}
	}

	if err := s.DeleteDirectory(ctx, "user1/kb1"); err != nil {
		t.Fatalf("DeleteDirectory: %v", err)
	}

	for _, f := range files {
		exists, err := s.FileExists(ctx, f)
		if err != nil {
			t.Fatalf("FileExists %s: %v", f, err)
		}
		if exists {
			t.Errorf("file %s still exists after DeleteDirectory", f)
		}
	}
}

func TestPathTraversalRejected(t *testing.T) {
	s := newTestLocalStorage(t)
	ctx := context.Background()

	maliciousPaths := []string{
		"../outside.txt",
		"../../etc/passwd",
		"user1/../../outside.txt",
	}

	for _, path := range maliciousPaths {
		err := s.StoreFile(ctx, path, []byte("evil"), "text/plain")
		if err == nil {
			t.Errorf("StoreFile(%q): expected path traversal error, got nil", path)
		}
	}
}

func TestGetStoragePath(t *testing.T) {
	got := GetStoragePath("alice", "kb-123", "document.pdf")
	want := "alice/kb-123/document.pdf"
	if got != want {
		t.Errorf("GetStoragePath: got %q, want %q", got, want)
	}
}

func TestIsS3False(t *testing.T) {
	s := newTestLocalStorage(t)
	if s.IsS3() {
		t.Error("IsS3() should return false for local storage")
	}
}

func TestNewLocalStorage(t *testing.T) {
	cfg := Config{DataDir: t.TempDir()}
	st, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if st.IsS3() {
		t.Error("expected local storage, got S3")
	}
}

func TestNewDefaultsApplied(t *testing.T) {
	// Verify that empty Config gets defaults (no S3 endpoint -> local).
	cfg := Config{DataDir: t.TempDir()}
	st, err := New(cfg)
	if err != nil {
		t.Fatalf("New with empty config: %v", err)
	}
	if st == nil {
		t.Fatal("New returned nil storage")
	}
}
