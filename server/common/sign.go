package common

import (
	"net/url"
	stdpath "path"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/internal/sign"
)

func Sign(obj model.Obj, parent string, encrypt bool, usernames ...string) string {
	if obj.IsDir() {
		return ""
	}
	rawPath := stdpath.Join(parent, obj.GetName())
	fileSign := ""
	if encrypt || setting.GetBool(conf.SignAll) {
		fileSign = sign.Sign(rawPath)
	}
	if len(usernames) > 0 {
		return appendMediaUserIdentity(fileSign, rawPath, usernames[0])
	}
	return fileSign
}

func appendMediaUserIdentity(fileSign, rawPath, username string) string {
	if username == "" || !IsMediaFile(rawPath) {
		return fileSign
	}
	return withMediaUserIdentity(fileSign, username, sign.SignUserIdentity(rawPath, username))
}

func withMediaUserIdentity(fileSign, username, userSign string) string {
	if username == "" || userSign == "" {
		return fileSign
	}
	query := url.Values{}
	query.Set(sign.UserNameQuery, username)
	query.Set(sign.UserSignatureQuery, userSign)
	return fileSign + "&" + query.Encode()
}
