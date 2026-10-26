package sign

import (
	"net/url"
	"sync"
	"testing"

	pkgSign "github.com/OpenListTeam/OpenList/v4/pkg/sign"
)

func TestUserIdentitySignature(t *testing.T) {
	previousInstance := instance
	defer func() {
		instance = previousInstance
		once = sync.Once{}
	}()

	once.Do(func() {})
	instance = pkgSign.NewHMACSign([]byte("test-token"))

	query := url.Values{}
	query.Set(UserNameQuery, "alice")
	query.Set(UserSignatureQuery, instance.Sign("/videos/sample.mp4\x00alice", 0))
	if query.Get(UserNameQuery) != "alice" || query.Get(UserSignatureQuery) == "" {
		t.Fatalf("user identity query = %#v", query)
	}
	if err := VerifyUserIdentity("/videos/sample.mp4", "alice", query.Get(UserSignatureQuery)); err != nil {
		t.Fatalf("valid identity rejected: %v", err)
	}
	if err := VerifyUserIdentity("/videos/sample.mp4", "bob", query.Get(UserSignatureQuery)); err == nil {
		t.Fatal("identity signature accepted for a different username")
	}
}
