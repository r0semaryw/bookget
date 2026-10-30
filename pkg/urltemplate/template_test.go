package urltemplate

import "testing"

func TestRenderSupportsLegacyAndCustomPlaceholders(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"https://example.test/page/[PAGE].jpg", "https://example.test/page/0007.jpg"},
		{"https://example.test/{{page:3d}}/{{volume:2}}.json", "https://example.test/007/03.json"},
		{"https://example.test/{{index}}/[VOL]/{{ab}}.html", "https://example.test/0007/0003/{{ab}}.html"},
	}
	for _, tt := range tests {
		got, err := Render(tt.raw, 7, 3, "04")
		if err != nil {
			t.Fatalf("Render(%q): %v", tt.raw, err)
		}
		if got != tt.want {
			t.Errorf("Render(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}

func TestPlaceholders(t *testing.T) {
	if !HasPage("https://example.test/{{page:04}}.json") {
		t.Fatal("custom page placeholder was not detected")
	}
	if !HasVolume("https://example.test/{{volume}}/{{page}}.html") {
		t.Fatal("custom volume placeholder was not detected")
	}
	if token, lower, ok := ABPlaceholder("https://example.test/{{ab}}/{{page}}.html"); !ok || token != "{{ab}}" || !lower {
		t.Fatalf("ABPlaceholder() = %q, %v, %v", token, lower, ok)
	}
}
