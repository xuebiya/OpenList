package pikpaklogin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func raw(values map[string]any) map[string]json.RawMessage {
	b, _ := json.Marshal(values)
	var out map[string]json.RawMessage
	_ = json.Unmarshal(b, &out)
	return out
}

func TestApprovalURL(t *testing.T) {
	good := raw(map[string]any{"user_code": "abc 123"})
	link, err := approvalURL(good)
	if err != nil || !strings.HasPrefix(link, "https://mypikpak.com/drive/qr-login?") {
		t.Fatalf("valid URL rejected: %v", err)
	}
	for _, link := range []string{"http://mypikpak.com/drive/qr-login", "https://mypikpak.com.evil.test/drive/qr-login", "https://user@mypikpak.com/drive/qr-login", "https://mypikpak.com/elsewhere", "https://mypikpak.com/drive/qr-login?client_id=other", "https://mypikpak.com/drive/qr-login?user_code=wrong", "https://mypikpak.com/drive/qr-login?scope=user&scope=other"} {
		if _, err := approvalURL(raw(map[string]any{"user_code": "abc", "verification_uri_complete": link})); err == nil {
			t.Errorf("accepted untrusted URL: %s", link)
		}
	}
}

func TestDeviceConsentAndAccountBinding(t *testing.T) {
	for _, scenario := range []string{"pending", "slow_down", "approved", "mismatch", "unverified", "denied", "expired"} {
		t.Run(scenario, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.Header().Set("Content-Type", "application/json")
				if r.Header.Get("x-client-id") != clientID || r.Header.Get("x-device-id") != "device-fixture" {
					t.Error("client/device mismatch")
				}
				if r.URL.Path == "/v1/user/device/authorize" {
					t.Error("must not automatically approve consent")
				}
				if r.URL.Path == "/v1/user/me" {
					if r.Header.Get("Authorization") != "Bearer access-fixture" {
						t.Error("missing account check")
					}
					email := "tester@example.invalid"
					if scenario == "mismatch" {
						email = "other@example.invalid"
					}
					if scenario == "unverified" {
						w.WriteHeader(403)
						fmt.Fprint(w, `{}`)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"email": email})
					return
				}
				if r.URL.Path != "/v1/auth/token" {
					t.Error("unexpected endpoint")
				}
				var body map[string]string
				_ = json.NewDecoder(r.Body).Decode(&body)
				if body["device_code"] != "device-code-fixture" || body["grant_type"] != "urn:ietf:params:oauth:grant-type:device_code" {
					t.Error("invalid device grant")
				}
				switch scenario {
				case "approved", "mismatch", "unverified":
					fmt.Fprint(w, `{"refresh_token":"refresh-fixture","access_token":"access-fixture","token_type":"Bearer"}`)
				default:
					w.WriteHeader(400)
					code := map[string]string{"pending": "authorization_pending", "slow_down": "slow_down", "denied": "access_denied", "expired": "expired_token"}[scenario]
					_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
				}
			}))
			defer server.Close()
			session := &Session{device: "device-fixture", code: "device-code-fixture", account: "TESTER@example.invalid", interval: 2 * time.Second, browser: &browser{}}
			err := session.poll(context.Background(), service{base: server.URL, http: server.Client()})
			switch scenario {
			case "approved":
				if err != nil || session.completed == nil || session.completed.RefreshToken != "refresh-fixture" || session.completed.Platform != "pc" || requests != 2 {
					t.Fatalf("approved result not account-checked: %v", err)
				}
			case "pending", "slow_down":
				if err != nil || session.completed != nil {
					t.Fatalf("pending login issued token: %v", err)
				}
				if scenario == "slow_down" && session.interval != 4*time.Second {
					t.Error("poll did not back off")
				}
			default:
				if err == nil || session.completed != nil {
					t.Fatal("unapproved or mismatched account issued token")
				}
			}
			if scenario == "pending" {
				_ = session.poll(context.Background(), service{base: server.URL, http: server.Client()})
				if requests != 1 {
					t.Error("ignored poll interval")
				}
			}
		})
	}
}

