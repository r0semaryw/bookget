package app

import (
	"bookget/config"
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func shidianTestConfig(t *testing.T) {
	t.Helper()
	old := config.Conf
	t.Cleanup(func() { config.Conf = old })
	config.Conf = config.Input{Directory: t.TempDir(), UserAgent: "bookget-test", Retries: 1}
}

func shidianTestManifest(t *testing.T, m shidianManifest) string {
	t.Helper()
	filename := filepath.Join(t.TempDir(), m.BookID+".json")
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, b, 0600); err != nil {
		t.Fatal(err)
	}
	return filename
}

func shidianTestImage(t *testing.T, format string) []byte {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, 3, 4))
	im.Set(1, 1, color.RGBA{R: 255, A: 255})
	var b bytes.Buffer
	var err error
	if format == "png" {
		err = png.Encode(&b, im)
	} else {
		err = jpeg.Encode(&b, im, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestShidianDownloadAndResume(t *testing.T) {
	shidianTestConfig(t)
	bodies := map[string][]byte{"/1": shidianTestImage(t, "png"), "/2": shidianTestImage(t, "jpeg")}
	requests := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests[r.URL.Path]++
		if r.URL.RawQuery != "x-signature=a%2Bb%2Fc%3D&x-expires=9999999999" {
			t.Errorf("signed query was changed: %s", r.URL.RawQuery)
		}
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Error("site credentials sent to image host")
		}
		if r.Header.Get("Referer") == "" {
			t.Error("missing referer")
		}
		// Even with an unhelpful HTTP content type, inspect the actual bytes.
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(bodies[r.URL.Path])
	}))
	defer server.Close()
	m := shidianManifest{Format: "bookget-shidian-v1", BookID: "NCL11566A", BookName: "東岡小稿", TotalPage: 2}
	// The supplied list need not be sorted. Filenames follow pageNum.
	for _, n := range []int{2, 1} {
		m.Pages = append(m.Pages, shidianPage{PageNum: n, URL: fmt.Sprintf("%s/%d?x-signature=a%%2Bb%%2Fc%%3D&x-expires=9999999999", server.URL, n)})
	}
	config.Conf.ShidianPages = filepath.Dir(shidianTestManifest(t, m))
	config.Conf.HeaderFile = filepath.Join(t.TempDir(), "headers.txt")
	if err := os.WriteFile(config.Conf.HeaderFile, []byte("Cookie: site-session=test\nAuthorization: Bearer test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	url := "https://www.shidianguji.com/book/NCL11566A/chapter/anything"
	path, err := NewShidian().Run(url)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "東岡小稿-NCL11566A" {
		t.Fatalf("book folder = %q", path)
	}
	for i, ext := range []string{"png", "jpg"} {
		b, err := os.ReadFile(filepath.Join(path, fmt.Sprintf("%04d.%s", i+1, ext)))
		if err != nil || !bytes.Equal(b, bodies[fmt.Sprintf("/%d", i+1)]) {
			t.Fatalf("page %d bytes not preserved: %v", i+1, err)
		}
	}
	if _, err := NewShidian().Run(url); err != nil {
		t.Fatal(err)
	}
	if requests["/1"] != 1 || requests["/2"] != 1 {
		t.Fatalf("resume re-downloaded complete images: %v", requests)
	}
	// A truncated file must be retried, not treated as a completed page.
	if err := os.WriteFile(filepath.Join(path, "0002.jpg"), bodies["/2"][:20], 0600); err != nil {
		t.Fatal(err)
	}
	config.Conf.SeqStart, config.Conf.SeqEnd = 2, 2
	if _, err := NewShidian().Run(url); err != nil {
		t.Fatal(err)
	}
	if requests["/1"] != 1 || requests["/2"] != 2 {
		t.Fatalf("range/resume requests: %v", requests)
	}
}

func TestShidianRejectsInvalidManifests(t *testing.T) {
	good := shidianManifest{Format: "bookget-shidian-v1", BookID: "B1", TotalPage: 1,
		Pages: []shidianPage{{PageNum: 1, URL: "https://images.example/scan.png"}}}
	for _, tc := range []struct {
		name string
		edit func(*shidianManifest)
	}{
		{"wrong book", func(m *shidianManifest) { m.BookID = "B2" }},
		{"wrong format", func(m *shidianManifest) { m.Format = "previewPages" }},
		{"incomplete", func(m *shidianManifest) { m.TotalPage = 2 }},
		{"duplicate", func(m *shidianManifest) { m.TotalPage = 2; m.Pages = append(m.Pages, m.Pages[0]) }},
		{"zero page", func(m *shidianManifest) { m.Pages[0].PageNum = 0 }},
		{"encrypted URL", func(m *shidianManifest) { m.Pages[0].URL = "ciphertext==" }},
		{"local file", func(m *shidianManifest) { m.Pages[0].URL = "file:///etc/passwd" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := good
			m.Pages = append([]shidianPage(nil), good.Pages...)
			tc.edit(&m)
			if _, err := validateShidianManifest(m, "B1"); err == nil {
				t.Fatal("accepted invalid manifest")
			}
		})
	}
}

func TestShidianRejectsErrorBodies(t *testing.T) {
	for _, name := range []string{"html", "truncated PNG", "expired URL"} {
		t.Run(name, func(t *testing.T) {
			shidianTestConfig(t)
			pngBody := shidianTestImage(t, "png")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch name {
				case "expired URL":
					w.WriteHeader(http.StatusForbidden)
				case "truncated PNG":
					_, _ = w.Write(pngBody[:len(pngBody)-10])
				default:
					_, _ = w.Write([]byte("<html>Please log in</html>"))
				}
			}))
			defer server.Close()
			err := NewShidian().downloadPage(server.URL, "https://www.shidianguji.com/book/B1", filepath.Join(config.Conf.Directory, "0001"))
			if err == nil {
				t.Fatal("accepted error body as a scan")
			}
			files, _ := os.ReadDir(config.Conf.Directory)
			if len(files) != 0 {
				t.Fatalf("failed download left files: %v", files)
			}
		})
	}
}

func TestShidianRequiresBrowserExport(t *testing.T) {
	shidianTestConfig(t)
	_, err := NewShidian().Run("https://www.shidianguji.com/book/B1")
	if err == nil || !strings.Contains(err.Error(), "--shidian-pages") {
		t.Fatalf("missing export instructions: %v", err)
	}
}
