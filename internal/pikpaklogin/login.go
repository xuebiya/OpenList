package pikpaklogin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const clientID = "YvtoWO6GNHiuCl7x" // Same PC client as the supplied --browser-login script.

type service struct {
	base string
	http *http.Client
}

var api = service{base: "https://user.mypikpak.com", http: &http.Client{Timeout: 20 * time.Second}}

func randomID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

func (a service) request(ctx context.Context, method, path, device string, payload any, access string) (int, map[string]json.RawMessage, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, body)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Language", "zh-CN")
	req.Header.Set("x-client-id", clientID)
	req.Header.Set("x-sdk-version", "8.1.4")
	req.Header.Set("x-protocol-version", "301")
	req.Header.Set("x-device-id", device)
	req.Header.Set("x-device-sign", "wdi10."+device+strings.Repeat("x", 32))
	if access != "" {
		req.Header.Set("Authorization", "Bearer "+access)
	}
	response, err := a.http.Do(req)
	if err != nil {
		return 0, nil, errors.New("provider_unavailable")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024+1))
	if err != nil || len(raw) > 1024*1024 {
		return 0, nil, errors.New("invalid_provider_response")
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(raw, &data) != nil || data == nil {
		return 0, nil, errors.New("invalid_provider_response")
	}
	return response.StatusCode, data, nil
}

func str(data map[string]json.RawMessage, key string) string {
	var value string
	_ = json.Unmarshal(data[key], &value)
	return value
}
func seconds(data map[string]json.RawMessage, key string, fallback int) int {
	var number int
	if json.Unmarshal(data[key], &number) == nil && number > 0 {
		return number
	}
	return fallback
}

func approvalURL(data map[string]json.RawMessage) (string, error) {
	link := str(data, "verification_uri_complete")
	code := str(data, "user_code")
	if link == "" {
		if code == "" {
			return "", errors.New("invalid_provider_response")
		}
		scope := str(data, "scope")
		if scope == "" {
			scope = "user"
		}
		link = "https://mypikpak.com/drive/qr-login?" + url.Values{"client_id": {clientID}, "scope": {scope}, "user_code": {code}}.Encode()
	}
	u, err := url.Parse(link)
	if err != nil {
		return "", errors.New("unexpected_origin")
	}
	if u.Scheme != "https" || (u.Host != "mypikpak.com" && u.Host != "www.mypikpak.com" && u.Host != "mypikpak.com:443" && u.Host != "www.mypikpak.com:443") || u.User != nil || u.Path != "/drive/qr-login" || strings.ContainsAny(link, "\r\n") {
		return "", errors.New("unexpected_origin")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", errors.New("unexpected_origin")
	}
	for _, key := range []string{"client_id", "scope", "user_code"} {
		if len(q[key]) > 1 {
			return "", errors.New("unexpected_origin")
		}
	}
	if v := q.Get("client_id"); v != "" && v != clientID {
		return "", errors.New("unexpected_origin")
	}
	if code != "" && q.Get("user_code") != code {
		return "", errors.New("unexpected_origin")
	}
	return link, nil
}

type View struct {
	State        string  `json:"state"`
	Image        string  `json:"image,omitempty"`
	Width        float64 `json:"width,omitempty"`
	Height       float64 `json:"height,omitempty"`
	RefreshToken string  `json:"refresh_token,omitempty"`
	DeviceID     string  `json:"device_id,omitempty"`
	Platform     string  `json:"platform,omitempty"`
}

type Session struct {
	mu                    sync.Mutex
	browser               *browser
	ctx                   context.Context
	cancel                context.CancelFunc
	code, device, account string
	approval              string
	next                  time.Time
	interval              time.Duration
	expires               time.Time
	crop                  frame
	terminal              error
	completed             *View
	timer                 *time.Timer
}

