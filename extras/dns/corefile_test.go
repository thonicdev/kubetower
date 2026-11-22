package dns

import (
	"errors"
	"reflect"
	"testing"

	"github.com/thonicdev/kubetower/extension"
)

const kindCorefile = `.:53 {
    errors
    health {
       lameduck 5s
    }
    ready
    kubernetes cluster.local in-addr.arpa ip6.arpa {
       pods insecure
       fallthrough in-addr.arpa ip6.arpa
       ttl 30
    }
    prometheus :9153
    forward . /etc/resolv.conf {
       max_concurrent 1000
    }
    cache 30
    loop
    reload
    loadbalance
}
failing.test:53 {
    forward . 192.0.2.1
}
`

const multiCorefile = `(common) {
    errors   # log errors
    cache 30
}
. {
    import common
    forward . 8.8.8.8 8.8.4.4:5353 tls://1.1.1.1 {
        policy sequential
        health_check 5s
    }
}
corp.example.org:53 internal.example.org:53 {
    import common
    forward corp.example.org [2001:db8::53]:53 2001:db8::54 dns://10.1.0.2
}
`

func TestParseCorefile(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want []forward
	}{
		{"empty", "", nil},
		{"no forward", ".:53 {\n  errors\n  cache 30\n}\n", nil},
		{"kind, resolv.conf and an unreachable zone", kindCorefile, []forward{
			{Zone: ".", Targets: []string{resolvConf}},
			{Zone: "failing.test", Targets: []string{"192.0.2.1:53"}},
		}},
		{"several targets, ports, tls, ipv6, a snippet and comments", multiCorefile, []forward{
			{Zone: ".", Targets: []string{"8.8.8.8:53", "8.8.4.4:5353", "1.1.1.1:853"}},
			{Zone: "corp.example.org", Targets: []string{"[2001:db8::53]:53", "[2001:db8::54]:53", "10.1.0.2:53"}},
		}},
		{"a forward outside any block is not read", "forward . 1.1.1.1\n", nil},
		{"a commented forward is not read", ".:53 {\n  # forward . 1.1.1.1\n}\n", nil},
		{"a scheme it does not know is left out", ".:53 {\n  forward . grpc://x:1 9.9.9.9\n}\n", []forward{
			{Zone: ".", Targets: []string{"9.9.9.9:53"}},
		}},
		{"brace glued to the last word", ".:53 {\n  forward . 9.9.9.9{\n  }\n}\n", []forward{
			{Zone: ".", Targets: []string{"9.9.9.9:53"}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseCorefile(tc.text); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseCorefile = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestMergeConfiguredListsTheSilentUpstream(t *testing.T) {
	have := []Upstream{{To: "172.18.0.1:53", QPS: 4, Answers: true}}
	got := mergeConfigured(have, parseCorefile(kindCorefile))
	byTo := map[string]Upstream{}
	for _, u := range got {
		byTo[u.To] = u
	}
	if len(got) != 2 {
		t.Fatalf("upstreams = %+v; want the answering one and the silent one, resolv.conf explained by the first", got)
	}
	if u := byTo["192.0.2.1:53"]; u.Answers || !reflect.DeepEqual(u.Zones, []string{"failing.test"}) {
		t.Errorf("silent upstream = %+v", u)
	}
	if u := byTo["172.18.0.1:53"]; !u.Answers || u.QPS != 4 {
		t.Errorf("answering upstream = %+v; its figures must stay", u)
	}
}

func TestMergeConfiguredKeepsFiguresAndAddsZones(t *testing.T) {
	have := []Upstream{{To: "8.8.8.8:53", QPS: 5, Answers: true}, {To: "1.1.1.1:853", HealthPS: 2}}
	got := mergeConfigured(have, parseCorefile(multiCorefile))
	byTo := map[string]Upstream{}
	for _, u := range got {
		byTo[u.To] = u
	}
	if len(got) != 6 {
		t.Fatalf("got %d upstreams: %+v", len(got), got)
	}
	if u := byTo["8.8.8.8:53"]; u.QPS != 5 || !u.Answers || !reflect.DeepEqual(u.Zones, []string{"."}) {
		t.Errorf("8.8.8.8 = %+v", u)
	}
	if u := byTo["1.1.1.1:853"]; u.Answers || u.HealthPS != 2 {
		t.Errorf("a health-failing one keeps its failures: %+v", u)
	}
	if u := byTo["8.8.4.4:5353"]; u.Answers || u.QPS != 0 {
		t.Errorf("a configured one with no series = %+v", u)
	}
}

func TestMergeConfiguredShowsResolvConfWhenNothingElseIsKnown(t *testing.T) {
	got := mergeConfigured(nil, parseCorefile(kindCorefile))
	if len(got) != 2 || got[0].To != resolvConf && got[1].To != resolvConf {
		t.Fatalf("got %+v; a resolv.conf forward with no known upstream must be listed", got)
	}
}

func TestStatsListsAnUpstreamOnlyTheCorefileNames(t *testing.T) {
	h := &fakeHost{answer: read(), corefile: ".:53 {\n  forward . 8.8.8.8 192.0.2.1\n}\n"}
	rec, v := get(t, h, "")
	if rec.Code != 200 {
		t.Fatalf("code %d", rec.Code)
	}
	if len(h.listed) != 1 || h.listed[0].Namespace != "kube-system" || h.listed[0].FieldSelector != "metadata.name=coredns" ||
		h.listed[0].GVR.Resource != "configmaps" {
		t.Fatalf("the Corefile was asked as %+v", h.listed)
	}
	var silent *Upstream
	for i := range v.Upstreams {
		if v.Upstreams[i].To == "192.0.2.1:53" {
			silent = &v.Upstreams[i]
		}
	}
	if silent == nil || silent.Answers || silent.P99 != nil {
		t.Fatalf("upstreams = %+v; want 192.0.2.1:53 listed with no answers", v.Upstreams)
	}
}

// A ConfigMap the person may not read, one that is missing, or a read that
// failed leaves the list as the metrics hold it, and the page still answers.
func TestStatsWithoutTheCorefileStaysMetricsOnly(t *testing.T) {
	for name, h := range map[string]*fakeHost{
		"refused": {answer: read(), listErr: extension.ErrDenied},
		"failed":  {answer: read(), listErr: errors.New("timeout")},
		"missing": {answer: read()},
	} {
		t.Run(name, func(t *testing.T) {
			rec, v := get(t, h, "")
			if rec.Code != 200 || v.State != "read" || len(v.Upstreams) != 2 {
				t.Fatalf("code %d state %q upstreams %+v", rec.Code, v.State, v.Upstreams)
			}
		})
	}
}

func TestStatsDoesNotAskForTheCorefileWhenNothingWasRead(t *testing.T) {
	h := &fakeHost{answer: extension.DNS{State: extension.WindowEmpty, Detail: "no CoreDNS pod was found"}}
	get(t, h, "")
	if len(h.listed) != 0 {
		t.Fatalf("asked for %+v with nothing to read", h.listed)
	}
}
