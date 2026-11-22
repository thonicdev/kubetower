package cluster

// The kubeconfig this process reads, which is a **list**, not a file.
//
// `KUBECONFIG` is a list of files separated by `os.PathListSeparator` (`;` on
// Windows, `:` elsewhere), first file winning on a duplicate key. Read as one
// path, `$HOME\.kube\config;C:\work\new-cluster.yaml` would be opened as a
// single file and the console would refuse to start.
//
// Four things read it: the manager's context list, its per-context
// `rest.Config`, the readiness probe, and the per-session kubeconfig the
// terminals run against. So the list is a type, built once and passed along;
// a terminal given only the first file would lack a context from the second.
//
// The merge is client-go's `ClientConfigLoadingRules.Precedence`, kubectl's
// own: on a duplicate **name the first file wins** and the loser's entry is
// discarded whole. A merge of our own could let the console and the dock's
// `kubectl` disagree about which `prod` they mean. Survey reports the losers.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// EnvKubeconfig is the variable, named rather than typed at three call sites.
//
// **`KUBECONFIG`, not a `KT_` variable**: it already means the kubeconfig's
// files in precedence order to kubectl, helm and client-go, and the dock's
// `kubectl` reads it. A second variable could disagree with it.
const EnvKubeconfig = "KUBECONFIG"

// Kubeconfig is the list of files that together are the operator's kubeconfig,
// in precedence order — earlier wins.
//
// A named slice and not a string, so a caller cannot treat the whole of
// `KUBECONFIG` as one filename.
type Kubeconfig []string

// SplitKubeconfig turns a `KUBECONFIG` value into the files it names.
//
// `filepath.SplitList`, because the separator is the platform's and must agree
// with kubectl on this machine. Empty entries are dropped, and so are
// duplicates, keeping the first: the merge ignores the second anyway.
func SplitKubeconfig(value string) Kubeconfig {
	var files Kubeconfig
	seen := map[string]bool{}
	for _, path := range filepath.SplitList(value) {
		path = strings.TrimSpace(path)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		files = append(files, path)
	}
	return files
}

// DefaultKubeconfigPath is where kubectl looks when `KUBECONFIG` says nothing.
//
// The last resort is the bare name `kubeconfig` on a host with no home
// directory; the next step then fails naming the missing file.
func DefaultKubeconfigPath() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".kube", "config")
	}
	return "kubeconfig"
}

// KubeconfigFromEnv is the list this process reads: `KUBECONFIG`, or the one
// default file behind it.
//
// The **one** place `KUBECONFIG` is split.
func KubeconfigFromEnv() Kubeconfig {
	if files := SplitKubeconfig(os.Getenv(EnvKubeconfig)); len(files) > 0 {
		return files
	}
	return Kubeconfig{DefaultKubeconfigPath()}
}

// Env is this list written the way `KUBECONFIG` is written, for handing to a
// child process.
//
// Distinct from String: a comma-joined list handed to kubectl names a single
// file that does not exist.
func (k Kubeconfig) Env() string {
	return strings.Join(k, string(os.PathListSeparator))
}

// String is the list as a log line or a sentence names it.
//
// One file renders as just that file.
func (k Kubeconfig) String() string {
	if len(k) == 0 {
		return "(no kubeconfig)"
	}
	return strings.Join(k, ", ")
}

// loadingRules is the merge, expressed the way kubectl expresses it.
//
// `Precedence` and never `ExplicitPath`, even for one file: `ExplicitPath`
// makes a **missing** file an error, `Precedence` skips it, as kubectl does.
// A file that is there and does not parse is an error either way; Load handles
// every path being missing.
func (k Kubeconfig) loadingRules() *clientcmd.ClientConfigLoadingRules {
	return &clientcmd.ClientConfigLoadingRules{Precedence: append(Kubeconfig(nil), k...)}
}

// Load reads and merges the list.
//
// The result is what kubectl would compute from the same `KUBECONFIG`, with
// relative certificate and token paths resolved against the file that named
// them: two files in two directories can each say `ca.crt` and mean different
// bytes.
//
// **Every path missing is a failure, not an empty kubeconfig**: an empty bind
// mount must not read as an installation with no clusters yet. client-go's
// `WarnIfAllMissing` decides it, so this cannot drift from client-go.
func (k Kubeconfig) Load() (*clientcmdapi.Config, error) {
	if len(k) == 0 {
		return nil, errors.New("no kubeconfig file to read")
	}

	rules := k.loadingRules()
	var allMissing error
	rules.WarnIfAllMissing = true
	rules.Warner = func(err error) { allMissing = err }

	raw, err := rules.Load()
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", k, err)
	}
	if allMissing != nil {
		return nil, fmt.Errorf("reading %s: %w", k, allMissing)
	}
	return raw, nil
}

// contextsIn reports how many contexts one candidate file declares, for
// discover.go.
//
// It lives here because a guard test allows exactly this file to call
// `clientcmd.LoadFromFile`. The same reader that merges a file decides whether
// it is a kubeconfig, or discovery could keep a file the merge then ignores.
func contextsIn(path string) (int, error) {
	config, err := clientcmd.LoadFromFile(path)
	if err != nil {
		return 0, err
	}
	if config == nil {
		return 0, nil
	}
	return len(config.Contexts), nil
}

