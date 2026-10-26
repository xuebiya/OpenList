package sign

import (
	"net/url"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/pkg/sign"
)

var once sync.Once
var instance sign.Sign

const (
	UserNameQuery      = "username"
	UserSignatureQuery = "user_sign"
)

func Sign(data string) string {
	expire := setting.GetInt(conf.LinkExpiration, 0)
	if expire == 0 {
		return NotExpired(data)
	} else {
		return WithDuration(data, time.Duration(expire)*time.Hour)
	}
}

func WithDuration(data string, d time.Duration) string {
	once.Do(Instance)
	return instance.Sign(data, time.Now().Add(d).Unix())
}

func NotExpired(data string) string {
	once.Do(Instance)
	return instance.Sign(data, 0)
}

func Verify(data string, sign string) error {
	once.Do(Instance)
	return instance.Verify(data, sign)
}

func AddUserIdentity(query url.Values, data, username string) {
	if username == "" {
		return
	}
	query.Set(UserNameQuery, username)
	query.Set(UserSignatureQuery, SignUserIdentity(data, username))
}

func SignUserIdentity(data, username string) string {
	return Sign(data + "\x00" + username)
}

func VerifyUserIdentity(data, username, signature string) error {
	if username == "" || signature == "" {
		return sign.ErrSignInvalid
	}
	return Verify(data+"\x00"+username, signature)
}

func Instance() {
	instance = sign.NewHMACSign([]byte(setting.GetStr(conf.Token)))
}
