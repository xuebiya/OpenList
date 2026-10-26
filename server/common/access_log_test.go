package common

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

func TestMediaAccessLogWritesConsoleAndLogger(t *testing.T) {
	oldStdout := os.Stdout
	oldOutput := log.StandardLogger().Out
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	var logOutput bytes.Buffer
	os.Stdout = w
	log.SetOutput(&logOutput)
	t.Cleanup(func() {
		os.Stdout = oldStdout
		log.SetOutput(oldOutput)
		_ = r.Close()
		_ = w.Close()
	})

	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodGet, "/d/videos/console-test.mp4", nil)
	context.Request.RemoteAddr = "203.0.113.7:4567"
	logMediaAccess(context, "/videos/console-test.mp4", AccessTypeDownload, "alice")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = oldStdout
	consoleOutput, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(consoleOutput), "[媒体访问] 时间：") ||
		!strings.Contains(string(consoleOutput), "访问IP：203.0.113.7 用户：alice 行为：下载 访问路径：/videos/console-test.mp4") {
		t.Fatalf("console output = %q", consoleOutput)
	}
	if !strings.Contains(logOutput.String(), "[媒体访问] 时间：") {
		t.Fatalf("logrus output = %q", logOutput.String())
	}
	t.Logf("MODIFIED_CONSOLE_MEDIA_LOG status=%d console=%q logrus_contains_record=true", context.Writer.Status(), strings.TrimSpace(string(consoleOutput)))
}

func TestPlayerAndDownloadRequestsProduceOneRecord(t *testing.T) {
	previousStdout := os.Stdout
	previousLogOutput := log.StandardLogger().Out
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	var logOutput bytes.Buffer
	os.Stdout = writePipe
	log.SetOutput(&logOutput)
	t.Cleanup(func() {
		os.Stdout = previousStdout
		log.SetOutput(previousLogOutput)
		_ = readPipe.Close()
		_ = writePipe.Close()
	})

	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodGet, "/d/videos/player-dedupe-test.mp4", nil)
	context.Request.RemoteAddr = "203.0.113.8:4567"
	logMediaAccess(context, "/videos/player-dedupe-test.mp4", AccessTypePlayer, "alice")
	logMediaAccess(context, "/videos/player-dedupe-test.mp4", AccessTypeDownload, "alice")
	if err := writePipe.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = previousStdout
	consoleOutput, err := io.ReadAll(readPipe)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(consoleOutput), "[媒体访问]") != 1 || !strings.Contains(string(consoleOutput), "行为：播放器") || strings.Contains(string(consoleOutput), "行为：下载") {
		t.Fatalf("console output = %q, want one player record", consoleOutput)
	}
	if strings.Count(logOutput.String(), "[媒体访问]") != 1 {
		t.Fatalf("logrus output = %q, want one record", logOutput.String())
	}
	t.Logf("MODIFIED_PLAYER_DEDUP console=%q logrus_records=1", strings.TrimSpace(string(consoleOutput)))
}

