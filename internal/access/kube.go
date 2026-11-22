package access

import (
	"context"

	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Clientset adapts a clientset lookup to the reviews a Gate asks: the two
// create calls of `authorization.k8s.io/v1`, and nothing else of the client.
func Clientset(lookup func(cluster string) (kubernetes.Interface, error)) func(string) (Reviews, error) {
	return func(cluster string) (Reviews, error) {
		clients, err := lookup(cluster)
		if err != nil {
			return nil, err
		}
		return clientsetReviews{clients}, nil
	}
}

type clientsetReviews struct{ clients kubernetes.Interface }

// SubjectAccessReview needs `create` on `subjectaccessreviews` - the one grant
// this whole package costs the ServiceAccount.
func (c clientsetReviews) SubjectAccessReview(ctx context.Context, review *authorizationv1.SubjectAccessReview) (*authorizationv1.SubjectAccessReview, error) {
	return c.clients.AuthorizationV1().SubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
}

// SelfSubjectAccessReview needs no grant: every authenticated identity may ask
// about itself (`system:basic-user`).
func (c clientsetReviews) SelfSubjectAccessReview(ctx context.Context, review *authorizationv1.SelfSubjectAccessReview) (*authorizationv1.SelfSubjectAccessReview, error) {
	return c.clients.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
}
