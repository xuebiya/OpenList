// Package pikpaklogin provides a short-lived, isolated browser for device consent.
package pikpaklogin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type browser struct {
	conn    *websocket.Conn
	cmd     *exec.Cmd
	profile string
	done    chan struct{}
	mu      sync.Mutex
	seq     int
	once    sync.Once
}

func browserPath() (string, error) {
	candidates := []string{os.Getenv("OPENLIST_PIKPAK_BROWSER"), "chrome", "msedge", "chromium", "chromium-browser", "google-chrome"}
	for _, root := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LOCALAPPDATA")} {
		if root != "" {
			candidates = append(candidates, filepath.Join(root, "Google", "Chrome", "Application", "chrome.exe"), filepath.Join(root, "Microsoft", "Edge", "Application", "msedge.exe"))
		}
	}
	for _, name := range candidates {
		if name == "" {
			continue
		}
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", errors.New("browser_unavailable")
}

func newBrowser(ctx context.Context) (*browser, error) {
	path, err := browserPath()
	if err != nil {
		return nil, err
	}
	profile, err := os.MkdirTemp("", "openlist-pikpak-")
	if err != nil {
		return nil, errors.New("browser_start_failed")
	}
	b := &browser{profile: profile, done: make(chan struct{})}
	b.cmd = exec.Command(path, "--headless=new", "--no-first-run", "--no-default-browser-check", "--disable-sync", "--disable-extensions", "--disable-background-networking", "--remote-debugging-address=127.0.0.1", "--remote-debugging-port=0", "--user-data-dir="+profile, "--window-size=520,720", "--lang=zh-CN", "about:blank")
	if err = b.cmd.Start(); err != nil {
		os.RemoveAll(profile)
		return nil, errors.New("browser_start_failed")
	}
	go func() { _ = b.cmd.Wait(); close(b.done) }()
	ok := false
	defer func() {
		if !ok {
			b.close()
		}
	}()
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-b.done:
			return nil, errors.New("browser_start_failed")
		default:
		}
		data, readErr := os.ReadFile(filepath.Join(profile, "DevToolsActivePort"))
		if readErr == nil {
			port := strings.SplitN(string(data), "\n", 2)[0]
			n, parseErr := strconv.Atoi(strings.TrimSpace(port))
			if parseErr == nil && n > 0 && n < 65536 {
				client := &http.Client{Timeout: 2 * time.Second}
				response, e := client.Get(fmt.Sprintf("http://127.0.0.1:%d/json/list", n))
				if e == nil {
					var targets []struct {
						Type                 string
						WebSocketDebuggerURL string
					}
					e = json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(&targets)
					response.Body.Close()
					if e == nil {
						for _, target := range targets {
							if target.Type == "page" && target.WebSocketDebuggerURL != "" {
								dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
								b.conn, _, err = dialer.DialContext(ctx, target.WebSocketDebuggerURL, nil)
								if err == nil {
									b.conn.SetReadLimit(8 * 1024 * 1024)
									ok = true
									return b, nil
								}
							}
						}
					}
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, errors.New("browser_start_failed")
}

func (b *browser) close() {
	b.once.Do(func() {
		if b.conn != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = b.call(ctx, "Browser.close", map[string]any{}, nil)
			cancel()
			_ = b.conn.Close()
		}
		if b.cmd != nil && b.cmd.Process != nil {
			select {
			case <-b.done:
			case <-time.After(2 * time.Second):
				_ = b.cmd.Process.Kill()
				select {
				case <-b.done:
				case <-time.After(3 * time.Second):
				}
			}
		}
		// Chromium's child processes may release profile locks shortly after
		// the browser process exits, especially on Windows.
		for i := 0; i < 30; i++ {
			if os.RemoveAll(b.profile) == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	})
}

func (b *browser) call(ctx context.Context, method string, params any, output any) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	deadline := time.Now().Add(25 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	_ = b.conn.SetWriteDeadline(deadline)
	_ = b.conn.SetReadDeadline(deadline)
	if err := b.conn.WriteJSON(map[string]any{"id": b.seq, "method": method, "params": params}); err != nil {
		return errors.New("browser_closed")
	}
	for {
		var message struct {
			ID     int
			Result json.RawMessage
			Error  *struct {
				Code    int
				Message string
			}
		}
		if err := b.conn.ReadJSON(&message); err != nil {
			return errors.New("browser_closed")
		}
		if message.ID != b.seq {
			continue
		}
		if message.Error != nil {
			return errors.New("browser_command_failed")
		}
		if output != nil {
			return json.Unmarshal(message.Result, output)
		}
		return nil
	}
}

func (b *browser) evaluate(ctx context.Context, expression string, out any) error {
	var response struct {
		Result           struct{ Value json.RawMessage }
		ExceptionDetails json.RawMessage
	}
	if err := b.call(ctx, "Runtime.evaluate", map[string]any{"expression": expression, "returnByValue": true, "awaitPromise": true}, &response); err != nil {
		return err
	}
	if len(response.ExceptionDetails) > 0 {
		return errors.New("login_page_changed")
	}
	if out != nil {
		return json.Unmarshal(response.Result.Value, out)
	}
	return nil
}

const officialPage = `location.protocol === 'https:' && ['mypikpak.com','www.mypikpak.com','user.mypikpak.com'].includes(location.hostname) && (!location.port || location.port === '443')`

func (b *browser) fill(ctx context.Context, username, password string) error {
	credentials, _ := json.Marshal([]string{username, password})
	expression := `(async()=>{
 const c=` + string(credentials) + `;
 const allowed=()=>` + officialPage + `;
 const visible=e=>e && e.getBoundingClientRect().width>0 && getComputedStyle(e).visibility!=='hidden';
 for(let n=0;n<100;n++){
  if(!allowed()){if(location.href==='about:blank'){await new Promise(r=>setTimeout(r,200));continue;}return 'unexpected_origin';}
  let p=[...document.querySelectorAll('input[type=password]')].find(visible);
  if(!p){const icon=document.querySelector('.quick-operation .icon-email');if(icon)icon.closest('.quick-operation').click();await new Promise(r=>setTimeout(r,200));continue;}
  const a=[...document.querySelectorAll('input[autocomplete=email],input[type=email]')].filter(visible);
  if(a.length!==1)return 'login_page_changed';
  const setter=Object.getOwnPropertyDescriptor(HTMLInputElement.prototype,'value').set;
  for(const [field,value] of [[a[0],c[0]],[p,c[1]]]){setter.call(field,value);field.dispatchEvent(new Event('input',{bubbles:true}));field.dispatchEvent(new Event('change',{bubbles:true}));}
  await new Promise(r=>setTimeout(r,150));
  if(!allowed())return 'unexpected_origin';
  const buttons=[...document.querySelectorAll('button.pp-button')].filter(e=>visible(e)&& /^(登录|Sign in|Log in)$/i.test(e.textContent.trim()));
  if(buttons.length!==1 || buttons[0].disabled)return 'login_page_changed';
  p.blur();c.fill('');setTimeout(()=>buttons[0].click(),0);return 'submitted';
 }return 'login_page_changed';})()`
	var result string
	if err := b.evaluate(ctx, expression, &result); err != nil {
		return err
	}
	if result != "submitted" {
		return errors.New(result)
	}
	return nil
}

type frame struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

func (b *browser) screenshot(ctx context.Context) (string, frame, error) {
	var f frame
	// Capture the real official login/consent card. A visible CAPTCHA expands the
	// crop to the viewport, keeping challenges usable without changing consent.
	expr := `(()=>{if(!(` + officialPage + `))return null;
 const visible=e=>{const r=e.getBoundingClientRect();return r.width>10&&r.height>10&&getComputedStyle(e).visibility!=='hidden'&&getComputedStyle(e).display!=='none'};
 const challenges=[...document.querySelectorAll('iframe')].filter(e=>visible(e)&&e.getBoundingClientRect().width>200&&e.getBoundingClientRect().height>120&&/captcha|recaptcha|turnstile|hcaptcha/i.test(e.src));
 let el=[...document.querySelectorAll('.pp-login-panel, .qr-login-content, .qr-login-container, .login-component')].find(visible);
 if(!el)el=[...document.querySelectorAll('main, #app .content, #app')].find(visible);
 let r=el?el.getBoundingClientRect():{x:0,y:0,width:innerWidth,height:innerHeight};for(const e of challenges){const c=e.getBoundingClientRect(),x=Math.min(r.x,c.x),y=Math.min(r.y,c.y);r={x,y,width:Math.max(r.x+r.width,c.x+c.width)-x,height:Math.max(r.y+r.height,c.y+c.height)-y};}
 const x=Math.max(0,r.x-4),y=Math.max(0,r.y-4);return {x,y,width:Math.max(1,Math.min(innerWidth-x,r.width+8)),height:Math.max(1,Math.min(innerHeight-y,r.height+8))};})()`
	if err := b.evaluate(ctx, expr, &f); err != nil {
		return "", f, errors.New("frame_pending")
	}
	if f.Width < 1 || f.Height < 1 {
		return "", f, errors.New("unexpected_origin")
	}
	var result struct{ Data string }
	err := b.call(ctx, "Page.captureScreenshot", map[string]any{"format": "png", "captureBeyondViewport": false, "clip": map[string]any{"x": f.X, "y": f.Y, "width": f.Width, "height": f.Height, "scale": 1}}, &result)
	if err != nil {
		return "", f, errors.New("frame_pending")
	}
	return "data:image/png;base64," + result.Data, f, err
}

type Input struct {
	Kind  string  `json:"kind"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	Phase string  `json:"phase"`
	Text  string  `json:"text"`
	Key   string  `json:"key"`
}

func (b *browser) input(ctx context.Context, in Input, f frame) error {
	switch in.Kind {
	case "pointer":
		if in.X < 0 || in.Y < 0 || in.X > f.Width || in.Y > f.Height {
			return errors.New("invalid_input")
		}
		typ := map[string]string{"down": "mousePressed", "up": "mouseReleased", "move": "mouseMoved"}[in.Phase]
		if typ == "" {
			return errors.New("invalid_input")
		}
		buttons := 0
		if in.Phase != "up" {
			buttons = 1
		}
		return b.call(ctx, "Input.dispatchMouseEvent", map[string]any{"type": typ, "x": f.X + in.X, "y": f.Y + in.Y, "button": "left", "buttons": buttons, "clickCount": 1}, nil)
	case "text":
		if len(in.Text) > 256 {
			return errors.New("invalid_input")
		}
		return b.call(ctx, "Input.insertText", map[string]any{"text": in.Text}, nil)
	case "key":
		code := map[string]int{"Backspace": 8, "Tab": 9, "Enter": 13, "Escape": 27, "ArrowLeft": 37, "ArrowUp": 38, "ArrowRight": 39, "ArrowDown": 40, "Delete": 46}[in.Key]
		if code == 0 {
			return errors.New("invalid_input")
		}
		for _, kind := range []string{"keyDown", "keyUp"} {
			if err := b.call(ctx, "Input.dispatchKeyEvent", map[string]any{"type": kind, "key": in.Key, "windowsVirtualKeyCode": code}, nil); err != nil {
				return err
			}
		}
		return nil
	}
	return errors.New("invalid_input")
}
