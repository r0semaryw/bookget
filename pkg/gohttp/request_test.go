package gohttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNetscapeCookieFile(t *testing.T) {
	var got map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = make(map[string]string)
		for _, cookie := range r.Cookies() {
			got[cookie.Name] = cookie.Value
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	cookieFile := filepath.Join(t.TempDir(), "cookies.txt")
	contents := strings.Join([]string{
		"# Netscape HTTP Cookie File",
		"example.test\tTRUE\t/\tFALSE\t0\tuserId\t42",
		"#HttpOnly_example.test\tTRUE\t/\tTRUE\t0\tpermissionLevel\t3",
		"",
	}, "\n")
	if err := os.WriteFile(cookieFile, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}

	resp, err := NewClient(context.Background(), Options{
		CookieFile: cookieFile,
		Retry:      1,
	}).Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}
	if got["userId"] != "42" || got["permissionLevel"] != "3" {
		t.Fatalf("cookies = %#v", got)
	}
}
