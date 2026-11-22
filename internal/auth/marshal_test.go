package auth

import (
	"encoding/json"
	"strings"
	"testing"
)

// The identity provider's configuration marshals without its client secret,
// whoever encodes it: the secret is the console's credential at the provider.
func TestAMarshalledOIDCConfigCarriesNoClientSecret(t *testing.T) {
	const secret = "a-known-client-secret-that-must-not-leave"
	cfg := OIDCConfig{
		Issuer:       "https://idp.test/dex",
		ClientID:     "kubetower",
		ClientSecret: secret,
		RedirectURL:  "https://console.test" + CallbackPath,
	}
	for name, v := range map[string]any{"value": cfg, "pointer": &cfg} {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.Contains(string(raw), secret) {
			t.Errorf("the marshalled configuration (%s) carries its client secret: %s", name, raw)
		}
		if !strings.Contains(string(raw), "kubetower") {
			t.Errorf("the marshalled configuration (%s) lost its client id, so the check saw nothing: %s", name, raw)
		}
	}
}
