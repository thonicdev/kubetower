package redis

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The driver stays where the viewer is: the defect would be a database driver
// linked into a package that is not a viewer.
//
// The Redis client is `valkey-go`, which the module already carried for shared
// state (`internal/sharedstate/valkeystore`); choosing it added no module
// to the build. This reads the import block of every .go file in the
// module, whatever its build constraints, and refuses:
//
//   - valkey-go imported from anywhere but this package and valkeystore;
//   - the other Redis client and the MySQL driver, imported from anywhere: a
//     second Redis client would be the one this choice exists to avoid;
//   - pgx's connection package imported from anywhere but the Postgres
//     (CNPG) extra, whose SQL door it is (`extras/cnpg/sqlconn.go`), and the
//     rest of pgx imported from anywhere: the door speaks the wire protocol
//     through pgconn alone.
func TestTheRedisClientIsImportedOnlyWhereItIsUsed(t *testing.T) {
	const valkey = "github.com/valkey-io/valkey-go"
	allowedValkey := map[string]bool{
		"extras/redis":                     true,
		"internal/sharedstate/valkeystore": true,
	}
	never := []string{"github.com/redis/go-redis", "github.com/go-redis/redis", "github.com/go-sql-driver/mysql"}
	const pgconn = "github.com/jackc/pgx/v5/pgconn"

	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	files, importers := 0, map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", "dist":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		files++
		rel, _ := filepath.Rel(root, filepath.Dir(path))
		dir := filepath.ToSlash(rel)
		for _, spec := range file.Imports {
			imp, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			if imp == valkey || strings.HasPrefix(imp, valkey+"/") {
				importers[dir] = true
				if !allowedValkey[dir] {
					t.Errorf("%s imports %s; the Redis client belongs to the viewer and to shared state only", path, imp)
				}
			}
			if strings.HasPrefix(imp, "github.com/jackc/pgx") {
				if imp != pgconn || dir != "extras/cnpg" {
					t.Errorf("%s imports %s; only extras/cnpg imports pgx, and only %s", path, imp, pgconn)
				}
				importers[dir] = true
			}
			for _, other := range never {
				if imp == other || strings.HasPrefix(imp, other+"/") {
					t.Errorf("%s imports %s, a driver this module does not link", path, imp)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the module: %v", err)
	}
	// The floor: the module has hundreds of Go files. Far fewer means the walk
	// started somewhere else and would pass every rule above.
	if files < 300 || !importers["extras/redis"] || !importers["internal/sharedstate/valkeystore"] || !importers["extras/cnpg"] {
		t.Fatalf("read %d .go files, valkey-go imported from %v: the walk is not seeing the module", files, importers)
	}
	t.Logf("%d .go files; valkey-go imported from %d directories", files, len(importers))
}
