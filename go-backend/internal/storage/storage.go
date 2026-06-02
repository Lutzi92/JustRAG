package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// Storage abstracts over S3 and local filesystem storage.
type Storage interface {
	StoreFile(ctx context.Context, storagePath string, content []byte, contentType string) error
	StoreFileFromReader(ctx context.Context, storagePath string, reader io.Reader, contentType string) error
	ReadFile(ctx context.Context, storagePath string) ([]byte, error)
	ReadFileStream(ctx context.Context, storagePath string) (io.ReadCloser, error)
	DeleteFile(ctx context.Context, storagePath string) error
	// DeleteFiles removes the given paths. Empty paths are skipped. The S3
	// implementation batches into DeleteObjects calls (1000 keys per request);
	// the local implementation iterates. Errors from individual deletes are
	// joined into a single error so the caller can surface partial failures.
	DeleteFiles(ctx context.Context, storagePaths []string) error
	DeleteDirectory(ctx context.Context, prefix string) error
	FileExists(ctx context.Context, storagePath string) (bool, error)
	IsS3() bool
}

// Config holds storage configuration sourced from environment variables.
type Config struct {
	S3Endpoint  string // S3_ENDPOINT
	S3AccessKey string // S3_ACCESS_KEY
	S3SecretKey string // S3_SECRET_KEY
	S3Bucket    string // S3_BUCKET (default "rag-files")
	S3Region    string // S3_REGION (default "us-east-1")
	DataDir     string // local data directory (default "data")
}

// New returns either an S3 or local filesystem Storage depending on Config.
// If S3Endpoint is non-empty, an S3 storage is returned; otherwise local.
func New(cfg Config) (Storage, error) {
	if cfg.S3Bucket == "" {
		cfg.S3Bucket = "rag-files"
	}
	if cfg.S3Region == "" {
		cfg.S3Region = "us-east-1"
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "data"
	}

	if cfg.S3Endpoint != "" {
		return newS3Storage(cfg)
	}
	return newLocalStorage(cfg), nil
}

// GetStoragePath builds a canonical storage path from username, KB ID, and filename.
func GetStoragePath(username, kbID, filename string) string {
	return username + "/" + kbID + "/" + filename
}

// ---------------------------------------------------------------------------
// Local filesystem implementation
// ---------------------------------------------------------------------------

type localStorage struct {
	dataDir string
}

func newLocalStorage(cfg Config) *localStorage {
	return &localStorage{dataDir: cfg.DataDir}
}

func (l *localStorage) IsS3() bool { return false }

func (l *localStorage) localPath(storagePath string) (string, error) {
	p := filepath.Join(l.dataDir, storagePath)
	if err := assertWithinDataDir(l.dataDir, p); err != nil {
		return "", err
	}
	return p, nil
}

func (l *localStorage) StoreFile(ctx context.Context, storagePath string, content []byte, contentType string) error {
	p, err := l.localPath(storagePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("storage: create directories: %w", err)
	}
	return os.WriteFile(p, content, 0o644)
}

func (l *localStorage) StoreFileFromReader(ctx context.Context, storagePath string, reader io.Reader, contentType string) error {
	p, err := l.localPath(storagePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("storage: create directories: %w", err)
	}
	f, err := os.Create(p)
	if err != nil {
		return fmt.Errorf("storage: create file: %w", err)
	}
	defer f.Close()
	// Wrap the reader so the copy is cancelled when the context is done.
	ctxReader := newContextReader(ctx, reader)
	if _, err := io.Copy(f, ctxReader); err != nil {
		return fmt.Errorf("storage: write file: %w", err)
	}
	return nil
}

func (l *localStorage) ReadFile(ctx context.Context, storagePath string) ([]byte, error) {
	p, err := l.localPath(storagePath)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("storage: read file: %w", err)
	}
	return data, nil
}

func (l *localStorage) ReadFileStream(ctx context.Context, storagePath string) (io.ReadCloser, error) {
	p, err := l.localPath(storagePath)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, fmt.Errorf("storage: open file: %w", err)
	}
	return f, nil
}

func (l *localStorage) DeleteFile(ctx context.Context, storagePath string) error {
	p, err := l.localPath(storagePath)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil {
		return fmt.Errorf("storage: delete file: %w", err)
	}
	return nil
}