func TestDetectMediaAccessType(t *testing.T) {
	for _, tt := range []struct {
		name      string
		path      string
		userAgent string
		want      string
	}{
		{name: "direct download", path: "/d/video.mp4", userAgent: "Mozilla/5.0", want: AccessTypeDownload},
		{name: "proxy preview", path: "/p/video.mp4", userAgent: "Mozilla/5.0", want: AccessTypePreview},
		{name: "marked preview", path: "/d/video.mp4?media_access=preview", userAgent: "Mozilla/5.0", want: AccessTypePreview},
		{name: "player", path: "/d/video.mp4", userAgent: "VLC/3.0", want: AccessTypePlayer},
		{name: "download client", path: "/p/video.mp4", userAgent: "curl/8.0", want: AccessTypeDownload},
	} {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, tt.path, nil)
			request.Header.Set("User-Agent", tt.userAgent)
			context, _ := gin.CreateTestContext(httptest.NewRecorder())
			context.Request = request
			if got := detectMediaAccessType(context); got != tt.want {
				t.Fatalf("detectMediaAccessType() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMediaThumbnailRequestIsNotLogged(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/d/images/0000.jpg?media_access=thumbnail", nil)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request
	if !isMediaThumbnailRequest(context) {
		t.Fatal("thumbnail marker was not recognized")
	}
	if got := detectMediaAccessType(context); got != AccessTypeDownload {
		t.Fatalf("thumbnail request classification = %q, want route default to remain download", got)
	}
}

func TestMediaAccessMarkersLogOnlyClickedPreview(t *testing.T) {
	previousStdout := os.Stdout
	previousLogOutput := log.StandardLogger().Out
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writePipe
	log.SetOutput(io.Discard)
	t.Cleanup(func() {
		os.Stdout = previousStdout
		log.SetOutput(previousLogOutput)
		_ = readPipe.Close()
		_ = writePipe.Close()
	})

	thumbnail, _ := gin.CreateTestContext(httptest.NewRecorder())
	thumbnail.Request = httptest.NewRequest(http.MethodGet, "/d/images/0001.jpg?media_access=thumbnail", nil)
	logMediaAccess(thumbnail, "/images/0001.jpg", detectMediaAccessType(thumbnail), "alice")

	clicked, _ := gin.CreateTestContext(httptest.NewRecorder())
	clicked.Request = httptest.NewRequest(http.MethodGet, "/d/images/0000.jpg?media_access=preview", nil)
	clicked.Request.RemoteAddr = "203.0.113.9:4567"
	logMediaAccess(clicked, "/images/0000.jpg", detectMediaAccessType(clicked), "alice")

	if err := writePipe.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = previousStdout
	output, err := io.ReadAll(readPipe)
	if err != nil {
		t.Fatal(err)
	}
	text := string(output)
	if strings.Count(text, "[媒体访问]") != 1 || !strings.Contains(text, "行为："+AccessTypePreview) || !strings.Contains(text, "访问路径：/images/0000.jpg") || strings.Contains(text, "0001.jpg") {
		t.Fatalf("marker output = %q", text)
	}
}

func TestMediaAccessFilteringAndDeduplication(t *testing.T) {
	if IsMediaFile("/docs/readme.pdf") || !IsMediaFile("/images/cover.JPEG") || !IsMediaFile("/videos/movie.mkv") {
		t.Fatal("media extension filtering returned an unexpected result")
	}
	now := time.Now()
	if !shouldLogMediaAccess("192.0.2.1", "alice", "/videos/movie.mkv", now) {
		t.Fatal("first access should be logged")
	}
	if shouldLogMediaAccess("192.0.2.1", "alice", "/videos/movie.mkv", now.Add(time.Second)) {
		t.Fatal("same-file requests with different access behavior should be deduplicated")
	}
	if !shouldLogMediaAccess("192.0.2.1", "bob", "/videos/movie.mkv", now.Add(time.Second)) {
		t.Fatal("a different user should have an independent access record")
	}
}

func TestSignedUsernameOverridesGuestContext(t *testing.T) {
	guest := &model.User{Username: "guest", Role: model.GUEST}
	alice := &model.User{Username: "alice", Role: model.GENERAL}
	if got := preferredMediaUsername(guest, "alice", true); got != "alice" {
		t.Fatalf("signed username with guest context = %q, want alice", got)
	}
	if got := preferredMediaUsername(alice, "other", true); got != "alice" {
		t.Fatalf("authenticated context username = %q, want alice", got)
	}
	if got := preferredMediaUsername(guest, "alice", false); got != "guest" {
		t.Fatalf("invalid signed username fallback = %q, want guest", got)
	}
}

func TestMediaSignCarriesSignedIdentityInLegacySignField(t *testing.T) {
	got := withMediaUserIdentity("file-sign", "alice@example.com", "hmac-marker")
	query, err := url.ParseQuery("sign=" + got)
	if err != nil {
		t.Fatalf("parse generated query fragment: %v", err)
	}
	if query.Get("sign") != "file-sign" {
		t.Fatalf("file signature = %q", query.Get("sign"))
	}
	if query.Get("username") != "alice@example.com" || query.Get("user_sign") != "hmac-marker" {
		t.Fatalf("identity query = %#v", query)
	}

	withoutFileSign := withMediaUserIdentity("", "alice", "image-hmac")
	query, err = url.ParseQuery("sign=" + withoutFileSign)
	if err != nil {
		t.Fatalf("parse unsigned media query fragment: %v", err)
	}
	if query.Get("sign") != "" || query.Get("username") != "alice" || query.Get("user_sign") != "image-hmac" {
		t.Fatalf("unsigned media identity query = %#v", query)
	}
	for _, tt := range []struct {
		fileSign string
		username string
		userSign string
		want     string
	}{
		{fileSign: "file-sign", username: "alice", want: "file-sign"},
		{fileSign: "file-sign", userSign: "marker", want: "file-sign"},
	} {
		if got := withMediaUserIdentity(tt.fileSign, tt.username, tt.userSign); got != tt.want {
			t.Errorf("withMediaUserIdentity(%q, %q, %q) = %q, want %q", tt.fileSign, tt.username, tt.userSign, got, tt.want)
		}
	}
}
