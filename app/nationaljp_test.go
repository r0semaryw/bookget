package app

import (
	"bookget/config"
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func nationaljpTestConfig(t *testing.T) {
	t.Helper()
	old := config.Conf
	t.Cleanup(func() { config.Conf = old })
	config.Conf = config.Input{
		Directory: t.TempDir(),
		FileExt:   ".jpg",
		Format:    "full/full/0/default.jpg",
		UseDzi:    false,
		UserAgent: "bookget-test",
	}
}

func testNationaljpJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 100), G: uint8(y * 100), B: 20, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestNationaljpRejectsHomeAndEmptyPages(t *testing.T) {
	nationaljpTestConfig(t)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/item/1" {
			_, _ = w.Write([]byte(`<html><title>metadata only</title></html>`))
			return
		}
		if req.URL.Path == "/DAS/meta/listPhoto" {
			_, _ = w.Write([]byte(`<html>old page without volumes</html>`))
			return
		}
		http.NotFound(w, req)
	}))
	defer server.Close()

	for _, rawURL := range []string{server.URL + "/", server.URL + "/item/1"} {
		if _, err := NewNationaljp().Run(rawURL); err == nil {
			t.Fatalf("accepted URL without downloadable content: %s", rawURL)
		}
	}
	if _, err := NewNationaljp().Run(server.URL + "/?BID=old-book"); err == nil {
		t.Fatal("accepted old page with an empty volume list")
	}
}

func TestNationaljpModernFileDownloadsAndResumes(t *testing.T) {
	nationaljpTestConfig(t)
	jpegBytes := testNationaljpJPEG(t)
	var manifestHits, imageHits int
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/file/42":
			_, _ = w.Write([]byte(`<a href="/img/7">閲覧</a><a href="https://` + req.Host + `/img/7">duplicate</a>`))
		case "/api/iiif/7/manifest.json":
			manifestHits++
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"sequences": []interface{}{map[string]interface{}{
					"canvases": []interface{}{map[string]interface{}{
						"images": []interface{}{map[string]interface{}{
							"resource": map[string]interface{}{
								"service": map[string]string{"@id": server.URL + "/content/7"},
							},
						}},
					}},
				}},
			})
		case "/content/7/full/full/0/default.jpg":
			imageHits++
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(jpegBytes)
		default:
			http.NotFound(w, req)
		}
	}))
	defer server.Close()

	output, err := NewNationaljp().Run(server.URL + "/file/42")
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(output, "0001.jpg")
	if !validNationaljpImage(filename) || imageHits != 1 || manifestHits != 1 {
		t.Fatalf("output=%s imageHits=%d manifestHits=%d", output, imageHits, manifestHits)
	}
	before, err := os.Stat(filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewNationaljp().Run(server.URL + "/file/42"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(filename)
	if imageHits != 1 || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("completed image was downloaded or overwritten")
	}
	if err := os.WriteFile(filename, []byte("interrupted"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewNationaljp().Run(server.URL + "/file/42"); err != nil {
		t.Fatal(err)
	}
	if imageHits != 2 || !validNationaljpImage(filename) {
		t.Fatal("invalid existing image was not replaced")
	}
}

func TestNationaljpModernRejectsInvalidImageResponse(t *testing.T) {
	nationaljpTestConfig(t)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/img/8":
			_, _ = w.Write([]byte(`<a href="/img/8">閲覧</a>`))
		case "/api/iiif/8/manifest.json":
			_, _ = w.Write([]byte(`{"sequences":[{"canvases":[{"images":[{"resource":{"service":{"@id":"` + server.URL + `/content/8"}}}]}]}]}`))
		case "/content/8/full/full/0/default.jpg":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<html>login</html>`))
		default:
			http.NotFound(w, req)
		}
	}))
	defer server.Close()

	if _, err := NewNationaljp().Run(server.URL + "/img/8"); err == nil || !strings.Contains(err.Error(), "有效图像") {
		t.Fatalf("invalid image response error=%v", err)
	}
	entries, _ := os.ReadDir(config.Conf.Directory)
	if len(entries) != 0 {
		t.Fatalf("invalid download left files: %v", entries)
	}
}