func (l *localStorage) DeleteFiles(ctx context.Context, storagePaths []string) error {
	var errs []error
	for _, p := range storagePaths {
		if p == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		if err := l.DeleteFile(ctx, p); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (l *localStorage) DeleteDirectory(ctx context.Context, prefix string) error {
	p, err := l.localPath(prefix)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(p); err != nil {
		return fmt.Errorf("storage: delete directory: %w", err)
	}
	return nil
}

func (l *localStorage) FileExists(ctx context.Context, storagePath string) (bool, error) {
	p, err := l.localPath(storagePath)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(p)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("storage: stat file: %w", err)
}

// contextReader wraps an io.Reader and checks for context cancellation on
// every Read call, allowing long file copies to be interrupted.
type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func newContextReader(ctx context.Context, r io.Reader) *contextReader {
	return &contextReader{ctx: ctx, r: r}
}

func (cr *contextReader) Read(p []byte) (int, error) {
	if err := cr.ctx.Err(); err != nil {
		return 0, err
	}
	return cr.r.Read(p)
}

// assertWithinDataDir ensures the resolved localPath is within dataDir to
// prevent path traversal attacks.
func assertWithinDataDir(dataDir, localPath string) error {
	resolved, err := filepath.Abs(localPath)
	if err != nil {
		return fmt.Errorf("storage: resolve path: %w", err)
	}
	base, err := filepath.Abs(dataDir)
	if err != nil {
		return fmt.Errorf("storage: resolve data dir: %w", err)
	}
	if !strings.HasPrefix(resolved, base+string(filepath.Separator)) && resolved != base {
		return fmt.Errorf("storage: path traversal attempt detected")
	}
	return nil
}

// ---------------------------------------------------------------------------
// S3 implementation
// ---------------------------------------------------------------------------

type s3Storage struct {
	client *s3.Client
	bucket string
}

func newS3Storage(cfg Config) (*s3Storage, error) {
	customResolver := aws.EndpointResolverWithOptionsFunc(
		func(service, region string, options ...interface{}) (aws.Endpoint, error) {
			return aws.Endpoint{
				URL:               cfg.S3Endpoint,
				SigningRegion:     cfg.S3Region,
				HostnameImmutable: true,
			}, nil
		},
	)

	awsCfg, err := awsconfig.LoadDefaultConfig(
		context.Background(),
		awsconfig.WithRegion(cfg.S3Region),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.S3AccessKey, cfg.S3SecretKey, ""),
		),
		awsconfig.WithEndpointResolverWithOptions(customResolver), //nolint:staticcheck
	)
	if err != nil {
		return nil, fmt.Errorf("storage: load AWS config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.UsePathStyle = true
	})

	s := &s3Storage{client: client, bucket: cfg.S3Bucket}
	// context.Background() here and above is intentional: newS3Storage runs
	// once at process startup (from New), not inside a request or worker task,
	// so there is no parent context to cancel.
	if err := ensureBucket(context.Background(), s.client, s.bucket, cfg.S3Region); err != nil {
		return nil, fmt.Errorf("storage: ensure bucket %q: %w", cfg.S3Bucket, err)
	}
	return s, nil
}

// bucketEnsurer is the subset of the S3 client used by ensureBucket, extracted
// as an interface so the startup preflight logic can be unit-tested without a
// live S3 endpoint.
type bucketEnsurer interface {
	HeadBucket(ctx context.Context, params *s3.HeadBucketInput, optFns ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
	CreateBucket(ctx context.Context, params *s3.CreateBucketInput, optFns ...func(*s3.Options)) (*s3.CreateBucketOutput, error)
}

// ensureBucket creates the configured bucket if it doesn't exist. Required for
// fresh installs against a bundled MinIO (or any self-managed S3) where no
// out-of-band IaC has provisioned the bucket. Idempotent: HeadBucket short-
// circuits when the bucket already exists, and BucketAlreadyOwnedByYou /
// BucketAlreadyExists are treated as success to tolerate concurrent boots of
// the server and worker.
//
// The preflight fails OPEN on any non-404 HeadBucket error (e.g. a 403). A 403
// means the bucket is reachable but the credentials can't introspect it — most
// often a least-privilege key that holds object permissions (Get/Put/Delete)
// but not the bucket-level s3:ListBucket that HeadBucket requires. In that case
// the worker's actual job is fine, so crash-looping the process at startup is
// the wrong call; we log a warning and let the first real object operation
// surface any genuine permission problem. (HeadBucket is also an HTTP HEAD with
// no response body, so a 403 carries no S3 error code to inspect anyway.)
// Only a definitive 404/NotFound triggers bucket creation.
func ensureBucket(ctx context.Context, api bucketEnsurer, bucket, region string) error {
	_, err := api.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})
	if err == nil {
		return nil
	}

	if !isNotFoundErr(err) {
		slog.Warn("S3 bucket preflight (HeadBucket) failed; assuming the bucket is provisioned out of band and continuing",
			"bucket", bucket, "error", err)
		return nil
	}

	slog.Info("creating missing S3 bucket", "bucket", bucket, "region", region)
	input := &s3.CreateBucketInput{Bucket: aws.String(bucket)}
	// us-east-1 is the SDK default and rejects an explicit LocationConstraint;
	// MinIO accepts either form, so we only set the constraint for other regions.
	if region != "" && region != "us-east-1" {
		input.CreateBucketConfiguration = &types.CreateBucketConfiguration{
			LocationConstraint: types.BucketLocationConstraint(region),
		}
	}
	if _, err := api.CreateBucket(ctx, input); err != nil {
		if isAlreadyExistsErr(err) {
			return nil
		}
		return fmt.Errorf("create bucket: %w", err)
	}
	return nil
}

