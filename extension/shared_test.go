package extension

import (
	"net/http/httptest"
	"testing"
)

func TestSharedConsoleIsFalseUnlessMarked(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	if SharedConsole(r) || SharedConsole(nil) {
		t.Error("an unmarked request reads as shared")
	}
	if !SharedConsole(MarkShared(r)) {
		t.Error("a marked request reads as not shared")
	}
	if SharedConsole(r) {
		t.Error("marking changed the original request")
	}
}
