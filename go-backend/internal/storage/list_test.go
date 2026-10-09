package storage

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

func TestLocalListNestedWithMeta(t *testing.T) {
	s := newTestLocalStorage(t)
	ctx := context.Background()
	for _, k := range []string{"users/u1/a", "users/u1/parses/x/b.txt", "users/u2/c", "other/d"} {
		if err := s.StoreFile(ctx, k, []byte("12345"), "text/plain"); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.List(ctx, "users/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	keys := make([]string, 0, len(got))
	for _, o := range got {
		keys = append(keys, o.Key)
		if o.Size != 5 || o.ModTime.IsZero() || time.Since(o.ModTime) > time.Minute {
			t.Errorf("bad meta for %s: %+v", o.Key, o)
		}
	}
	sort.Strings(keys)
	want := []string{"users/u1/a", "users/u1/parses/x/b.txt", "users/u2/c"}
	if fmt.Sprint(keys) != fmt.Sprint(want) {
		t.Errorf("keys = %v, want %v", keys, want)
	}
}

func TestLocalListPartialPrefixMatchesS3Semantics(t *testing.T) {
	s := newTestLocalStorage(t)
	ctx := context.Background()
	for _, k := range []string{"users/u1/abc", "users/u1/abd", "users/u1/xyz"} {
		_ = s.StoreFile(ctx, k, []byte("x"), "")
	}
	got, err := s.List(ctx, "users/u1/ab")
	if err != nil || len(got) != 2 {
		t.Fatalf("got %v err %v, want 2 entries", got, err)
	}
}

func TestLocalListMissingPrefix(t *testing.T) {
	s := newTestLocalStorage(t)
	got, err := s.List(context.Background(), "users/")
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v err %v, want empty/nil", got, err)
	}
}

func TestLocalListTraversalRefused(t *testing.T) {
	s := newTestLocalStorage(t)
	if _, err := s.List(context.Background(), "../"); err == nil {
		t.Fatal("expected traversal error")
	}
	if _, err := s.List(context.Background(), "../etc"); err == nil {
		t.Fatal("expected traversal error")
	}
}

type fakeLister struct{ calls int }

func (f *fakeLister) ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	f.calls++
	page := 0
	if in.ContinuationToken != nil {
		page = 1
	}
	out := &s3.ListObjectsV2Output{IsTruncated: aws.Bool(page == 0)}
	if page == 0 {
		out.NextContinuationToken = aws.String("t1")
	}
	for i := 0; i < 1000; i++ {
		out.Contents = append(out.Contents, types.Object{
			Key:          aws.String(fmt.Sprintf("users/p%d/%04d", page, i)),
			Size:         aws.Int64(int64(i)),
			LastModified: aws.Time(time.Unix(1000, 0)),
		})
	}
	return out, nil
}

func TestListS3ObjectsPaginates(t *testing.T) {
	f := &fakeLister{}
	got, err := listS3Objects(context.Background(), f, "b", "users/")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2000 || f.calls != 2 {
		t.Fatalf("len=%d calls=%d, want 2000/2", len(got), f.calls)
	}
	if got[1500].Size != 500 || !got[1500].ModTime.Equal(time.Unix(1000, 0)) {
		t.Errorf("meta wrong: %+v", got[1500])
	}
}
