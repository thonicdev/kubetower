package main

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestParseForward(t *testing.T) {
	got, err := parseForward("dex=dex/svc/dex:5556:5556@127.0.0.1,host.docker.internal")
	if err != nil {
		t.Fatal(err)
	}
	want := forward{Name: "dex", Namespace: "dex", Resource: "svc/dex", Local: 5556, Remote: 5556,
		Addresses: []string{"127.0.0.1", "host.docker.internal"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}

	// No address list is loopback alone, never kubectl's own default, which
	// would also be loopback today - said here rather than inherited.
	got, err = parseForward("console=kubetower/svc/kubetower:8824:5823")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Addresses, []string{"127.0.0.1"}) {
		t.Errorf("default addresses %v, want [127.0.0.1]", got.Addresses)
	}

	for _, bad := range []string{
		"", "nameonly", "=ns/svc/x:1:2", "x=svc:1:2", "x=ns/svc/x:1", "x=ns/svc/x:0:2",
		"x=ns/svc/x:1:70000", "x=ns/svc/x:a:2", "x=ns/svc/x:1:2@", "x=ns/svc/x:1:2@,",
		// Every interface, in any spelling.
		"x=ns/svc/x:1:2@0.0.0.0", "x=ns/svc/x:1:2@::", "x=ns/svc/x:1:2@127.0.0.1,0:0::0",
	} {
		if _, err := parseForward(bad); err == nil {
			t.Errorf("parseForward(%q) accepted", bad)
		}
	}
}

// A host name is resolved to what kubectl's --address accepts, duplicates are
// dropped, and an address that is not loopback is said out loud: it is the one
// that puts a forward on the network.
func TestResolveSaysWhenAForwardLeavesLoopback(t *testing.T) {
	f := forward{Name: "dex", Local: 5556, Addresses: []string{"127.0.0.1", "host.docker.internal", "nowhere.invalid"}}
	lookup := func(host string) ([]string, error) {
		switch host {
		case "host.docker.internal":
			return []string{"198.51.100.21", "127.0.0.1"}, nil
		default:
			return nil, errors.New("no such host")
		}
	}
	ips, notes := f.resolve(lookup)
	if !reflect.DeepEqual(ips, []string{"127.0.0.1", "198.51.100.21"}) {
		t.Errorf("ips %v", ips)
	}
	joined := strings.Join(notes, "\n")
	if !strings.Contains(joined, "198.51.100.21:5556") || !strings.Contains(joined, "not loopback") {
		t.Errorf("no note that the forward is on the network: %q", joined)
	}
	if !strings.Contains(joined, "nowhere.invalid does not resolve") {
		t.Errorf("no note for the name that does not resolve: %q", joined)
	}

	loopbackOnly := forward{Name: "console", Local: 8824, Addresses: []string{"127.0.0.1", "::1", "localhost"}}
	if ips, notes := loopbackOnly.resolve(lookup); len(notes) != 0 || len(ips) != 3 {
		t.Errorf("loopback forward: ips %v notes %v", ips, notes)
	}
}

// A literal address off loopback is said as a name
// resolving to one is, and a name resolving to every interface is not bound.
func TestResolveRefusesEveryInterfaceAndNamesLiteralAddresses(t *testing.T) {
	lookup := func(host string) ([]string, error) {
		if host == "wildcard.test" {
			return []string{"0.0.0.0", "::"}, nil
		}
		return nil, errors.New("no such host")
	}
	f := forward{Name: "dex", Local: 5556, Addresses: []string{"127.0.0.1", "198.51.100.21", "fd00::1", "wildcard.test"}}
	ips, notes := f.resolve(lookup)
	if !reflect.DeepEqual(ips, []string{"127.0.0.1", "198.51.100.21", "fd00::1"}) {
		t.Errorf("ips %v", ips)
	}
	joined := strings.Join(notes, "\n")
	for _, want := range []string{
		"198.51.100.21 is not loopback: dex is reachable from this machine's network on 198.51.100.21:5556",
		"fd00::1 is not loopback: dex is reachable from this machine's network on [fd00::1]:5556",
		"wildcard.test resolves to 0.0.0.0 here, which is every interface, so dex is not bound there",
		"wildcard.test resolves to :: here, which is every interface, so dex is not bound there",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("no note %q in:\n%s", want, joined)
		}
	}
}

func TestScanLinesJoinsSplitWritesAndDropsBlankLines(t *testing.T) {
	var got []string
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() { scanLines(pr, func(s string) { got = append(got, s) }); close(done) }()
	for _, chunk := range []string{"Forwarding from 127.0", ".0.1:8824 -> 5823\r\n\nHandling", " connection for 8824\n", "error: lost"} {
		if _, err := pw.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	pw.Close()
	<-done
	want := []string{"Forwarding from 127.0.0.1:8824 -> 5823", "Handling connection for 8824", "error: lost"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestKindOf(t *testing.T) {
	for line, want := range map[string]int{
		"Handling connection for 8824":              chatter,
		"Forwarding from 127.0.0.1:8824 -> 5823":    listening,
		"Forwarding from [::1]:8824 -> 5823":        listening,
		"E1011 error copying from local connection": other,
		"error: lost connection to pod":             other,
	} {
		if got := kindOf(line); got != want {
			t.Errorf("kindOf(%q) = %d, want %d", line, got, want)
		}
	}
}
