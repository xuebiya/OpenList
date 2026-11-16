package handles

import (
	"net/url"
	"strings"
	"testing"

	driver115 "github.com/SheltonZhu/115driver/pkg/driver"
)

func TestQR115Sources(t *testing.T) {
	for _, source := range []string{"web", "android", "ios", "tv", "alipaymini", "wechatmini", "qandroid"} {
		app, ok := qr115App(source)
		if !ok || app != driver115.LoginApp(source) {
			t.Fatalf("source %q did not select its login app", source)
		}
		imageURL := qr115ImageURL(source, "uid with space")
		parsed, err := url.Parse(imageURL)
		if err != nil || !strings.Contains(parsed.Path, "/"+source+"/1.0/qrcode") || parsed.Query().Get("uid") != "uid with space" {
			t.Fatalf("source %q did not select its QR image: %q (%v)", source, imageURL, err)
		}
	}
	for _, source := range []string{"linux", "mac", "windows", "", "../web"} {
		if _, ok := qr115App(source); ok {
			t.Fatalf("unsupported source %q was accepted", source)
		}
	}
}
