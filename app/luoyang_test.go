package app

import (
	"bookget/config"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func luoyangTestConfig(t *testing.T) {
	t.Helper()
	old := config.Conf
	t.Cleanup(func() { config.Conf = old })
	config.Conf = config.Input{Directory: t.TempDir(), Timeout: 5 * time.Second, UserAgent: "bookget-test"}
}

func TestLuoyangDetailLinks(t *testing.T) {
	base, _ := url.Parse("http://111.7.82.29:8090/books/productshow.php?id=365&cid=5")
	body := `<span class="title">試帖 <b>百篇</b> &amp; 最豁解</span>
<a href="viewer.php?pdf=uploads/image/20250411/1744353537.pdf&name=第一册">一</a>
<a href='viewer.php?name=第二册&amp;pdf=%2Fuploads%2Fbook%20two.PDF'>二</a>
<a href="http://111.7.82.29:8090/books/viewer.php?pdf=uploads/image/20250411/1744353537.pdf">重复</a>
<a href="/viewer.php?pdf=%2Fuploads%2Fthree.pdf%3Fkey%3Da%252Bb">三</a>`
	title, volumes, err := parseLuoyangDetail(base, body)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"http://111.7.82.29:8090/books/uploads/image/20250411/1744353537.pdf",
		"http://111.7.82.29:8090/uploads/book%20two.PDF",
		"http://111.7.82.29:8090/uploads/three.pdf?key=a%2Bb",
	}
	if title != "試帖 百篇 & 最豁解" || !reflect.DeepEqual(volumes, want) {
		t.Fatalf("title=%q volumes=%v", title, volumes)
	}
	if id := NewLuoyang().getBookId(base.String()); id != "365" {
		t.Fatalf("id-first URL parsed as %q", id)
	}
	for _, body := range []string{
		`<title>没有阅读入口</title>`,
		`<a href="viewer.php?pdf=login.html">not PDF</a>`,
		`<a href="viewer.php?pdf=https://other.example/scan.pdf">external</a>`,
	} {
		if _, _, err := parseLuoyangDetail(base, body); err == nil {
			t.Fatalf("accepted missing/invalid PDF: %s", body)
		}
	}
}

// These fixtures exercise transfer validation, not PDF structure parsing.
const luoyangTestPDF = "%PDF-1.5\nfixture for download validation\n%%EOF\n"

