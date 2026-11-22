package cnpg

import "testing"

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"": "''", "pg-main": "pg-main", "a b": "'a b'", "it's": `'it'\''s'`,
		"$(rm -rf /)": "'$(rm -rf /)'", "a;b": "'a;b'", "`id`": "'`id`'",
		"-n": "'-n'", "a\nb": "'a\nb'", "x\"y": "'x\"y'", "ünï": "'ünï'",
	} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCommandsFor(t *testing.T) {
	members := []Member{
		{Instance: Instance{Name: "pg-1", Role: "primary"}},
		{Instance: Instance{Name: "pg-2", Role: "replica"}},
		{Instance: Instance{Name: "x; id", Role: "replica"}},
	}
	got := commandsFor("shop", "pg", members)
	want := []string{
		"kubectl cnpg backup pg -n shop",
		"kubectl cnpg restart pg -n shop",
		"kubectl cnpg reload pg -n shop",
		"kubectl cnpg promote pg pg-2 -n shop",
		"kubectl cnpg promote pg 'x; id' -n shop",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d commands, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Line != want[i] {
			t.Errorf("command %d = %q, want %q", i, got[i].Line, want[i])
		}
	}
	odd := commandsFor("ns' ; rm -rf ~", "$(x)", nil)
	if odd[0].Line != `kubectl cnpg backup '$(x)' -n 'ns'\'' ; rm -rf ~'` {
		t.Errorf("odd names: %q", odd[0].Line)
	}
}