func Start(ctx context.Context, account, password string) (*Session, error) {
	account = strings.TrimSpace(account)
	if !strings.Contains(account, "@") || len(account) > 320 || len(password) == 0 || len(password) > 4096 {
		return nil, errors.New("credentials_required")
	}
	// Verify availability before creating a provider device code.
	if _, err := browserPath(); err != nil {
		return nil, err
	}
	device := randomID()
	if device == "" {
		return nil, errors.New("session_failed")
	}
	status, data, err := api.request(ctx, http.MethodPost, "/v1/auth/device/code", device, map[string]string{"client_id": clientID, "scope": "user"}, "")
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK || str(data, "device_code") == "" {
		return nil, errors.New("device_authorization_failed")
	}
	link, err := approvalURL(data)
	if err != nil {
		return nil, err
	}
	b, err := newBrowser(ctx)
	if err != nil {
		return nil, err
	}
	lifetime := time.Duration(min(300, seconds(data, "expires_in", 300))) * time.Second
	sessionCtx, cancel := context.WithTimeout(context.Background(), lifetime)
	s := &Session{browser: b, ctx: sessionCtx, cancel: cancel, device: device, code: str(data, "device_code"), account: account, expires: time.Now().Add(lifetime), interval: time.Duration(min(30, seconds(data, "interval", 2))) * time.Second}
	s.timer = time.AfterFunc(lifetime, s.Close)
	ok := false
	defer func() {
		password = ""
		if !ok {
			s.Close()
		}
	}()
	if err = b.call(ctx, "Page.enable", map[string]any{}, nil); err != nil {
		return nil, err
	}
	if err = b.call(ctx, "Emulation.setDeviceMetricsOverride", map[string]any{"width": 520, "height": 720, "deviceScaleFactor": 1, "mobile": false}, nil); err != nil {
		return nil, err
	}
	if err = b.call(ctx, "Page.navigate", map[string]string{"url": link}, nil); err != nil {
		return nil, err
	}
	ready := false
	for i := 0; i < 100; i++ {
		if ctx.Err() != nil || sessionCtx.Err() != nil {
			return nil, errors.New("expired")
		}
		if err = b.evaluate(ctx, "("+officialPage+") && document.readyState !== 'loading'", &ready); err == nil && ready {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !ready {
		return nil, errors.New("login_page_changed")
	}
	if err = b.fill(ctx, account, password); err != nil {
		return nil, err
	}
	s.next = time.Now().Add(s.interval)
	ok = true
	return s, nil
}

func (s *Session) Close() {
	s.cancel()
	if s.timer != nil {
		s.timer.Stop()
	}
	if s.browser != nil {
		s.browser.close()
	}
}

// StartDevice uses the user's current browser for official device consent.
// The server never launches a browser or receives a password in this mode.
func StartDevice(ctx context.Context, account string) (*Session, error) {
	return startDevice(ctx, account, api)
}

func startDevice(ctx context.Context, account string, a service) (*Session, error) {
	account = strings.TrimSpace(account)
	if !strings.Contains(account, "@") || len(account) > 320 {
		return nil, errors.New("email_required")
	}
	device := randomID()
	if device == "" {
		return nil, errors.New("session_failed")
	}
	status, data, err := a.request(ctx, http.MethodPost, "/v1/auth/device/code", device, map[string]string{"client_id": clientID, "scope": "user"}, "")
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK || str(data, "device_code") == "" {
		return nil, errors.New("device_authorization_failed")
	}
	link, err := approvalURL(data)
	if err != nil {
		return nil, err
	}
	lifetime := time.Duration(min(300, seconds(data, "expires_in", 300))) * time.Second
	sessionCtx, cancel := context.WithTimeout(context.Background(), lifetime)
	interval := time.Duration(min(30, seconds(data, "interval", 2))) * time.Second
	s := &Session{ctx: sessionCtx, cancel: cancel, code: str(data, "device_code"), device: device, account: account, approval: link, expires: time.Now().Add(lifetime), interval: interval, next: time.Now().Add(interval)}
	s.timer = time.AfterFunc(lifetime, s.Close)
	return s, nil
}

func (s *Session) ApprovalURL() string { return s.approval }

func (a service) verifyAccount(ctx context.Context, account, device string, data map[string]json.RawMessage) error {
	access := str(data, "access_token")
	kind := str(data, "token_type")
	if access == "" || strings.ContainsAny(access, "\r\n") || (kind != "" && !strings.EqualFold(kind, "Bearer")) {
		return errors.New("account_unverified")
	}
	status, profile, err := a.request(ctx, http.MethodGet, "/v1/user/me", device, nil, access)
	if err != nil || status != http.StatusOK || str(profile, "email") == "" {
		return errors.New("account_unverified")
	}
	if !strings.EqualFold(strings.TrimSpace(str(profile, "email")), account) {
		return errors.New("account_mismatch")
	}
	return nil
}

func (s *Session) poll(ctx context.Context, a service) error {
	if time.Now().Before(s.next) {
		return nil
	}
	status, data, err := a.request(ctx, http.MethodPost, "/v1/auth/token", s.device, map[string]string{"client_id": clientID, "grant_type": "urn:ietf:params:oauth:grant-type:device_code", "device_code": s.code}, "")
	s.next = time.Now().Add(s.interval)
	if err != nil {
		return err
	}
	if status == http.StatusOK {
		token := str(data, "refresh_token")
		if token == "" {
			return errors.New("missing_refresh_token")
		}
		if err = a.verifyAccount(ctx, s.account, s.device, data); err != nil {
			return err
		}
		s.completed = &View{State: "complete", RefreshToken: token, DeviceID: s.device, Platform: "pc"}
		if s.browser != nil {
			s.browser.close()
		}
		return nil
	}
	switch str(data, "error") {
	case "authorization_pending":
		return nil
	case "slow_down":
		s.interval = min(30*time.Second, s.interval*2)
		s.next = time.Now().Add(s.interval)
		return nil
	case "expired_token":
		return errors.New("expired")
	case "access_denied":
		return errors.New("denied")
	default:
		return errors.New("authorization_failed")
	}
}

func (s *Session) View(ctx context.Context) (View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil || time.Now().After(s.expires) {
		return View{}, errors.New("expired")
	}
	if s.terminal != nil {
		return View{}, s.terminal
	}
	if s.completed != nil {
		return *s.completed, nil
	}
	if err := s.poll(ctx, api); err != nil {
		s.terminal = err
		if s.browser != nil {
			s.browser.close()
		}
		return View{}, err
	}
	if s.completed != nil {
		return *s.completed, nil
	}
	if s.browser == nil {
		return View{State: "waiting"}, nil
	}
	image, crop, err := s.browser.screenshot(ctx)
	if err != nil && err.Error() == "frame_pending" {
		// Login and CAPTCHA redirects may replace the execution context between
		// frames. Keep the session alive and capture again on the next poll.
		return View{State: "waiting"}, nil
	}
	if err != nil {
		return View{}, err
	}
	s.crop = crop
	return View{State: "waiting", Image: image, Width: crop.Width, Height: crop.Height}, nil
}

func (s *Session) Input(ctx context.Context, in Input) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil || s.completed != nil || s.terminal != nil || s.browser == nil {
		return errors.New("expired")
	}
	var allowed bool
	if err := s.browser.evaluate(ctx, officialPage, &allowed); err != nil || !allowed {
		return errors.New("unexpected_origin")
	}
	return s.browser.input(ctx, in, s.crop)
}
