package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A write the console did not attempt because it could not ask the cluster
// is a 503 with its own code, and never the 403 a denial is: nobody said no,
// and the two send the reader to different people.
func TestAnUnreviewableWriteIsA503AndNotARefusal(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   ProblemCode
	}{
		{fmt.Errorf("%w: could not ask", ErrUnreviewable), http.StatusServiceUnavailable, ProblemAccessUnreviewable},
		{fmt.Errorf("%w: may not", ErrRefused), http.StatusForbidden, ProblemRBACDenied},
	} {
		rec := httptest.NewRecorder()
		fail(rec, tc.err)
		var problem Problem
		if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
			t.Fatalf("%v: the body is not a problem: %s", tc.err, rec.Body.String())
		}
		if rec.Code != tc.status || problem.Code != tc.code {
			t.Errorf("%v answered %d %s, want %d %s", tc.err, rec.Code, problem.Code, tc.status, tc.code)
		}
	}
}

// A write not made because its audit record could not be written is the same
// 503 and code - the contract's "the console could not act" - under its own
// title, so nobody is sent to check whether the cluster answers reviews.
func TestAnUnrecordedWriteIsA503WithItsOwnTitle(t *testing.T) {
	rec := httptest.NewRecorder()
	fail(rec, fmt.Errorf("%w: the console did not delete pods api-1", ErrUnrecorded))
	var problem Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
		t.Fatalf("the body is not a problem: %s", rec.Body.String())
	}
	if rec.Code != http.StatusServiceUnavailable || problem.Code != ProblemAccessUnreviewable ||
		problem.Title != "Audit record could not be written" {
		t.Errorf("an unrecorded write answered %d %+v", rec.Code, problem)
	}
}

// A write that never left the console - its time ran out first, the rate limit
// held it, no connection was made - is a 503 with its own code, so a client
// branches on `code` and not on the title; and one sent with no answer back is
// a 504 with its own. Never the 403 "rbac_denied", which would report them as
// the cluster's refusal.
func TestAWriteNeverSentOrNeverAnsweredHasItsOwnCode(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   ProblemCode
		title  string
	}{
		{fmt.Errorf("%w: The eviction was not sent", ErrNotSent), http.StatusServiceUnavailable, ProblemNotSent, "Not sent"},
		{fmt.Errorf("%w: The eviction was sent and no answer came back", ErrUnanswered), http.StatusGatewayTimeout,
			ProblemNotAnswered, "Not answered"},
	} {
		rec := httptest.NewRecorder()
		fail(rec, tc.err)
		var problem Problem
		if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
			t.Fatalf("the body is not a problem: %s", rec.Body.String())
		}
		if rec.Code != tc.status || problem.Code != tc.code || problem.Title != tc.title {
			t.Errorf("%v answered %d %+v", tc.err, rec.Code, problem)
		}
	}
}