// KubeconfigFile is what one path in the list is doing. See Survey.
type KubeconfigFile struct {
	Path string

	// Absent is set when the path is not there at all.
	//
	// Separate from Problem because they need opposite answers: a skipped file
	// is worth naming on a page and must never degrade `/kt-healthz`, while a
	// file that stopped reading is the failure `/kt-healthz` exists for.
	Absent bool

	// Problem is why a file that *is* there could not be read. Empty when it
	// read, or when it is Absent.
	Problem string

	// Contexts, Clusters and Users are what this file declares, sorted. Empty
	// for a file that could not be read, never a claim about its contents.
	Contexts []string
	Clusters []string
	Users    []string
}

// Read reports whether this file contributed to the merge.
func (f KubeconfigFile) Read() bool { return !f.Absent && f.Problem == "" }

// KubeconfigShadow is one name that more than one file declares, and therefore
// one entry an operator cannot see.
//
// The loss is **silent and total**: the merge keeps the first file's entry and
// the second does not exist anywhere in the console, with no error and no row.
// A shadowed user is the dangerous case: the second file's context then
// authenticates with the **first** file's credential, against a server that
// file never named.
type KubeconfigShadow struct {
	// Kinds is which of the kubeconfig's three maps this name is shadowed in,
	// "context", "cluster", "user", in that order.
	//
	// A list because a context, its cluster and its user conventionally share
	// a name, so one incident usually shadows all three, and three warnings for
	// it get skipped. They fold only when the name, the winner and the losers
	// all agree.
	Kinds []string
	Name  string
	// Winner is the file whose entry is in force, Losers the files whose entry
	// was discarded, in precedence order.
	Winner string
	Losers []string
}

// Subject is the kinds as a phrase: "context", or "context, cluster and user".
func (s KubeconfigShadow) Subject() string {
	switch len(s.Kinds) {
	case 0:
		return "name"
	case 1:
		return s.Kinds[0]
	}
	return strings.Join(s.Kinds[:len(s.Kinds)-1], ", ") + " and " + s.Kinds[len(s.Kinds)-1]
}

// Sentence is how this is said, in the log and in the settings answer both.
//
// One composer for both, so the log and the page cannot disagree. It opens
// with a capital because both put it after a lead-in.
func (s KubeconfigShadow) Sentence() string {
	verb := "is"
	if len(s.Kinds) > 1 {
		verb = "are"
	}
	return fmt.Sprintf("The %s %q %s declared by %s as well, and %s wins: "+
		"the entry in %s is discarded whole and nothing in this console can reach it",
		s.Subject(), s.Name, verb, strings.Join(s.Losers, ", "), s.Winner,
		strings.Join(s.Losers, ", "))
}

// KubeconfigSurvey is the list, file by file, with what the merge hid.
//
// A separate pass from Load, off the request path, for the startup log and a
// diagnostic page. It reads each file **on its own**: the merged config holds
// the winner and no trace of what it beat.
type KubeconfigSurvey struct {
	Files    []KubeconfigFile
	Shadowed []KubeconfigShadow
}

// Survey reads each file in the list separately and reports what it found.
func (k Kubeconfig) Survey() KubeconfigSurvey {
	survey := KubeconfigSurvey{Files: make([]KubeconfigFile, 0, len(k))}

	// name -> the files declaring it, in precedence order. The three maps merge
	// independently: a context can lose while the cluster it names wins.
	declared := map[string]map[string][]string{
		"context": {}, "cluster": {}, "user": {},
	}

	for _, path := range k {
		file := KubeconfigFile{Path: path}
		raw, err := clientcmd.LoadFromFile(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			// Skipped by the merge, and skipped here for the same reason.
			file.Absent = true
		case err != nil:
			file.Problem = err.Error()
		default:
			file.Contexts = sortedKeys(raw.Contexts)
			file.Clusters = sortedKeys(raw.Clusters)
			file.Users = sortedKeys(raw.AuthInfos)
			for _, name := range file.Contexts {
				declared["context"][name] = append(declared["context"][name], path)
			}
			for _, name := range file.Clusters {
				declared["cluster"][name] = append(declared["cluster"][name], path)
			}
			for _, name := range file.Users {
				declared["user"][name] = append(declared["user"][name], path)
			}
		}
		survey.Files = append(survey.Files, file)
	}

	// Contexts first and users last, the order an operator can act on, and the
	// order the folded Kinds come out in.
	folded := map[string]int{}
	for _, kind := range []string{"context", "cluster", "user"} {
		names := make([]string, 0, len(declared[kind]))
		for name, files := range declared[kind] {
			if len(files) > 1 {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		for _, name := range names {
			files := declared[kind][name]
			// Folded onto an entry with the same name shadowed by the same files
			// (see KubeconfigShadow.Kinds). The separator is one no path can
			// contain, so two different triples cannot share a key.
			key := strings.Join(append([]string{name}, files...), "\x00")
			if at, ok := folded[key]; ok {
				survey.Shadowed[at].Kinds = append(survey.Shadowed[at].Kinds, kind)
				continue
			}
			folded[key] = len(survey.Shadowed)
			survey.Shadowed = append(survey.Shadowed, KubeconfigShadow{
				Kinds: []string{kind}, Name: name, Winner: files[0], Losers: files[1:],
			})
		}
	}
	return survey
}

// Read is the paths that contributed to the merge.
func (s KubeconfigSurvey) Read() []string {
	var read []string
	for _, f := range s.Files {
		if f.Read() {
			read = append(read, f.Path)
		}
	}
	return read
}

// sortedKeys is the map's keys, sorted.
func sortedKeys[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
