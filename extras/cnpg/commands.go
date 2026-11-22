package cnpg

import "strings"

// Command is one `kubectl` line the person may copy to act on a Cluster.
// The console never runs it: it is written out from the object's own names
// and nothing sends it anywhere.
type Command struct {
	// Act is back-up, promote, restart or reload.
	Act string `json:"act"`
	// Instance is the instance a promote is for; empty for the others.
	Instance string `json:"instance,omitempty"`
	// Line is the command, every name shell-quoted.
	Line string `json:"line"`
}

// shellQuote quotes s as one POSIX shell word. A word made only of safe
// characters stays bare; anything else is wrapped in single quotes, with a
// single quote inside closed, escaped and reopened. An empty name is two single quotes.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./:=@%+,", r)) {
			safe = false
			break
		}
	}
	if safe && s[0] != '-' {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// commandsFor is the commands for one Cluster: a back-up, a restart and a
// reload, and a promote for each replica. Written from the namespace, the
// Cluster's name and its instances' names only.
func commandsFor(namespace, name string, members []Member) []Command {
	ns, n := shellQuote(namespace), shellQuote(name)
	out := []Command{
		{Act: "backup", Line: "kubectl cnpg backup " + n + " -n " + ns},
		{Act: "restart", Line: "kubectl cnpg restart " + n + " -n " + ns},
		{Act: "reload", Line: "kubectl cnpg reload " + n + " -n " + ns},
	}
	for _, m := range members {
		if m.Role == "primary" || m.Name == "" {
			continue
		}
		out = append(out, Command{Act: "promote", Instance: m.Name, Line: "kubectl cnpg promote " + n + " " + shellQuote(m.Name) + " -n " + ns})
	}
	return out
}
