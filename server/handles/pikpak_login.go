package handles

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/pikpaklogin"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

type pikpakBrowserLogin struct {
	owner   uint
	session *pikpaklogin.Session
	expires time.Time
}

var pikpakBrowserLogins = struct {
	sync.Mutex
	sessions map[string]*pikpakBrowserLogin
}{sessions: map[string]*pikpakBrowserLogin{}}

func pikpakLoginOwner(c *gin.Context) uint {
	return c.Request.Context().Value(conf.UserKey).(*model.User).ID
}
func pikpakLoginError(c *gin.Context, err error) {
	common.ErrorStrResp(c, "pikpak_login."+err.Error(), 400)
}

func StartPikPakBrowserLogin(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8192)
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Mode     string `json:"mode"`
	}
	if c.ShouldBindJSON(&input) != nil {
		common.ErrorStrResp(c, "pikpak_login.credentials_required", 400)
		return
	}
	if input.Mode != "" && input.Mode != "browser" && input.Mode != "device" {
		common.ErrorStrResp(c, "pikpak_login.invalid_input", 400)
		return
	}
	owner := pikpakLoginOwner(c)
	idBytes := make([]byte, 24)
	if _, err := rand.Read(idBytes); err != nil {
		common.ErrorStrResp(c, "pikpak_login.session_failed", 500)
		return
	}
	id := hex.EncodeToString(idBytes)
	entry := &pikpakBrowserLogin{owner: owner, expires: time.Now().Add(5 * time.Minute)}
	pikpakBrowserLogins.Lock()
	for key, item := range pikpakBrowserLogins.sessions {
		if time.Now().After(item.expires) {
			if item.session != nil {
				go item.session.Close()
			}
			delete(pikpakBrowserLogins.sessions, key)
		}
	}
	if len(pikpakBrowserLogins.sessions) >= 2 {
		pikpakBrowserLogins.Unlock()
		common.ErrorStrResp(c, "pikpak_login.too_many_sessions", 429)
		return
	}
	pikpakBrowserLogins.sessions[id] = entry
	pikpakBrowserLogins.Unlock()
	var session *pikpaklogin.Session
	var err error
	if input.Mode == "device" {
		session, err = pikpaklogin.StartDevice(c.Request.Context(), input.Username)
	} else {
		session, err = pikpaklogin.Start(c.Request.Context(), input.Username, input.Password)
	}
	input.Password = ""
	pikpakBrowserLogins.Lock()
	if err != nil {
		delete(pikpakBrowserLogins.sessions, id)
	} else {
		entry.session = session
	}
	pikpakBrowserLogins.Unlock()
	if err != nil {
		pikpakLoginError(c, err)
		return
	}
	time.AfterFunc(5*time.Minute, func() {
		pikpakBrowserLogins.Lock()
		if pikpakBrowserLogins.sessions[id] == entry {
			delete(pikpakBrowserLogins.sessions, id)
		}
		pikpakBrowserLogins.Unlock()
		session.Close()
	})
	common.SuccessResp(c, gin.H{"session": id, "authorization_url": session.ApprovalURL()})
}

func getPikPakBrowserLogin(c *gin.Context, id string) *pikpaklogin.Session {
	pikpakBrowserLogins.Lock()
	defer pikpakBrowserLogins.Unlock()
	item := pikpakBrowserLogins.sessions[id]
	if item == nil || item.owner != pikpakLoginOwner(c) || time.Now().After(item.expires) {
		return nil
	}
	return item.session
}

func ViewPikPakBrowserLogin(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var input struct {
		Session string `json:"session"`
	}
	if c.ShouldBindJSON(&input) != nil {
		common.ErrorStrResp(c, "pikpak_login.expired", 400)
		return
	}
	s := getPikPakBrowserLogin(c, input.Session)
	if s == nil {
		common.ErrorStrResp(c, "pikpak_login.expired", 410)
		return
	}
	view, err := s.View(c.Request.Context())
	if err != nil {
		pikpakLoginError(c, err)
		return
	}
	common.SuccessResp(c, view)
}

func InputPikPakBrowserLogin(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var input struct {
		Session string `json:"session"`
		pikpaklogin.Input
	}
	if c.ShouldBindJSON(&input) != nil {
		common.ErrorStrResp(c, "pikpak_login.invalid_input", 400)
		return
	}
	s := getPikPakBrowserLogin(c, input.Session)
	if s == nil {
		common.ErrorStrResp(c, "pikpak_login.expired", 410)
		return
	}
	if err := s.Input(c.Request.Context(), input.Input); err != nil {
		pikpakLoginError(c, err)
		return
	}
	common.SuccessResp(c)
}

func ClosePikPakBrowserLogin(c *gin.Context) {
	var input struct {
		Session string `json:"session"`
	}
	if c.ShouldBindJSON(&input) != nil {
		common.ErrorStrResp(c, "pikpak_login.invalid_input", 400)
		return
	}
	pikpakBrowserLogins.Lock()
	item := pikpakBrowserLogins.sessions[input.Session]
	if item != nil && item.owner == pikpakLoginOwner(c) {
		delete(pikpakBrowserLogins.sessions, input.Session)
	} else {
		item = nil
	}
	pikpakBrowserLogins.Unlock()
	if item != nil && item.session != nil {
		item.session.Close()
	}
	common.SuccessResp(c)
}
