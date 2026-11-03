package app

import (
	"errors"
	"strings"
	"testing"
)

func TestShidianBrowserFallback(t *testing.T) {
	old := shidianPreferHeaded.Load()
	t.Cleanup(func() { shidianPreferHeaded.Store(old) })
	for _, tc := range []struct {
		name            string
		headed, desktop bool
		code, calls     int
		success         bool
	}{
		{"retry visible once", false, true, 31000, 2, true},
		{"no desktop", false, false, 31000, 1, false},
		{"already visible", true, true, 31000, 1, false},
		{"permission error", false, true, 40031, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shidianPreferHeaded.Store(false)
			calls := 0
			_, err := exportShidianWithFallback(tc.headed, tc.desktop, func(headed bool) (shidianManifest, error) {
				calls++
				if calls == 2 {
					if !headed {
						t.Fatal("retry was headless")
					}
					return shidianManifest{}, nil
				}
				return shidianManifest{}, &shidianAPIError{Code: tc.code, Stage: "书籍加载"}
			})
			if calls != tc.calls || (err == nil) != tc.success || shidianPreferHeaded.Load() != tc.success {
				t.Fatalf("calls=%d err=%v preferred=%v", calls, err, shidianPreferHeaded.Load())
			}
			if !tc.desktop && !strings.Contains(err.Error(), "--shidian-pages") {
				t.Fatal("missing no-desktop fallback instructions")
			}
		})
	}
	// Even if the visible attempt is rejected, there is no endless retry loop.
	calls := 0
	_, err := exportShidianWithFallback(false, true, func(bool) (shidianManifest, error) {
		calls++
		return shidianManifest{}, &shidianAPIError{Code: 31000}
	})
	if calls != 2 || err == nil {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestShidianRelevantAPIErrors(t *testing.T) {
	for _, tc := range []struct {
		path, body string
		fail       bool
	}{
		{"/api/ancientlib/read/reader-book/get/", `{"errorCode":31000}`, true},
		{"/api/ancientlib/read/book/get/", `{"errorCode":31000}`, true},
		{"/api/ancientlib/read/book/pages/v3/", `{"errorCode":31000}`, true},
		{"/api/ancientlib/read/book/pages/v3/", `{"errorCode":0}`, false},
		{"/api/ancientlib/read/book/pages/v3/", `invalid`, false},
		{"/api/ancientlib/read/bookmark/list/", `{"errorCode":40031}`, false},
	} {
		err := shidianResponseError("https://www.shidianguji.com"+tc.path, []byte(tc.body))
		var apiErr *shidianAPIError
		if errors.As(err, &apiErr) != tc.fail {
			t.Fatalf("%s: %v", tc.path, err)
		}
	}
	if err := shidianResponseError("https://other.example/api/ancientlib/read/book/get/", []byte(`{"errorCode":31000}`)); err != nil {
		t.Fatal(err)
	}
}
