package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadAndFilterURLs(t *testing.T) {
	file := filepath.Join(t.TempDir(), "urls.txt")
	content := "\ufeffhttps://example.com/book/1\r\n\nnot-a-url\nhttpx://bad\nhttps://example.com/book/2?part=2\n"
	if err := os.WriteFile(file, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := loadAndFilterURLs(file)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"https://example.com/book/1",
		"https://example.com/book/2?part=2",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("URLs = %#v, want %#v", got, want)
	}
}

func TestBatchOutputDirectoryIsStableAndDistinct(t *testing.T) {
	base := filepath.Join(t.TempDir(), "downloads")
	first := "https://example.com/book/1"
	second := "https://example.com/book/2"

	if got, want := batchOutputDirectory(base, first), batchOutputDirectory(base, first); got != want {
		t.Fatalf("same URL produced different directories: %q != %q", got, want)
	}
	if got, want := batchOutputDirectory(base, first), batchOutputDirectory(base, second); got == want {
		t.Fatalf("different URLs share directory %q", got)
	}
	if !strings.HasPrefix(batchOutputDirectory(base, first), base+string(os.PathSeparator)) {
		t.Fatalf("directory escaped batch root")
	}
}

func TestDirectOutputDirectoryUsesBookIdentifier(t *testing.T) {
	base := filepath.Join(t.TempDir(), "downloads")
	cadalURL := "https://cadal.edu.cn/cx/reader/reader/book/reader.shtml?ssno=06707066&bookType=2"
	got := directOutputDirectory(base, cadalURL)
	if !strings.Contains(filepath.Base(got), "cadal.edu.cn-ssno-06707066") {
		t.Fatalf("CADAL directory = %q, want ssno in directory name", got)
	}
	if !strings.HasPrefix(got, base+string(os.PathSeparator)) {
		t.Fatalf("directory escaped download root: %q", got)
	}
	if got != directOutputDirectory(base, cadalURL) {
		t.Fatalf("same URL produced different directories")
	}
	if got == directOutputDirectory(base, cadalURL+"&page=2") {
		t.Fatalf("different URLs share directory")
	}
}

func TestIsValidURL(t *testing.T) {
	for _, rawURL := range []string{
		"https://example.com/a",
		"http://localhost:8080/file",
	} {
		if !isValidURL(rawURL) {
			t.Errorf("isValidURL(%q) = false", rawURL)
		}
	}
	for _, rawURL := range []string{"", "ftp://example.com/a", "https://", "httpx://example.com"} {
		if isValidURL(rawURL) {
			t.Errorf("isValidURL(%q) = true", rawURL)
		}
	}
}
