package health

import "strings"

// Credential says whether a failed read failed at the credential step, before
// any request reached the API server, and how: StateSSO when the error carries
// an SSO session's own words (the AWS SDK signing an EKS context in this
// process, or a plugin that printed them), StatePlugin when an exec plugin ran
// and refused with its words lost (client-go keeps only the exit code). Empty
// for anything else, a bare 401 included: Denied answers that one.
//
// A read and the probe fail the same way when a session lapses, but the probe's
// verdict is only as recent as its last round, and an `ok` verdict is believed
// for a while. Without this the reads of a cluster whose session lapsed after
// its last probe say "the cluster did not answer", one panel at a time, when
// every read is refused for one reason the person fixes in one command.
//
// Only an error that went through the credential step counts: client-go's
// "getting credentials:" around an exec plugin, or internal/ekstoken's
// "signing a token for" around the SDK. An object named `sso-session` that is
// not found carries the words and none of the steps.
func Credential(err error) string {
	if err == nil {
		return ""
	}
	message := withoutRequestURL(err.Error())
	lower := strings.ToLower(message)
	if !contains(lower, "getting credentials", "signing a token for") {
		return ""
	}
	if ssoWorded(message) {
		return StateSSO
	}
	if strings.Contains(lower, "failed with exit code") {
		return StatePlugin
	}
	return ""
}
