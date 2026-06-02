package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDescribeImage_SendsVisionRequest(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "a red square"}}},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-key")
	out, err := DescribeImage(context.Background(), c, "vision-model",
		"data:image/png;base64,AAAA", "Describe the image.")
	if err != nil {
		t.Fatal(err)
	}
	if out != "a red square" {
		t.Fatalf("got %q", out)
	}
	if !strings.Contains(gotBody, `"image_url"`) || !strings.Contains(gotBody, `"vision-model"`) {
		t.Fatalf("request did not carry image_url + model: %s", gotBody)
	}
}

func TestDescribeImage_EmptyModel(t *testing.T) {
	c := NewClient("http://localhost", "key")
	_, err := DescribeImage(context.Background(), c, "", "data:image/png;base64,AAAA", "")
	if err == nil {
		t.Fatal("expected error for empty model")
	}
}

func TestDescribeImage_EmptyImage(t *testing.T) {
	c := NewClient("http://localhost", "key")
	_, err := DescribeImage(context.Background(), c, "vision-model", "", "")
	if err == nil {
		t.Fatal("expected error for empty image")
	}
}

func TestDescribeImage_DefaultPrompt(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "a cat"}}},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-key")
	out, err := DescribeImage(context.Background(), c, "vision-model",
		"data:image/png;base64,AAAA", "")
	if err != nil {
		t.Fatal(err)
	}
	if out != "a cat" {
		t.Fatalf("got %q", out)
	}
	if !strings.Contains(gotBody, "Describe this image") {
		t.Fatalf("default prompt not sent: %s", gotBody)
	}
}
