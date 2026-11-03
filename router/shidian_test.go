package router

import (
	"bookget/config"
	"strings"
	"testing"
)

func TestShidianRegisteredForOrdinaryURLs(t *testing.T) {
	old := config.Conf
	t.Cleanup(func() { config.Conf = old })
	config.Conf = config.Input{}
	for _, host := range []string{"www.shidianguji.com", "shidianguji.com"} {
		_, err := FactoryRouter(host, "https://"+host+"/book/NCL11566A/chapter/test")
		if err == nil || !strings.Contains(err.Error(), "--shidian-pages") {
			t.Fatalf("%s not routed to Shidian: %v", host, err)
		}
	}
}
