package app

import (
	"bookget/config"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestExtractIIIFMediaV2AndV3(t *testing.T) {
	base, _ := url.Parse("https://example.test/manifest.json")
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "v2",
			body: `{"sequences":[{"canvases":[{"images":[{"resource":{"service":{"@id":"https://img.test/iiif/page-1"}}}]}]}]}`,
			want: "https://img.test/iiif/page-1/info.json",
		},
		{
			name: "v3",
			body: `{"items":[{"items":[{"body":{"service":[{"id":"https://img.test/iiif/page-2"}]}}]}]}`,
			want: "https://img.test/iiif/page-2/info.json",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractIIIFMedia([]byte(tt.body), base)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0] != tt.want {
				t.Fatalf("media URLs = %#v, want %#v", got, []string{tt.want})
			}
		})
	}
}

func TestDownloadTemplateHTMLImage(t *testing.T) {
	oldExt, oldCookie, oldHeader := config.Conf.FileExt, config.Conf.CookieFile, config.Conf.HeaderFile
	config.Conf.FileExt = ".jpg"
	config.Conf.CookieFile = filepath.Join(t.TempDir(), "missing-cookie.txt")
	config.Conf.HeaderFile = filepath.Join(t.TempDir(), "missing-header.txt")
	t.Cleanup(func() {
		config.Conf.FileExt = oldExt
		config.Conf.CookieFile = oldCookie
		config.Conf.HeaderFile = oldHeader
	})

	imageBody := []byte(strings.Repeat("x", 128))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page.html":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<html><img src="/image.png"></html>`))
		case "/image.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(imageBody)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "0001.html")
	var downloaded int64
	if err := NewImageDownloader().downloadTemplateResource(server.URL+"/page.html", dest, nil, &downloaded); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(filepath.Dir(dest), "0001.png"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(imageBody) || atomic.LoadInt64(&downloaded) != 1 {
		t.Fatalf("downloaded image/count = %d/%d", len(got), downloaded)
	}
}

func TestDownloadPageSmartCustomTemplate(t *testing.T) {
	var requestedPage string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page/007.html":
			requestedPage = r.URL.Path
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<img src="/page.png">`))
		case "/page.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("template-image"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	var downloaded int64
	i := NewImageDownloader()
	i.downloadPageSmart(server.URL+"/page/{{page:03}}.html", "0001", 7, dir, "04", ".html", nil, &downloaded)
	if requestedPage != "/page/007.html" {
		t.Fatalf("rendered page path = %q, want /page/007.html", requestedPage)
	}
	if _, err := os.Stat(filepath.Join(dir, "0007.png")); err != nil {
		t.Fatal(err)
	}
}
