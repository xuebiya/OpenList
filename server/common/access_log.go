package common

import (
	"fmt"
	stdpath "path"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/sign"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

const (
	AccessTypePreview  = "在线预览"
	AccessTypeDownload = "下载"
	AccessTypePlayer   = "播放器"
	mediaAccessQuery   = "media_access"
	accessDedupeWindow = 20 * time.Second
)

var (
	imageExtensions = map[string]struct{}{
		"apng": {}, "avif": {}, "bmp": {}, "cr2": {}, "dng": {}, "gif": {}, "heic": {},
		"heif": {}, "ico": {}, "jpe": {}, "jpeg": {}, "jpg": {}, "jxl": {}, "nef": {},
		"png": {}, "raw": {}, "svg": {}, "tif": {}, "tiff": {}, "webp": {},
	}
	videoExtensions = map[string]struct{}{
		"3g2": {}, "3gp": {}, "asf": {}, "avi": {}, "divx": {}, "f4v": {}, "flv": {},
		"m2ts": {}, "m2v": {}, "m4v": {}, "mkv": {}, "mov": {}, "mp4": {}, "mpe": {},
		"mpeg": {}, "mpg": {}, "mts": {}, "ogm": {}, "ogv": {}, "qt": {}, "rm": {},
		"rmvb": {}, "ts": {}, "vob": {}, "webm": {}, "wmv": {}, "xvid": {},
	}
	accessCacheMu sync.Mutex
	accessCache   = make(map[string]time.Time)
)

func IsMediaFile(filename string) bool {
	ext := strings.TrimPrefix(strings.ToLower(stdpath.Ext(filename)), ".")
	if _, ok := imageExtensions[ext]; ok {
		return true
	}
	_, ok := videoExtensions[ext]
	return ok
}

func LogMediaAccess(c *gin.Context, rawPath string, username ...string) {
	if len(username) > 0 && username[0] != "" {
		logMediaAccess(c, rawPath, AccessTypePreview, username[0])
		return
	}
	logMediaAccess(c, rawPath, AccessTypePreview, mediaAccessUsername(c, rawPath))
}

func LogMediaAccessWithType(c *gin.Context, rawPath, accessType string) {
	logMediaAccess(c, rawPath, accessType, mediaAccessUsername(c, rawPath))
}

func LogMediaAccessAuto(c *gin.Context, rawPath string) {
	logMediaAccess(c, rawPath, detectMediaAccessType(c), mediaAccessUsername(c, rawPath))
}

func logMediaAccess(c *gin.Context, rawPath, accessType, username string) {
	if !IsMediaFile(rawPath) || isMediaThumbnailRequest(c) {
		return
	}
	clientIP := "unknown"
	if c != nil {
		clientIP = c.ClientIP()
	}
	username = cleanAccessLogField(username)
	if !shouldLogMediaAccess(clientIP, username, rawPath, time.Now()) {
		return
	}

	line := fmt.Sprintf("时间：%s 访问IP：%s 用户：%s 行为：%s 访问路径：%s",
		time.Now().Format("2006年01月02日 15:04:05"),
		cleanAccessLogField(clientIP), username, accessType, cleanAccessLogField(rawPath))
	log.WithFields(log.Fields{
		"type":        "media_access",
		"ip":          clientIP,
		"user":        username,
		"access_type": accessType,
		"path":        rawPath,
	}).Info("[媒体访问] " + line)
	// Keep access records visible even when Log.Enable redirects Logrus to a file.
	fmt.Println("[媒体访问] " + line)
}

func mediaAccessUsername(c *gin.Context, rawPath string) string {
	if c != nil && c.Request != nil {
		contextUser, _ := c.Request.Context().Value(conf.UserKey).(*model.User)
		query := c.Request.URL.Query()
		username := query.Get(sign.UserNameQuery)
		signatureValid := username != "" && sign.VerifyUserIdentity(rawPath, username, query.Get(sign.UserSignatureQuery)) == nil
		return preferredMediaUsername(contextUser, username, signatureValid)
	}
	return "Guest"
}

func preferredMediaUsername(contextUser *model.User, signedUsername string, signatureValid bool) string {
	if contextUser != nil && !contextUser.IsGuest() && contextUser.Username != "" {
		return contextUser.Username
	}
	if signatureValid && signedUsername != "" {
		return signedUsername
	}
	if contextUser != nil && contextUser.Username != "" {
		return contextUser.Username
	}
	return "Guest"
}

func detectMediaAccessType(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return AccessTypeDownload
	}
	userAgent := strings.ToLower(c.Request.UserAgent())
	if hasMediaAgent(userAgent,
		"vlc", "mpv", "potplayer", "mpc-hc", "mpc-be", "kodi", "plex", "infuse", "iina",
		"nplayer", "oplayer", "avplayer", "kmplayer", "gom", "daum", "lavf", "ffmpeg",
		"libmpv", "exoplayer", "stagefright", "android.media", "quicktime", "windows-media",
		"mxplayer", "emby", "jellyfin", "applecoremedia", "video player",
	) {
		return AccessTypePlayer
	}
	if hasMediaAgent(userAgent, "wget", "curl", "aria2", "internet download manager", "free download manager", "motrix", "xunlei") {
		return AccessTypeDownload
	}
	query := c.Request.URL.Query()
	switch strings.ToLower(query.Get(mediaAccessQuery)) {
	case "preview":
		return AccessTypePreview
	case "download":
		return AccessTypeDownload
	}
	for _, key := range []string{"download", "dl", "attachment"} {
		if value, ok := query[key]; ok && (len(value) == 0 || value[0] == "" || value[0] == "1" || strings.EqualFold(value[0], "true")) {
			return AccessTypeDownload
		}
	}
	if mode := strings.ToLower(query.Get("type")); mode == "download" || mode == "attachment" {
		return AccessTypeDownload
	}

	requestPath := c.Request.URL.Path
	if conf.URL != nil {
		basePath := strings.TrimSuffix(conf.URL.Path, "/")
		if basePath != "" && (requestPath == basePath || strings.HasPrefix(requestPath, basePath+"/")) {
			requestPath = strings.TrimPrefix(requestPath, basePath)
		}
	}
	switch {
	case strings.HasPrefix(requestPath, "/d/"), strings.HasPrefix(requestPath, "/ad/"), strings.HasPrefix(requestPath, "/sd/"):
		return AccessTypeDownload
	default:
		return AccessTypePreview
	}
}

func isMediaThumbnailRequest(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	return strings.EqualFold(c.Request.URL.Query().Get(mediaAccessQuery), "thumbnail")
}

func shouldLogMediaAccess(clientIP, username, rawPath string, now time.Time) bool {
	key := strings.Join([]string{clientIP, username, rawPath}, "\x00")
	accessCacheMu.Lock()
	defer accessCacheMu.Unlock()
	if last, ok := accessCache[key]; ok && now.Sub(last) < accessDedupeWindow {
		return false
	}
	accessCache[key] = now
	if len(accessCache) > 1000 {
		for cachedKey, last := range accessCache {
			if now.Sub(last) > 2*accessDedupeWindow {
				delete(accessCache, cachedKey)
			}
		}
	}
	return true
}

func hasMediaAgent(userAgent string, agents ...string) bool {
	for _, agent := range agents {
		if strings.Contains(userAgent, agent) {
			return true
		}
	}
	return false
}

func cleanAccessLogField(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
}
