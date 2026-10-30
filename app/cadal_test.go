package app

import (
	"bookget/config"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestDecodeCADALJSON(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "json", body: `{"pages":"12"}`, want: "12"},
		{name: "jsonp", body: `bookgetCADAL({"pages":"12"});`, want: "12"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var info cadalBookInfo
			if err := decodeCADALJSON([]byte(tt.body), &info); err != nil {
				t.Fatal(err)
			}
			if info.Pages != tt.want {
				t.Fatalf("pages = %q, want %q", info.Pages, tt.want)
			}
		})
	}
}

func TestCADALPageImageURL(t *testing.T) {
	base, err := url.Parse("https://cadal.edu.cn/cx/reader/reader.shtml?ssno=06707067")
	if err != nil {
		t.Fatal(err)
	}
	got := cadalPageImageURL(base, "img/signed-prefix/", 7, "2", "", "")
	wantPage := base64.StdEncoding.EncodeToString([]byte("00000007.jpg"))
	if !strings.Contains(got, "/cx/reader/img/signed-prefix/"+wantPage) {
		t.Fatalf("page URL = %q, missing encoded page", got)
	}
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Query().Get("bookType") != "2" || parsed.Query().Get("ssno") != "06707067" || parsed.Query().Get("page") != "7" {
		t.Fatalf("page query = %s", parsed.RawQuery)
	}
	if got := bookTypeOrUndefined(""); got != "undefined" {
		t.Fatalf("empty optional query value = %q, want undefined", got)
	}
}

func TestCADALDownloadPagesThroughReaderAPI(t *testing.T) {
	oldConfig := config.Conf
	t.Cleanup(func() { config.Conf = oldConfig })
	config.Conf.Directory = t.TempDir()
	config.Conf.CookieFile = filepath.Join(t.TempDir(), "missing-cookie.txt")
	config.Conf.HeaderFile = filepath.Join(t.TempDir(), "missing-header.txt")
	config.Conf.UserAgent = "bookget-test"
	config.Conf.SeqStart = 0
	config.Conf.SeqEnd = 0

	const ssno = "06707067"
	pageBodies := map[string][]byte{
		"00000001.jpg": []byte("page-one"),
		"00000002.jpg": []byte("page-two"),
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/reader" {
			http.SetCookie(w, &http.Cookie{Name: "reader-session", Value: "ok", Path: "/"})
			_, _ = w.Write([]byte("reader"))
			return
		}
		if req.URL.Path == "/cx/reader/reader/cadal/api/addr" {
			if cookie, err := req.Cookie("reader-session"); err != nil || cookie.Value != "ok" {
				t.Errorf("reader session cookie = %v, want reader-session=ok", cookie)
			}
			if req.Method != http.MethodPost {
				t.Errorf("address method = %s, want POST", req.Method)
			}
			if got := req.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/x-www-form-urlencoded") {
				t.Errorf("address content type = %q", got)
			}
			if got := req.Header.Get("X-Requested-With"); got != "XMLHttpRequest" {
				t.Errorf("address X-Requested-With = %q", got)
			}
			if err := req.ParseForm(); err != nil {
				t.Error(err)
			}
			if req.Form.Get("ssno") != ssno || req.Form.Get("apiType") != "bookInfo" {
				t.Errorf("address form = %s", req.Form.Encode())
			}
			_, _ = fmt.Fprintf(w, `{"success":true,"params":{"ssno":"%s","securityCode":"test"},"url":"%s/bookInfo"}`, ssno, server.URL)
			return
		}
		if req.URL.Path == "/bookInfo" {
			if req.URL.Query().Get("callback") != "bookgetCADAL" {
				t.Errorf("book info callback = %q", req.URL.Query().Get("callback"))
			}
			_, _ = fmt.Fprint(w, `bookgetCADAL({"msg":"1001","imgpath":"img/signed-prefix/","title":"Test book","pages":"3","allowpages":"2","ssno":"06707067","dataSources":"1","bookType":"2"});`)
			return
		}
		if req.URL.Path == "/cx/reader/reader/book/info" {
			if req.Method != http.MethodPost {
				t.Errorf("image info method = %s, want POST", req.Method)
			}
			if err := req.ParseForm(); err != nil {
				t.Error(err)
			}
			if req.Form.Get("bookType") != "2" || req.Form.Get("ssno") != ssno {
				t.Errorf("image info form = %s", req.Form.Encode())
			}
			_, _ = fmt.Fprint(w, `{"success":false,"pathInfo":{"startPage":1,"endPage":2,"jpgUrl":"/cx/reader/img/signed-prefix/"}}`)
			return
		}
		if strings.HasPrefix(req.URL.Path, "/cx/reader/img/signed-prefix/1/") {
			if req.URL.Query().Get("bookType") != "2" || req.URL.Query().Get("ssno") != ssno {
				t.Errorf("image query = %s", req.URL.RawQuery)
			}
			encoded := strings.TrimPrefix(req.URL.Path, "/cx/reader/img/signed-prefix/1/")
			decoded, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				t.Errorf("decode page name: %v", err)
				http.Error(w, "bad page", http.StatusBadRequest)
				return
			}
			body, ok := pageBodies[string(decoded)]
			if !ok {
				http.NotFound(w, req)
				return
			}
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(body)
			return
		}
		http.NotFound(w, req)
	}))
	defer server.Close()

	r := NewCadan()
	if _, err := r.Run(server.URL + "/reader?ssno=" + ssno + "&bookType=2"); err != nil {
		t.Fatal(err)
	}
	for page, want := range pageBodies {
		pageNumber, err := strconv.Atoi(strings.TrimSuffix(page, ".jpg"))
		if err != nil {
			t.Fatal(err)
		}
		// CADAL files are stored with a four-digit local sequence.
		got, err := os.ReadFile(filepath.Join(config.Conf.Directory, fmt.Sprintf("%04d.jpg", pageNumber)))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("%s = %q, want %q", page, got, want)
		}
	}
}
