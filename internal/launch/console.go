package launch

// consoleAttached says whether anything written to stderr will be read by a
// person.
//
// True by default: every build is a console program except the Windows one,
// linked `-H windowsgui`, where initConsole corrects it.
//
// Off Windows the default is an assumption, not a measurement: a .app opened
// from the Finder or a .desktop launch has an ordinary stderr that nobody reads,
// and nothing can tell it from the container's stderr, which `docker compose
// logs` does read and where the first-run banner must work. term.IsTerminal
// answers a different question, and would make the container's first run
// refuse itself.
//
// So nothing irreversible rests on it: logfile.go writes the log regardless;
// firstrun.go does not decide whether a drawn password can be obtained from it
// (passwordonce.go adds a channel that does not need it); and windowDecision
// reads the launcher, using this only for the terminal case. Keep it that way:
// a decision that cannot be undone depending on this variable is a defect.
var consoleAttached = true

// logFileName is where the log always goes, and to stderr as well when there is
// somebody there; beside everything else the console keeps.
const logFileName = "kubetower.log"
