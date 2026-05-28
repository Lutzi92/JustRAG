package parser

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTexParserCanParse(t *testing.T) {
	t.Parallel()
	p := &TexParser{}

	if !p.CanParse("", "document.tex") {
		t.Error("expected true for .tex extension")
	}
	if !p.CanParse("application/x-tex", "file") {
		t.Error("expected true for application/x-tex")
	}
	if !p.CanParse("text/x-tex", "file") {
		t.Error("expected true for text/x-tex")
	}
	if p.CanParse("application/pdf", "document.pdf") {
		t.Error("expected false for .pdf")
	}
}

func TestTexParserStripPercentComments(t *testing.T) {
	t.Parallel()
	p := &TexParser{}
	dir := t.TempDir()
	fp := filepath.Join(dir, "test.tex")

	content := "% this is a comment\n\\section{Hello}\n  % indented comment\nWorld\n"
	if err := os.WriteFile(fp, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	res, err := p.Parse(context.Background(), ParseContext{FilePath: fp, FileName: "test.tex"})
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(res.Text, "this is a comment") {
		t.Error("percent comment line should be stripped")
	}
	if strings.Contains(res.Text, "indented comment") {
		t.Error("indented percent comment should be stripped")
	}
	if !strings.Contains(res.Text, "\\section{Hello}") {
		t.Error("section command should be preserved")
	}
	if !strings.Contains(res.Text, "World") {
		t.Error("regular content should be preserved")
	}
}

func TestTexParserRemoveCommentEnvironment(t *testing.T) {
	t.Parallel()
	p := &TexParser{}
	dir := t.TempDir()
	fp := filepath.Join(dir, "test.tex")

	content := "Before\n\\begin{comment}\nThis should be removed.\nMultiple lines.\n\\end{comment}\nAfter\n"
	if err := os.WriteFile(fp, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	res, err := p.Parse(context.Background(), ParseContext{FilePath: fp, FileName: "test.tex"})
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(res.Text, "This should be removed") {
		t.Error("comment environment content should be removed")
	}
	if !strings.Contains(res.Text, "Before") {
		t.Error("content before comment env should be preserved")
	}
	if !strings.Contains(res.Text, "After") {
		t.Error("content after comment env should be preserved")
	}
}

func TestTexParserPreservesContent(t *testing.T) {
	t.Parallel()
	p := &TexParser{}
	dir := t.TempDir()
	fp := filepath.Join(dir, "test.tex")

	content := "\\documentclass{article}\n\\begin{document}\nHello, world!\n\\end{document}\n"
	if err := os.WriteFile(fp, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	res, err := p.Parse(context.Background(), ParseContext{FilePath: fp, FileName: "test.tex"})
	if err != nil {
		t.Fatal(err)
	}

	expected := "\\documentclass{article}\n\\begin{document}\nHello, world!\n\\end{document}"
	if res.Text != expected {
		t.Errorf("expected content to be preserved, got: %q", res.Text)
	}
}