func TestLuoyangDownloadVolumesAndResume(t *testing.T) {
	luoyangTestConfig(t)
	config.Conf.VolStart, config.Conf.VolEnd = 2, 2
	headerFile := filepath.Join(t.TempDir(), "headers.txt")
	if err := os.WriteFile(headerFile, []byte("Cookie: account=test\nX-Test: present\nRange: bytes=0-0\nIf-None-Match: old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	config.Conf.HeaderFile = headerFile
	hits := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("X-Test") != "present" || req.Header.Get("Range") != "" || req.Header.Get("If-None-Match") != "" {
			t.Errorf("wrong headers: %v", req.Header)
		}
		if _, err := req.Cookie("account"); err != nil {
			t.Error("missing supplied cookie")
		}
		if req.URL.Path == "/productshow.php" {
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "ok", Path: "/"})
			io.WriteString(w, `<span class="title">測試古籍</span><a href="viewer.php?pdf=/one.pdf&name=一">一</a><a href="viewer.php?pdf=/two.pdf&name=二">二</a>`)
			return
		}
		if _, err := req.Cookie("session"); err != nil {
			t.Error("missing reader cookie jar")
		}
		if !strings.HasSuffix(req.Referer(), "/productshow.php?id=365&cid=5") {
			t.Errorf("escaped or missing Referer: %s", req.Referer())
		}
		hits[req.URL.Path]++
		io.WriteString(w, luoyangTestPDF)
	}))
	defer server.Close()
	rawURL := server.URL + "/productshow.php?id=365&cid=5"
	output, err := NewLuoyang().Run(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(output, "0002.pdf")
	before, err := os.Stat(filename)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(output) != "測試古籍-365" || hits["/one.pdf"] != 0 || hits["/two.pdf"] != 1 {
		t.Fatalf("output=%s hits=%v", output, hits)
	}
	if _, err := NewLuoyang().Run(rawURL); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(filename)
	if hits["/two.pdf"] != 1 || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("completed volume was downloaded or overwritten")
	}
	if err := os.WriteFile(filename, []byte("%PDF-1.5\ntruncated"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLuoyang().Run(rawURL); err != nil {
		t.Fatal(err)
	}
	if hits["/two.pdf"] != 2 || !validLuoyangPDF(filename) {
		t.Fatal("truncated volume was not replaced")
	}
	manifest, err := os.ReadFile(filepath.Join(output, "urls.txt"))
	if err != nil || string(manifest) != server.URL+"/one.pdf\n"+server.URL+"/two.pdf\n" {
		t.Fatalf("manifest=%q err=%v", manifest, err)
	}
}

func TestLuoyangRejectsFailedDownloads(t *testing.T) {
	for _, kind := range []string{"http", "html", "trailer", "truncated", "partial"} {
		t.Run(kind, func(t *testing.T) {
			luoyangTestConfig(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				switch kind {
				case "http":
					http.Error(w, "not found", http.StatusNotFound)
				case "html":
					io.WriteString(w, "<html>login</html>")
				case "trailer":
					io.WriteString(w, "%PDF-1.5\nmissing trailer")
				case "truncated":
					w.Header().Set("Content-Length", "9999")
					io.WriteString(w, luoyangTestPDF)
				case "partial":
					w.WriteHeader(http.StatusPartialContent)
					io.WriteString(w, luoyangTestPDF)
				}
			}))
			defer server.Close()
			if _, err := NewLuoyang().Run(server.URL + "/viewer.php?pdf=/scan.pdf&name=Test"); err == nil {
				t.Fatal("failure was swallowed")
			}
			filepath.WalkDir(config.Conf.Directory, func(filename string, entry os.DirEntry, err error) error {
				if err != nil {
					t.Error(err)
					return err
				}
				if !entry.IsDir() && entry.Name() != "urls.txt" {
					t.Errorf("failed download left a file: %s", filename)
				}
				return nil
			})
		})
	}
}

func TestLuoyangViewerAndInvalidInputs(t *testing.T) {
	luoyangTestConfig(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		io.WriteString(w, luoyangTestPDF)
	}))
	defer server.Close()
	output, err := NewLuoyang().Run(server.URL + "/viewer.php?name=卷一&pdf=/scan.pdf")
	if err != nil || !validLuoyangPDF(filepath.Join(output, "0001.pdf")) {
		t.Fatalf("viewer download failed: %v", err)
	}
	for _, rawURL := range []string{"://bad", server.URL + "/productshow.php?cid=5", server.URL + "/unknown"} {
		if _, err := NewLuoyang().Run(rawURL); err == nil {
			t.Fatalf("accepted invalid URL %s", rawURL)
		}
	}
	config.Conf.Seq = "1:3"
	if _, err := NewLuoyang().Run(server.URL + "/productshow.php?id=365"); err == nil || !strings.Contains(err.Error(), "整册 PDF") {
		t.Fatalf("page range error=%v", err)
	}
}

func TestLuoyangRetriesAndContinuesAfterFailedVolume(t *testing.T) {
	luoyangTestConfig(t)
	config.Conf.Retries = 1
	hits := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/productshow.php" {
			io.WriteString(w, `<span class="title">重試</span><a href="viewer.php?pdf=/one.pdf">一</a><a href="viewer.php?pdf=/two.pdf">二</a>`)
			return
		}
		hits[req.URL.Path]++
		if req.URL.Path == "/one.pdf" || hits[req.URL.Path] == 1 {
			http.Error(w, "temporary failure", http.StatusServiceUnavailable)
			return
		}
		io.WriteString(w, luoyangTestPDF)
	}))
	defer server.Close()
	_, err := NewLuoyang().Run(server.URL + "/productshow.php?id=1")
	if err == nil || !strings.Contains(err.Error(), "1/2 册下载失败") || hits["/one.pdf"] != 2 || hits["/two.pdf"] != 2 {
		t.Fatalf("err=%v hits=%v", err, hits)
	}
	if !validLuoyangPDF(filepath.Join(config.Conf.Directory, "重試-1", "0002.pdf")) {
		t.Fatal("failure in the first volume prevented download of the next volume")
	}
}