func TestBrowserCardAndCredentialAutofill(t *testing.T) {
	if os.Getenv("OPENLIST_TEST_PIKPAK_BROWSER") != "1" {
		t.Skip("set OPENLIST_TEST_PIKPAK_BROWSER=1 for installed-browser integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	b, err := newBrowser(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	if err = b.call(ctx, "Page.navigate", map[string]string{"url": "https://mypikpak.com/robots.txt"}, nil); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 60; i++ {
		var ready bool
		_ = b.evaluate(ctx, officialPage, &ready)
		if ready {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	html := `<!doctype html><html><body><nav>This header must be cropped</nav><div class="pp-login-panel" style="position:absolute;left:60px;top:100px;width:400px;height:380px;background:white"><form onsubmit="event.preventDefault();window.submitted=true"><input autocomplete=email><input type=password><button class=pp-button type=submit>Sign in</button></form></div></body></html>`
	var tree struct {
		FrameTree struct{ Frame struct{ ID string } }
	}
	if err = b.call(ctx, "Page.getFrameTree", map[string]any{}, &tree); err != nil {
		t.Fatal(err)
	}
	if err = b.call(ctx, "Page.setDocumentContent", map[string]string{"frameId": tree.FrameTree.Frame.ID, "html": html}, nil); err != nil {
		t.Fatal(err)
	}
	if err = b.call(ctx, "Emulation.setDeviceMetricsOverride", map[string]any{"width": 520, "height": 720, "deviceScaleFactor": 1, "mobile": false}, nil); err != nil {
		t.Fatal(err)
	}
	if err = b.fill(ctx, "tester@example.invalid", "password-fixture"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	var filled bool
	if err = b.evaluate(ctx, `window.submitted===true && document.querySelector('[autocomplete=email]').value==='tester@example.invalid' && document.querySelector('[type=password]').value==='password-fixture'`, &filled); err != nil || !filled {
		t.Fatal("autofill or login submission failed")
	}
	image, crop, err := b.screenshot(ctx)
	if err != nil || !strings.HasPrefix(image, "data:image/png;base64,") || crop.Width != 408 || crop.Height != 388 {
		t.Fatalf("login card not cropped: %+v %v", crop, err)
	}
	if err = b.input(ctx, Input{Kind: "pointer", Phase: "down", X: 10, Y: 10}, crop); err != nil {
		t.Fatal(err)
	}
	if err = b.input(ctx, Input{Kind: "pointer", Phase: "up", X: 10, Y: 10}, crop); err != nil {
		t.Fatal(err)
	}
	if err = b.input(ctx, Input{Kind: "pointer", Phase: "down", X: 9999, Y: 0}, crop); err == nil {
		t.Fatal("accepted click outside login card")
	}
	b.close()
	if _, err := os.Stat(b.profile); !os.IsNotExist(err) {
		t.Fatal("isolated browser profile was not removed")
	}
}

func TestLiveDeviceCode(t *testing.T) {
	if os.Getenv("OPENLIST_TEST_PIKPAK_BROWSER") != "1" {
		t.Skip("opt-in provider initialization check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	status, data, err := api.request(ctx, http.MethodPost, "/v1/auth/device/code", randomID(), map[string]string{"client_id": clientID, "scope": "user"}, "")
	if err != nil || status != 200 || str(data, "device_code") == "" {
		t.Fatalf("device initialization failed; status=%d error=%v", status, err)
	}
	if _, err = approvalURL(data); err != nil {
		t.Fatal("official device URL failed origin validation")
	}
}

func TestDeviceSessionWithoutBrowser(t *testing.T) {
	empty := t.TempDir()
	for _, key := range []string{"PATH", "ProgramFiles", "ProgramFiles(x86)", "LOCALAPPDATA"} {
		t.Setenv(key, empty)
	}
	t.Setenv("OPENLIST_PIKPAK_BROWSER", empty+"/missing-browser")
	if _, err := browserPath(); err == nil {
		t.Fatal("browser must be unavailable for this test")
	}
	approved := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/auth/device/code":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if _, ok := body["password"]; ok {
				t.Error("device grant received a password")
			}
			fmt.Fprint(w, `{"device_code":"device-fixture","user_code":"user-fixture","interval":1,"expires_in":300}`)
		case "/v1/auth/token":
			if !approved {
				w.WriteHeader(400)
				fmt.Fprint(w, `{"error":"authorization_pending"}`)
				return
			}
			fmt.Fprint(w, `{"refresh_token":"refresh-fixture","access_token":"access-fixture","token_type":"Bearer"}`)
		case "/v1/user/me":
			fmt.Fprint(w, `{"email":"tester@example.invalid"}`)
		default:
			t.Error("unexpected request")
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	originalAPI := api
	api = service{base: server.URL, http: server.Client()}
	defer func() { api = originalAPI }()
	s, err := StartDevice(context.Background(), "tester@example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.browser != nil || !strings.HasPrefix(s.ApprovalURL(), "https://mypikpak.com/drive/qr-login?") {
		t.Fatal("device session unexpectedly needs a browser")
	}
	s.next = time.Time{}
	view, err := s.View(context.Background())
	if err != nil || view.State != "waiting" || view.Image != "" {
		t.Fatalf("invalid pending device view: %+v %v", view, err)
	}
	approved = true
	s.next = time.Time{}
	view, err = s.View(context.Background())
	if err != nil || view.RefreshToken != "refresh-fixture" || view.Platform != "pc" {
		t.Fatalf("device consent did not fill PC token: %+v %v", view, err)
	}
}
