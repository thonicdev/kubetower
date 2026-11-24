package runtimeinspect

import (
	"path"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/thonicdev/kubetower/textutil"
)

// Hint is one reason to think a container runs a JVM, read from its spec.
type Hint struct {
	// Source is where it was read: `image`, `command` or `env`.
	Source string `json:"source"`
	// Reason is the sentence the page prints: what was seen, quoted.
	Reason string `json:"reason"`
}

// The environment variables that say a container is Java. The first two are
// read by the JVM (JAVA_TOOL_OPTIONS, by every JVM) or by its `java` launcher
// (JDK_JAVA_OPTIONS, JDK 9 and later); JAVA_HOME is where a JDK or a JRE was
// installed. JAVA_OPTS is only a launcher scripts' convention (Tomcat, Spring
// Boot's script): measured, a JVM started with it set ignores it, so it says
// the container is Java and its options are never counted as read (heap.go).
const (
	envToolOptions = "JAVA_TOOL_OPTIONS"
	envJDKOptions  = "JDK_JAVA_OPTIONS"
	envJavaHome    = "JAVA_HOME"
	envJavaOpts    = "JAVA_OPTS"
)

// runtimeImages are the names of the Java runtime images: a repository path
// or a tag that contains one is built on a JDK or a JRE (`maven:3.9-eclipse-temurin-21`). Matched inside the path, so
// `azul/zulu-openjdk`, `bellsoft/liberica-openjre-alpine`,
// `ibm-semeru-runtimes` and `gcr.io/distroless/java17-debian12` all answer.
var runtimeImages = []string{
	"openjdk", "openjre", "eclipse-temurin", "amazoncorretto", "zulu", "liberica", "semeru", "graalvm",
	"distroless/java",
}

// javaProducts are well-known images of software written in Java: their
// entrypoint starts a JVM though nothing in the spec says so. Matched as a
// word of the image's last path segment, a trailing version number allowed
// (`nexus3`), so `bitnami/kafka` and `confluentinc/cp-kafka` answer and
// `kafkacat` does not. The list ages; a product missing from it is a false
// negative, which the page says it may have.
var javaProducts = []string{
	"kafka", "elasticsearch", "opensearch", "keycloak", "jenkins", "cassandra", "solr", "zookeeper", "nexus",
	"sonarqube",
}

// notTheProduct are words that, beside a product's name, make the image
// something else that serves it: an exporter written in Go, OpenSearch's
// dashboards in Node.js.
var notTheProduct = map[string]bool{"exporter": true, "dashboards": true}

// hints are the reasons to think c runs a JVM, cheapest first: its image, its
// command line, its environment. None means nothing in the spec says so,
// which is not "not Java" (package comment).
func hints(c corev1.Container) []Hint {
	var out []Hint
	if h, ok := imageHint(c.Image); ok {
		out = append(out, h)
	}
	if h, ok := commandHint(commandLine(c)); ok {
		out = append(out, h)
	}
	for _, e := range c.Env {
		switch e.Name {
		case envToolOptions:
			out = append(out, Hint{Source: "env", Reason: envToolOptions + " is set: every JVM reads it at start-up"})
		case envJDKOptions:
			out = append(out, Hint{Source: "env", Reason: envJDKOptions + " is set: the java launcher reads it at start-up"})
		case envJavaHome:
			out = append(out, Hint{Source: "env", Reason: envJavaHome + " is set"})
		case envJavaOpts:
			out = append(out, Hint{Source: "env", Reason: envJavaOpts + " is set: a launch script's convention, which the JVM itself does not read"})
		}
	}
	return out
}

