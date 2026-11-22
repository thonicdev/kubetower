package report

import (
	"strings"
	"testing"
)

// The two sentences about a refused read speak to the reader, who is the
// kubeconfig's user on a desktop and the signed-in person in a cluster, and so
// never name a kubeconfig.
func TestTheStateSentencesNameNoKubeconfig(t *testing.T) {
	served := func(kind, unavailable string, objects int) Area {
		return Area{Kind: kind, Served: true, Unavailable: unavailable, Objects: objects}
	}
	for name, areas := range map[string][]Area{
		"unreadable": {served(ApplicationKind, "forbidden", 0)},
		"pending":    {served(ApplicationKind, "", 0)},
	} {
		_, sentence := state(areas)
		if sentence == "" || strings.Contains(sentence, "kubeconfig") {
			t.Errorf("%s: %q must be a sentence that names no kubeconfig", name, sentence)
		}
		if !strings.Contains(sentence, "you") {
			t.Errorf("%s: %q does not speak to the reader", name, sentence)
		}
	}
}
