package resources

import (
	"context"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
)

// TestEveryKindsListQuestionIsTheOneItsListAsks: listQuestions spells each
// kind's review a second time, so it is held to the first. Every descriptor's
// list is run across every namespace, against a review that records what it
// was asked and lets everything through, and the question it asked must be the
// one the table says. A kind moved to another API version, or a new kind with
// no entry, fails here rather than in a menu that hides or offers the wrong
// thing on the in-cluster console.
func TestEveryKindsListQuestionIsTheOneItsListAsks(t *testing.T) {
	for _, d := range Kinds() {
		t.Run(d.Kind, func(t *testing.T) {
			want, ok := d.ListQuestion()
			if !ok {
				t.Fatalf("%s has no list question: add it to listQuestions", d.Kind)
			}
			reviews := &accesstest.Reviews{}
			reader := Reader{Gate: reviews.Gate(), Cluster: "test", Clients: fake.NewSimpleClientset()}
			if _, _, err := d.list(context.Background(), reader, ""); err != nil {
				t.Fatalf("listing %s: %v", d.Kind, err)
			}
			asked := reviews.Asked()
			if len(asked) == 0 {
				t.Fatalf("listing %s asked no review", d.Kind)
			}
			if asked[0].Attributes != want.Attrs {
				t.Errorf("%s's list asked %s; listQuestions says %s", d.Kind, asked[0].Attributes, want.Attrs)
			}
			if want.Namespaced != d.Namespaced {
				t.Errorf("%s: listQuestions says namespaced=%v, the descriptor %v", d.Kind, want.Namespaced, d.Namespaced)
			}
		})
	}
}
