package launch

// With KT_LISTEN naming
// `localhost`, the address is resolved once - by the bind - and every later
// use takes the address bound, so nothing resolves the name a second time and
// reaches whatever holds the other half of a dual-stack `localhost`.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstanceAddressNeverResolvesAName(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KT_STATE_DIR", dir)
	record := func(addr string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, instanceAddressFile), []byte(addr+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if got, err := instanceAddress("localhost:5823"); err == nil {
		t.Errorf("a name with nothing recorded resolved to %q; want a refusal", got)
	}
	if got, _ := instanceAddress("127.0.0.1:5823"); got != "127.0.0.1:5823" {
		t.Errorf("an address literal answered %q", got)
	}
	if got, _ := instanceAddress("[::1]:5823"); got != "[::1]:5823" {
		t.Errorf("an IPv6 literal answered %q", got)
	}

	record("127.0.0.1:5823")
	if got, err := instanceAddress("localhost:5823"); err != nil || got != "127.0.0.1:5823" {
		t.Errorf("a name answered %q (%v); want the recorded address", got, err)
	}
	if got, _ := instanceAddress("[::1]:5823"); got != "[::1]:5823" {
		t.Errorf("an address literal was overridden by the record: %q", got)
	}
	record("127.0.0.1:6000")
	if got, err := instanceAddress("localhost:5823"); err == nil {
		t.Errorf("a record for another port was taken: %q", got)
	}
	record("192.0.2.7:5823")
	if got, err := instanceAddress("localhost:5823"); err == nil {
		t.Errorf("a record that is not loopback was taken: %q", got)
	}
}

func TestALaunchForLocalhostReachesTheAddressTheInstanceBound(t *testing.T) {
	t.Setenv("KT_STATE_DIR", t.TempDir())
	secret, err := launcherSecret(true)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := listenFirst("localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	bound := ln.Addr().String()
	if err := writeInstanceAddress(bound); err != nil {
		t.Fatal(err)
	}
	instanceOn(t, ln, secret)
	_, port, _ := net.SplitHostPort(bound)

	var seen openedWindow
	if !secondLaunch(true, "localhost:"+port, false, false, fakeOpener(t, &seen)) {
		t.Fatal("a launch for KT_LISTEN=localhost did not hand off to the instance that bound it")
	}
	if seen.listen != bound || strings.Contains(seen.listen, "localhost") {
		t.Errorf("the window was opened on %q; want the address bound, %q", seen.listen, bound)
	}
	if !seen.nativeSignedIn {
		t.Error("the window's code opened no session")
	}
}

// Run's half, read from its source: the window, the handoff and the start-up
// "listening on" line take the bound address, never KT_LISTEN.
func TestRunUsesTheBoundAddressAfterBinding(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "run.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	checked := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		var name string
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			name = fn.Name
		case *ast.SelectorExpr:
			name = fn.Sel.Name
		}
		argIndex := map[string]int{"openWindow": 1, "AcceptHandoffs": 1, "writeInstanceAddress": 0, "announceListen": 0}
		i, ok := argIndex[name]
		if !ok || len(call.Args) <= i {
			return true
		}
		checked[name] = true
		if id, ok := call.Args[i].(*ast.Ident); !ok || id.Name != "bound" {
			t.Errorf("%s at %s is not given the bound address", name, fset.Position(call.Pos()))
		}
		return true
	})
	for _, name := range []string{"openWindow", "AcceptHandoffs", "writeInstanceAddress", "announceListen"} {
		if !checked[name] {
			t.Errorf("Run never calls %s, so this test reads the wrong code", name)
		}
	}
}

// A launch that cannot ask for a window says
// what answers at a loopback address, never by dialling the name again.
func TestTheLogProbeNeverDialsTheName(t *testing.T) {
	t.Setenv("KT_STATE_DIR", t.TempDir()) // no record: the name cannot be used
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	said := captureLog(t)

	var seen openedWindow
	if secondLaunch(true, "localhost:"+port, false, false, fakeOpener(t, &seen)) || seen.calls != 0 {
		t.Fatal("a launch for a name with no record handed off")
	}
	if !strings.Contains(said.String(), "something answers on 127.0.0.1:"+port) {
		t.Errorf("the log does not name the loopback address that answered:\n%s", said.String())
	}
	if strings.Contains(said.String(), "answers on localhost") {
		t.Errorf("the log probe dialled the name:\n%s", said.String())
	}
}

func TestARecordOthersCanReadIsNotTrusted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits are not the permission on Windows; the DACL test covers the record")
	}
	dir := t.TempDir()
	t.Setenv("KT_STATE_DIR", dir)
	path := filepath.Join(dir, instanceAddressFile)
	if err := os.WriteFile(path, []byte("127.0.0.1:5823\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := instanceAddress("localhost:5823"); err != nil || got != "127.0.0.1:5823" {
		t.Fatalf("an owner-only record was not used: %q (%v)", got, err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if got, err := instanceAddress("localhost:5823"); err == nil {
		t.Errorf("a record others can write was trusted: %q", got)
	}
}
