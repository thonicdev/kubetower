package jvm

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// An image is Java when its name says a Java runtime, a JRE or a JDK, or a
// product known to run on one.
func TestAJavaImageIsRecognisedByItsName(t *testing.T) {
	for _, image := range []string{
		"openjdk:17",
		"eclipse-temurin:21-jre",
		"docker.io/library/eclipse-temurin:21-jdk-alpine@sha256:0123",
		"amazoncorretto:21",
		"azul/zulu-openjdk:17",
		"bellsoft/liberica-openjre-alpine:21",
		"ibm-semeru-runtimes:open-17-jre",
		"ghcr.io/graalvm/jdk-community:21",
		"gcr.io/distroless/java17-debian12",
		"registry.example.com:5000/acme/billing:1.4-jre",
		"maven:3.9-eclipse-temurin-21",
		"acme/billing-jdk17:2",
		"bitnami/kafka:3.7",
		"confluentinc/cp-kafka:7.6.0",
		"docker.elastic.co/elasticsearch/elasticsearch:8.13.0",
		"opensearchproject/opensearch:2",
		"quay.io/keycloak/keycloak:24.0",
		"jenkins/jenkins:lts-jdk17",
		"cassandra:5",
		"solr:9",
		"zookeeper:3.9",
		"sonatype/nexus3:3.68.0",
		"sonarqube:10-community",
	} {
		h, ok := imageHint(image)
		if !ok {
			t.Errorf("%s: not recognized as Java", image)
			continue
		}
		if h.Source != "image" || !strings.Contains(h.Reason, image) {
			t.Errorf("%s: hint %+v does not name the image", image, h)
		}
	}
}

// An image whose name only resembles one is not Java: an exporter beside a
// Java product is a Go binary, OpenSearch's dashboards are Node.js, and a word
// that merely starts like a product is another program.
func TestAnImageThatOnlyResemblesJavaIsNot(t *testing.T) {
	for _, image := range []string{
		"nginx:1.27",
		"registry.example.com/acme/billing:1.4",
		"prometheuscommunity/elasticsearch-exporter:v1.7.0",
		"danielqsj/kafka-exporter:latest",
		"opensearchproject/opensearch-dashboards:2",
		"edenhill/kafkacat:1.6.0",
		"node:20-alpine",
		"python:3.12-slim",
		"golang:1.23",
		"javascript-runner:1", // "java" inside a word is not a runtime
		"acme/jdkless:1",      // nor is a word that only starts with jdk
		"",
	} {
		if h, ok := imageHint(image); ok {
			t.Errorf("%s: recognized as Java (%+v)", image, h)
		}
	}
}

// A command line in the spec that starts java, runs a jar or carries a JVM
// option is Java, also inside `sh -c`.
func TestACommandLineThatStartsAJVMIsRecognised(t *testing.T) {
	for _, tc := range []struct {
		command, args []string
		says          string
	}{
		{[]string{"java"}, []string{"-jar", "app.jar"}, "starts"},
		{[]string{"/opt/java/openjdk/bin/java"}, nil, "starts"},
		{nil, []string{"-jar", "/app/app.jar"}, "-jar"},
		{[]string{"/app/run"}, []string{"--classpath", "/app/lib/a.jar:/app/lib/b.jar"}, "jar"},
		{[]string{"sh", "-c", "exec java -Xmx512m -cp /app Main"}, nil, "starts"},
		{nil, []string{"-Xmx1g"}, "heap"},
		{nil, []string{"-XX:+UseG1GC"}, "JVM option"},
	} {
		h, ok := commandHint(commandTokens(tc.command, tc.args))
		if !ok || h.Source != "command" || !strings.Contains(h.Reason, tc.says) {
			t.Errorf("%v %v: hint %+v, %v, want one saying %q", tc.command, tc.args, h, ok, tc.says)
		}
	}
}

// A command line that is not one is not Java: a word containing "java", a
// JavaScript file, a script with no JVM in it.
func TestACommandLineWithoutAJVMIsNot(t *testing.T) {
	for _, tc := range [][]string{
		{"node", "server.js"},
		{"python", "-m", "javadoc_builder"},
		{"/bin/sh", "-c", "echo hello"},
		{"nginx", "-g", "daemon off;"},
		{"./javascript-bundle"},
	} {
		if h, ok := commandHint(commandTokens(tc, nil)); ok {
			t.Errorf("%v: recognized as Java (%+v)", tc, h)
		}
	}
}

// The Java environment variables are each a hint; JAVA_OPTS too, saying that
// the JVM does not read it.
func TestTheJavaEnvironmentIsAHint(t *testing.T) {
	for _, name := range []string{"JAVA_TOOL_OPTIONS", "JDK_JAVA_OPTIONS", "JAVA_HOME", "JAVA_OPTS"} {
		got := hints(corev1.Container{Image: "acme/app:1", Env: env(name, "x")})
		if len(got) != 1 || got[0].Source != "env" || !strings.Contains(got[0].Reason, name) {
			t.Errorf("%s: hints %+v, want one env hint naming it", name, got)
		}
	}
	opts := hints(corev1.Container{Env: env("JAVA_OPTS", "-Xmx1g")})
	if !strings.Contains(opts[0].Reason, "does not read") {
		t.Errorf("JAVA_OPTS hint %q does not say the JVM ignores it", opts[0].Reason)
	}
	// Other variables are no evidence.
	if got := hints(corev1.Container{Image: "acme/app:1", Env: env("JAVA_VERSION_UNUSED", "1", "PATH", "/usr/bin")}); len(got) != 0 {
		t.Errorf("unrelated variables gave hints %+v", got)
	}
}

// Every hint is kept, cheapest first, so the page can say all of what the
// guess rests on.
func TestEveryHintIsKeptInOrder(t *testing.T) {
	got := hints(corev1.Container{
		Image: "eclipse-temurin:21-jre", Command: []string{"java"}, Env: env("JAVA_TOOL_OPTIONS", "-Xmx1g"),
	})
	var sources []string
	for _, h := range got {
		sources = append(sources, h.Source)
	}
	if strings.Join(sources, ",") != "image,command,env" {
		t.Errorf("hint sources = %v, want image, command, env", sources)
	}
}

// Kubernetes substitutes $(NAME) in a command line from the container's
// environment, so a JVM option passed that way is seen.
func TestASubstitutedCommandLineIsRead(t *testing.T) {
	c := corev1.Container{Command: []string{"/app/start"}, Args: []string{"$(OPTS)", "$$(KEPT)"}, Env: env("OPTS", "-Xmx256m")}
	tokens := commandLine(c)
	if strings.Join(tokens, " ") != "/app/start -Xmx256m $(KEPT)" {
		t.Errorf("command line = %v", tokens)
	}
	if !probablyJava(c) {
		t.Error("an -Xmx substituted into the arguments was not seen")
	}
}
