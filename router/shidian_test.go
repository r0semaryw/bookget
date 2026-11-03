package router

import (
	"bookget/config"
	"path/filepath"
	"strings"
	"testing"
)

func TestShidianRegisteredForOrdinaryURLs(t *testing.T) {
	old := config.Conf
	t.Cleanup(func() { config.Conf = old })
	config.Conf = config.Input{ShidianPages: filepath.Join(t.TempDir(), "missing.json")}
	for _, host := range []string{"www.shidianguji.com", "shidianguji.com"} {
		_, err := FactoryRouter(host, "https://"+host+"/book/NCL11566A/chapter/test")
		if err == nil || !strings.Contains(err.Error(), "读取识典古籍清单") {
			t.Fatalf("%s not routed to Shidian: %v", host, err)
		}
	}
}
