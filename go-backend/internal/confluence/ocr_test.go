package confluence

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeAttachmentFetcher struct {
	attachments []ConfluenceAttachment
	files       map[string][]byte // downloadLink -> bytes
	fetchErr    error
	downloadErr error
}

func (f *fakeAttachmentFetcher) GetPageAttachments(_ context.Context, _ string) ([]ConfluenceAttachment, error) {
	if f.fetchErr != nil {
		return nil, f.fetchErr
	}
	return f.attachments, nil
}

func (f *fakeAttachmentFetcher) DownloadAttachment(_ context.Context, link string) ([]byte, error) {
	if f.downloadErr != nil {
		return nil, f.downloadErr
	}
	data, ok := f.files[link]
	if !ok {
		return nil, errors.New("not found")
	}
	return data, nil
}

const sampleSVG = `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg">
<title>Architecture</title>
<text x="10" y="20">API Gateway</text>
<text x="100" y="20">PostgreSQL</text>
</svg>`

func TestProcessInlineImages_SVGExtractsLabels(t *testing.T) {
	fetcher := &fakeAttachmentFetcher{
		attachments: []ConfluenceAttachment{
			{Title: "diagram.svg", DownloadLink: "/svg-link"},
		},
		files: map[string][]byte{
			"/svg-link": []byte(sampleSVG),
		},
	}

	html := `<p>Before</p>
<ac:image><ri:attachment ri:filename="diagram.svg" /></ac:image>
<p>After</p>`

	got := processInlineImages(context.Background(), html, fetcher, "page-1")

	for _, want := range []string{"[Image: diagram.svg]", "Architecture", "API Gateway", "PostgreSQL"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in result\ngot: %s", want, got)
		}
	}
	if strings.Contains(got, "<ac:image") {
		t.Errorf("expected <ac:image> to be replaced\ngot: %s", got)
	}
}

func TestProcessInlineImages_SVGDownloadFailureStillEmitsPlaceholder(t *testing.T) {
	fetcher := &fakeAttachmentFetcher{
		attachments: []ConfluenceAttachment{
			{Title: "diagram.svg", DownloadLink: "/svg-link"},
		},
		downloadErr: errors.New("404"),
	}

	html := `<ac:image><ri:attachment ri:filename="diagram.svg" /></ac:image>`
	got := processInlineImages(context.Background(), html, fetcher, "page-1")

	if !strings.Contains(got, "[Image: diagram.svg]") {
		t.Errorf("expected placeholder to survive download failure\ngot: %s", got)
	}
}

func TestProcessInlineImages_SVGMissingFromAttachmentsUsesPlaceholder(t *testing.T) {
	// Attachment list doesn't include the referenced SVG — should still emit
	// a placeholder so the image's existence is recorded.
	fetcher := &fakeAttachmentFetcher{
		attachments: []ConfluenceAttachment{
			{Title: "other.png", DownloadLink: "/other"},
		},
	}

	html := `<ac:image><ri:attachment ri:filename="ghost.svg" /></ac:image>`
	got := processInlineImages(context.Background(), html, fetcher, "page-1")

	if !strings.Contains(got, "[Image: ghost.svg]") {
		t.Errorf("expected placeholder for missing SVG attachment\ngot: %s", got)
	}
}

func TestProcessInlineImages_UnknownExtensionGetsPlaceholder(t *testing.T) {
	// A .gif (or any non-SVG / non-OCR ext) should still get a placeholder so
	// the image isn't silently dropped by the downstream tag stripper.
	fetcher := &fakeAttachmentFetcher{}

	html := `<ac:image><ri:attachment ri:filename="funny.gif" /></ac:image>`
	got := processInlineImages(context.Background(), html, fetcher, "page-1")

	if !strings.Contains(got, "[Image: funny.gif]") {
		t.Errorf("expected placeholder for .gif\ngot: %s", got)
	}
}

func TestProcessInlineImages_NoImagesIsNoop(t *testing.T) {
	fetcher := &fakeAttachmentFetcher{}
	html := `<p>plain text with no images</p>`
	got := processInlineImages(context.Background(), html, fetcher, "page-1")
	if got != html {
		t.Errorf("expected html unchanged\ngot: %s", got)
	}
}

func TestProcessInlineImages_MultipleImagesAllReplaced(t *testing.T) {
	fetcher := &fakeAttachmentFetcher{
		attachments: []ConfluenceAttachment{
			{Title: "a.svg", DownloadLink: "/a"},
			{Title: "b.svg", DownloadLink: "/b"},
		},
		files: map[string][]byte{
			"/a": []byte(`<svg><text>first</text></svg>`),
			"/b": []byte(`<svg><text>second</text></svg>`),
		},
	}

	html := `<ac:image><ri:attachment ri:filename="a.svg" /></ac:image>
middle
<ac:image><ri:attachment ri:filename="b.svg" /></ac:image>`

	got := processInlineImages(context.Background(), html, fetcher, "page-1")

	for _, want := range []string{"[Image: a.svg]", "first", "[Image: b.svg]", "second", "middle"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in result\ngot: %s", want, got)
		}
	}
	if strings.Contains(got, "<ac:image") {
		t.Errorf("expected both <ac:image> tags to be replaced\ngot: %s", got)
	}
}
