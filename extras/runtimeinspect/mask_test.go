package runtimeinspect

import (
	"strings"
	"testing"
)

func TestSecretLookingNames(t *testing.T) {
	for name, want := range map[string]bool{
		"spring.datasource.password":     true,
		"javax.net.ssl.keyStorePassword": true,
		"db.password":                    true,
		"DB_PASSWD":                      true,
		"oauth.client-secret":            true,
		"github.token":                   true,
		"aws.credentials.file":           true,
		"stripe.apiKey":                  true,
		"api_key":                        true,
		"api-key":                        true,
		"tls.key":                        true,
		"key.store.pass":                 true,
		"privateKeyPath":                 true,
		"java.version":                   false,
		"user.dir":                       false,
		"java.home":                      false,
		"keyboard.layout":                false,
		"javax.net.ssl.keyStore":         false,
		"monkey":                         false,
	} {
		if got := secretName(name); got != want {
			t.Errorf("secretName(%q) = %v, want %v", name, got, want)
		}
	}
}

// Q6: the value of a secret-looking option is hidden in every form, and the
// rest of the command line is drawn as it is.
func TestCommandLineSecretsAreMasked(t *testing.T) {
	args := maskArgs(strings.Fields("-XX:NativeMemoryTracking=summary -Dspring.datasource.password=hunter2 -Xmx512m " +
		"-Djavax.net.ssl.keyStorePassword=changeit -Djava.version=x -Duser.dir=/app --db.password=hunter2 " +
		"--token hunter2 -jar app.jar"))
	text := joined(args)
	if strings.Contains(text, "hunter2") || strings.Contains(text, "changeit") {
		t.Fatalf("a secret is drawn: %s", text)
	}
	for _, want := range []string{"-XX:NativeMemoryTracking=summary", "-Dspring.datasource.password=" + masked,
		"-Djavax.net.ssl.keyStorePassword=" + masked, "-Djava.version=x", "-Duser.dir=/app",
		"--db.password=" + masked, "--token " + masked, "-jar app.jar"} {
		if !strings.Contains(text, want) {
			t.Errorf("%q is not in %s", want, text)
		}
	}
	var secrets []string
	for _, a := range args {
		if a.Secret != "" {
			secrets = append(secrets, a.Secret)
		}
	}
	if strings.Join(secrets, ",") != "spring.datasource.password,javax.net.ssl.keyStorePassword,db.password,token" {
		t.Errorf("secrets = %v", secrets)
	}
	tokens := strings.Fields("-Dspring.datasource.password=hunter2 --token s3cr3t -Djava.version=x")
	if v, ok := argValue(tokens, "token"); !ok || v != "s3cr3t" {
		t.Errorf("token = %q %v", v, ok)
	}
	if v, ok := argValue(tokens, "spring.datasource.password"); !ok || v != "hunter2" {
		t.Errorf("password = %q %v", v, ok)
	}
	// A reveal is for a value the page hid, not a way to read any option.
	if _, ok := argValue(tokens, "java.version"); ok {
		t.Error("a value that was never masked is revealed")
	}
}

func TestPropertiesAreMaskedByKeyAndInCommandLines(t *testing.T) {
	for _, c := range []struct {
		p      property
		value  string
		masked bool
	}{
		{property{"spring.datasource.password", "hunter2"}, masked, true},
		{property{"javax.net.ssl.keyStorePassword", "changeit"}, masked, true},
		{property{"sun.java.command", "D --db.password=hunter2"}, "D --db.password=" + masked, true},
		{property{"java.version", "21.0.13"}, "21.0.13", false},
		{property{"user.dir", "/"}, "/", false},
	} {
		got := maskProperty(c.p)
		if got.Value != c.value || got.Masked != c.masked || got.Key != c.p.Key {
			t.Errorf("%+v -> %+v", c.p, got)
		}
	}
}
