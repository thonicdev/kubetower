package redis

import (
	"encoding/json"
	"strings"
	"testing"
)

// A credential marshals as where the password came from, never the password:
// the page is told the Secret and the key, and only the session uses the value.
func TestAMarshalledCredentialCarriesNoPassword(t *testing.T) {
	const password = "a-known-redis-password-that-must-not-leave"
	c := Credential{Secret: "redis-auth", Key: "password", Source: "container redis of pod redis-0", Pod: "redis-0", password: password}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), password) {
		t.Errorf("the marshalled credential carries its password: %s", raw)
	}
	if !strings.Contains(string(raw), "redis-auth") {
		t.Errorf("the marshalled credential lost its Secret's name, so the check saw nothing: %s", raw)
	}
}