// imageHint is what the image's name says: a Java runtime image, an image
// tagged or named -jre or -jdk, or a known Java product.
func imageHint(image string) (Hint, bool) {
	repo, tag := splitImage(image)
	if repo == "" {
		return Hint{}, false
	}
	for _, name := range runtimeImages {
		if strings.Contains(repo, name) || strings.Contains(tag, name) {
			return Hint{Source: "image", Reason: "the image " + quote(image) + " is a Java runtime image (" + name + ")"}, true
		}
	}
	last := path.Base(repo)
	for _, word := range append(words(last), words(tag)...) {
		if isJREOrJDK(word) {
			return Hint{Source: "image", Reason: "the image " + quote(image) + " names a " + strings.ToUpper(word[:3])}, true
		}
	}
	lastWords := words(last)
	for _, word := range lastWords {
		if notTheProduct[word] {
			return Hint{}, false
		}
	}
	for _, word := range lastWords {
		bare := strings.TrimRight(word, "0123456789")
		for _, product := range javaProducts {
			if bare == product {
				return Hint{Source: "image", Reason: "the image " + quote(image) + " is " + product + ", which runs on a JVM"}, true
			}
		}
	}
	return Hint{}, false
}

// commandHint is what the command line in the spec says. Each element is
// split on spaces too, so `sh -c "exec java -Xmx512m -jar app.jar"` is read.
// Empty when the spec carries no command: the image's own entrypoint is not
// in the API, which is the common case.
func commandHint(tokens []string) (Hint, bool) {
	for _, token := range tokens {
		switch {
		case token == "java" || strings.HasSuffix(token, "/java"):
			return Hint{Source: "command", Reason: "the command line starts " + quote(token)}, true
		case token == "-jar":
			return Hint{Source: "command", Reason: "the command line runs a jar (-jar)"}, true
		case strings.HasSuffix(token, ".jar") || strings.Contains(token, ".jar:") || strings.Contains(token, ".jar;"):
			return Hint{Source: "command", Reason: "the command line names a jar: " + quote(clip(token))}, true
		case strings.HasPrefix(token, "-Xmx"):
			return Hint{Source: "command", Reason: "the command line sets the heap: " + quote(token)}, true
		case strings.HasPrefix(token, "-XX:"):
			return Hint{Source: "command", Reason: "the command line carries a JVM option: " + quote(clip(token))}, true
		}
	}
	return Hint{}, false
}

// commandLine is c's command then its args, with Kubernetes' $(NAME)
// substitution made (heap.go's expand), split on white space.
func commandLine(c corev1.Container) []string {
	env := map[string]corev1.EnvVar{}
	for _, e := range c.Env {
		env[e.Name] = e
	}
	return commandTokens(expand(c.Command, env), expand(c.Args, env))
}

// commandTokens is the command then the args, each split on white space.
func commandTokens(command, args []string) []string {
	var out []string
	for _, part := range append(append([]string(nil), command...), args...) {
		for _, token := range strings.Fields(part) {
			out = append(out, strings.Trim(token, `"'`))
		}
	}
	return out
}

// splitImage is an image reference's repository path and tag, lower case,
// with no digest. `registry:5000/acme/app` has no tag: a colon before the
// last slash is a port.
func splitImage(image string) (repo, tag string) {
	ref := strings.ToLower(strings.TrimSpace(image))
	if at := strings.Index(ref, "@"); at >= 0 {
		ref = ref[:at]
	}
	if colon := strings.LastIndex(ref, ":"); colon > strings.LastIndex(ref, "/") {
		return ref[:colon], ref[colon+1:]
	}
	return ref, ""
}

// words splits an image name or a tag on its separators.
func words(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == '-' || r == '_' || r == '.' })
}

// isJREOrJDK is a word that names a runtime: `jre`, `jdk`, `jre17`, `jdk8u392`.
func isJREOrJDK(word string) bool {
	for _, prefix := range []string{"jre", "jdk"} {
		if rest, ok := strings.CutPrefix(word, prefix); ok && (rest == "" || (rest[0] >= '0' && rest[0] <= '9')) {
			return true
		}
	}
	return false
}

// probablyJava is whether anything in c's spec says it runs a JVM.
func probablyJava(c corev1.Container) bool { return len(hints(c)) > 0 }

func quote(s string) string { return "“" + s + "”" }

// clip keeps a token printable: a class path can run to kilobytes.
func clip(s string) string { return textutil.Clip(s, 80) }
