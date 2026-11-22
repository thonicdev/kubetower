package ekstoken

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A provider holding a minted token, and the identity it diagnosed, marshal
// without that token: it is a bearer credential to the cluster for its whole
// life, and a value that reaches a log or an API answer through an encoder
// must not carry it.
func TestAMarshalledProviderCarriesNoToken(t *testing.T) {
	const minted = tokenPrefix + "a-known-presigned-url-that-must-not-leave"
	p := &Provider{
		spec:   Spec{Cluster: "prod-eks-1", Region: "eu-west-3", Profile: "prod-devops"},
		token:  minted,
		minted: time.Now(),
		identity: Identity{
			ARN:     "arn:aws:sts::123456789012:assumed-role/ops/alice",
			Account: "123456789012",
			Source:  "SSOProvider",
		},
	}
	for name, v := range map[string]any{
		"provider":      p,
		"identity":      p.identity,
		"round tripper": &RoundTripper{Provider: p},
	} {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, forbidden := range []string{minted, strings.TrimPrefix(minted, tokenPrefix)} {
			if strings.Contains(string(raw), forbidden) {
				t.Errorf("the marshalled %s carries the token: %s", name, raw)
			}
		}
	}
}
