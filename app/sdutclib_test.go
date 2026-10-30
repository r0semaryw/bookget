package app

import (
	"bookget/config"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEncryptSduTclibParam(t *testing.T) {
	got, err := encryptSduTclibParam("3638")
	if err != nil {
		t.Fatal(err)
	}
	if got != "IdnoXQSvOI95639+6Ufz3w==" {
		t.Fatalf("encrypted book id = %q", got)
	}
}

func TestSduTclibDownload(t *testing.T) {
	oldConfig := config.Conf
	t.Cleanup(func() { config.Conf = oldConfig })
	config.Conf.Directory = t.TempDir()
	config.Conf.CookieFile = filepath.Join(t.TempDir(), "missing-cookie.txt")
	config.Conf.HeaderFile = filepath.Join(t.TempDir(), "header.txt")
	config.Conf.UserAgent = "bookget-test"
	config.Conf.SeqStart = 0
	config.Conf.SeqEnd = 0
	if err := os.WriteFile(config.Conf.HeaderFile, []byte("Cookie: userId=42; permissionLevel=3\n"), 0644); err != nil {
		t.Fatal(err)
	}

	images := map[string][]byte{
		"00000001": []byte("first-page"),
		"00000002": []byte("second-page"),
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.URL.Path == "/portal-tsxq.html":
			_, _ = w.Write([]byte("reader"))
		case req.URL.Path == "/portal/book/detail":
			if req.URL.Query().Get("bookId") != "IdnoXQSvOI95639+6Ufz3w==" {
				t.Errorf("encrypted detail id = %q", req.URL.Query().Get("bookId"))
			}
			_, _ = fmt.Fprint(w, `{"status":1,"data":[{"attr":"題名卷數","value":"测试古籍"}]}`)
		case req.URL.Path == "/portal/book/view":
			if req.URL.Query().Get("userId") == "" || req.URL.Query().Get("id") == "" {
				t.Errorf("view query = %s", req.URL.RawQuery)
			}
			_, _ = fmt.Fprintf(w, `{"status":1,"data":{"title":"测试古籍","serverBase":"%s/medias/3638","images":["00000001","00000002"]}}`, server.URL)
		case strings.HasPrefix(req.URL.Path, "/medias/3638/thumbnails/"):
			name := strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, "/medias/3638/thumbnails/"), "/large.jpg")
			body, ok := images[name]
			if !ok {
				http.NotFound(w, req)
				return
			}
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(body)
		default:
			http.NotFound(w, req)
		}
	}))
	defer server.Close()

	if _, err := NewSduTclib().Run(server.URL + "/portal-tsxq.html?id=3638"); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"first-page", "second-page"} {
		got, err := os.ReadFile(filepath.Join(config.Conf.Directory, fmt.Sprintf("%04d.jpg", i+1)))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("page %d = %q, want %q", i+1, got, want)
		}
	}
}
