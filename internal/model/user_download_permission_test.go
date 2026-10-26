package model

import "testing"

func TestCanDownload(t *testing.T) {
	if CanDownload(0) {
		t.Fatal("download permission should be disabled when bit 16 is clear")
	}
	if !CanDownload(1 << 16) {
		t.Fatal("download permission should be enabled when bit 16 is set")
	}
	if CanDownload(1 << 15) {
		t.Fatal("share ID permission must not imply download permission")
	}
}
