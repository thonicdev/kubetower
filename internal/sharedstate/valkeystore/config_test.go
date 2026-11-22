package valkeystore

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// The store's configuration never marshals with its password. Today Config
// does not marshal at all (its clock is a func), so the `json:"-"` tag is what
// keeps the password out the day that changes; both halves are held here.
func TestAMarshalledConfigCarriesNoPassword(t *testing.T) {
	const password = "a-known-valkey-password-that-must-not-leave"
	cfg := Config{Address: "valkey:6379", Username: "kubetower", Password: password, Prefix: "kt:"}
	raw, err := json.Marshal(cfg)
	if err == nil && strings.Contains(string(raw), password) {
		t.Errorf("the marshalled configuration carries its password: %s", raw)
	}
	field, ok := reflect.TypeFor[Config]().FieldByName("Password")
	if !ok {
		t.Fatal("Config has no Password field, so the check saw nothing")
	}
	if tag := field.Tag.Get("json"); tag != "-" {
		t.Errorf("Config.Password is tagged json:%q; it must be \"-\" so no encoder carries it", tag)
	}
}
