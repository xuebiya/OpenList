package handles

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/server/common"
	driver115 "github.com/SheltonZhu/115driver/pkg/driver"
	"github.com/gin-gonic/gin"
)

const qr115SessionLifetime = 5 * time.Minute

type qr115Login struct {
	session *driver115.QRCodeSession
	app     driver115.LoginApp
	expires time.Time
	mu      sync.Mutex
}

var qr115Logins = struct {
	sync.Mutex
	sessions map[string]*qr115Login
}{sessions: make(map[string]*qr115Login)}

func qr115App(source string) (driver115.LoginApp, bool) {
	switch source {
	case "web", "android", "ios", "tv", "alipaymini", "wechatmini", "qandroid":
		return driver115.LoginApp(source), true
	default:
		return "", false
	}
}

func qr115ImageURL(source, uid string) string {
	return fmt.Sprintf("https://qrcodeapi.115.com/api/1.0/%s/1.0/qrcode?uid=%s", source, url.QueryEscape(uid))
}

func cleanQR115Logins(now time.Time) {
	for id, login := range qr115Logins.sessions {
		if !now.Before(login.expires) {
			delete(qr115Logins.sessions, id)
		}
	}
}

func removeQR115Login(id string, login *qr115Login) {
	qr115Logins.Lock()
	if qr115Logins.sessions[id] == login {
		delete(qr115Logins.sessions, id)
	}
	qr115Logins.Unlock()
}

// Start115QRCodeLogin starts an admin-only 115 login. The selected source is
// used for both the QR image and the device associated with the cookie.
func Start115QRCodeLogin(c *gin.Context) {
	var input struct {
		Source string `json:"source"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		common.ErrorStrResp(c, "invalid QR code request", 400)
		return
	}
	app, ok := qr115App(input.Source)
	if !ok {
		common.ErrorStrResp(c, "unsupported 115 QR code source", 400)
		return
	}

	client := driver115.New()
	client.Client.SetTimeout(15 * time.Second)
	session, err := client.QRCodeStart()
	if err != nil || session == nil || session.UID == "" || session.Sign == "" || session.Time == 0 {
		common.ErrorStrResp(c, "failed to start 115 QR code login", 502)
		return
	}

	imageRequest, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, qr115ImageURL(input.Source, session.UID), nil)
	if err != nil {
		common.ErrorStrResp(c, "failed to request 115 QR code", 500)
		return
	}
	imageClient := &http.Client{Timeout: 15 * time.Second}
	imageResponse, err := imageClient.Do(imageRequest)
	if err != nil {
		common.ErrorStrResp(c, "failed to fetch 115 QR code", 502)
		return
	}
	defer imageResponse.Body.Close()
	if imageResponse.StatusCode != http.StatusOK || !strings.HasPrefix(imageResponse.Header.Get("Content-Type"), "image/png") {
		common.ErrorStrResp(c, "115 QR code image is unavailable for this source", 502)
		return
	}
	image, err := io.ReadAll(io.LimitReader(imageResponse.Body, 1024*1024+1))
	if err != nil || len(image) == 0 || len(image) > 1024*1024 {
		common.ErrorStrResp(c, "invalid 115 QR code image", 502)
		return
	}

	idBytes := make([]byte, 24)
	if _, err := rand.Read(idBytes); err != nil {
		common.ErrorStrResp(c, "failed to create QR code session", 500)
		return
	}
	id := base64.RawURLEncoding.EncodeToString(idBytes)
	qr115Logins.Lock()
	cleanQR115Logins(time.Now())
	if len(qr115Logins.sessions) >= 64 {
		qr115Logins.Unlock()
		common.ErrorStrResp(c, "too many 115 QR code sessions", 429)
		return
	}
	qr115Logins.sessions[id] = &qr115Login{session: session, app: app, expires: time.Now().Add(qr115SessionLifetime)}
	qr115Logins.Unlock()

	c.Header("Cache-Control", "no-store")
	common.SuccessResp(c, gin.H{
		"session": id,
		"image":   "data:image/png;base64," + base64.StdEncoding.EncodeToString(image),
	})
}

// Get115QRCodeLoginStatus returns the cookie only after the QR code has been
// scanned and approved. The cookie is never stored in the session map.
func Get115QRCodeLoginStatus(c *gin.Context) {
	var input struct {
		Session string `json:"session"`
	}
	if err := c.ShouldBindJSON(&input); err != nil || input.Session == "" {
		common.ErrorStrResp(c, "invalid QR code session", 400)
		return
	}
	c.Header("Cache-Control", "no-store")
	qr115Logins.Lock()
	cleanQR115Logins(time.Now())
	login := qr115Logins.sessions[input.Session]
	qr115Logins.Unlock()
	if login == nil {
		common.ErrorStrResp(c, "115 QR code expired; please refresh", 410)
		return
	}

	login.mu.Lock()
	defer login.mu.Unlock()
	if !time.Now().Before(login.expires) {
		removeQR115Login(input.Session, login)
		common.SuccessResp(c, gin.H{"status": -1})
		return
	}
	client := driver115.New()
	client.Client.SetTimeout(15 * time.Second)
	status, err := client.QRCodeStatus(login.session)
	if err != nil || status == nil {
		common.ErrorStrResp(c, "failed to check 115 QR code status", 502)
		return
	}
	switch status.Status {
	case 0, 1:
		common.SuccessResp(c, gin.H{"status": status.Status})
	case 2:
		credential, err := client.QRCodeLoginWithApp(login.session, login.app)
		if err != nil || credential == nil || credential.UID == "" || credential.CID == "" || credential.SEID == "" {
			common.ErrorStrResp(c, "failed to obtain 115 login cookie", 502)
			return
		}
		cookie := fmt.Sprintf("UID=%s;CID=%s;SEID=%s;KID=%s", credential.UID, credential.CID, credential.SEID, credential.KID)
		removeQR115Login(input.Session, login)
		common.SuccessResp(c, gin.H{"status": 2, "cookie": cookie})
	case -1, -2:
		removeQR115Login(input.Session, login)
		common.SuccessResp(c, gin.H{"status": status.Status})
	default:
		common.ErrorStrResp(c, "unexpected 115 QR code status", 502)
	}
}