func isNotFoundErr(err error) bool {
	var notFound *types.NotFound
	if errors.As(err, &notFound) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NotFound", "NoSuchBucket", "404":
			return true
		}
	}
	return false
}

func isAlreadyExistsErr(err error) bool {
	var owned *types.BucketAlreadyOwnedByYou
	if errors.As(err, &owned) {
		return true
	}
	var exists *types.BucketAlreadyExists
	if errors.As(err, &exists) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "BucketAlreadyOwnedByYou", "BucketAlreadyExists":
			return true
		}
	}
	return false
}

func (s *s3Storage) IsS3() bool { return true }

func (s *s3Storage) StoreFile(ctx context.Context, storagePath string, content []byte, contentType string) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(storagePath),
		Body:        bytes.NewReader(content),
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return fmt.Errorf("storage: s3 put object: %w", err)
	}
	return nil
}

func (s *s3Storage) StoreFileFromReader(ctx context.Context, storagePath string, reader io.Reader, contentType string) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(storagePath),
		Body:        reader,
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return fmt.Errorf("storage: s3 put object from reader: %w", err)
	}
	return nil
}

func (s *s3Storage) ReadFile(ctx context.Context, storagePath string) ([]byte, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(storagePath),
	})
	if err != nil {
		return nil, fmt.Errorf("storage: s3 get object: %w", err)
	}
	defer out.Body.Close()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, fmt.Errorf("storage: s3 read body: %w", err)
	}
	return data, nil
}

func (s *s3Storage) ReadFileStream(ctx context.Context, storagePath string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(storagePath),
	})
	if err != nil {
		return nil, fmt.Errorf("storage: s3 get object stream: %w", err)
	}
	return out.Body, nil
}

func (s *s3Storage) DeleteFile(ctx context.Context, storagePath string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(storagePath),
	})
	if err != nil {
		return fmt.Errorf("storage: s3 delete object: %w", err)
	}
	return nil
}

func (s *s3Storage) DeleteFiles(ctx context.Context, storagePaths []string) error {
	keys := make([]types.ObjectIdentifier, 0, len(storagePaths))
	for _, p := range storagePaths {
		if p == "" {
			continue
		}
		keys = append(keys, types.ObjectIdentifier{Key: aws.String(p)})
	}
	if len(keys) == 0 {
		return nil
	}

	const batchSize = 1000 // S3 DeleteObjects hard limit
	var errs []error
	for i := 0; i < len(keys); i += batchSize {
		end := min(i+batchSize, len(keys))
		_, err := s.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(s.bucket),
			Delete: &types.Delete{
				Objects: keys[i:end],
				Quiet:   aws.Bool(true),
			},
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("storage: s3 delete objects: %w", err))
		}
	}
	return errors.Join(errs...)
}

func (s *s3Storage) DeleteDirectory(ctx context.Context, prefix string) error {
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(prefix),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("storage: s3 list objects: %w", err)
		}
		if len(page.Contents) == 0 {
			continue
		}

		objectIDs := make([]types.ObjectIdentifier, 0, len(page.Contents))
		for _, obj := range page.Contents {
			objectIDs = append(objectIDs, types.ObjectIdentifier{Key: obj.Key})
		}

		_, err = s.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(s.bucket),
			Delete: &types.Delete{
				Objects: objectIDs,
				Quiet:   aws.Bool(true),
			},
		})
		if err != nil {
			return fmt.Errorf("storage: s3 delete objects: %w", err)
		}
	}
	return nil
}

func (s *s3Storage) FileExists(ctx context.Context, storagePath string) (bool, error) {
	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(storagePath),
	})
	if err == nil {
		return true, nil
	}

	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		if apiErr.ErrorCode() == "NotFound" || apiErr.ErrorCode() == "NoSuchKey" {
			return false, nil
		}
	}
	// HeadObject returns a generic 404 response type
	var notFound *types.NotFound
	if errors.As(err, &notFound) {
		return false, nil
	}

	return false, fmt.Errorf("storage: s3 head object: %w", err)
}
