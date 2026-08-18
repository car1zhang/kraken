package jobs

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go/ast"
)

// The Q2 retirement's absence guard (lead-authored, sighted — see
// 90-build-report.md). RotationGate's DELETION is self-enforcing while the
// symbol stays deleted, but the obligation it stood for — "no spawn path
// performs or can trigger a main swap" — regresses silently the day someone
// re-adds a SwapMainTo call outside the two sanctioned manual surfaces, with
// every other test still green. This walks the AST of every production file
// so a COMMENT may still tell the story (limitrescue.go's do) without
// tripping the guard; only a real identifier reference counts.
func TestRetirementNoAutomatedMainSwap(t *testing.T) {
	root := filepath.Join("..", "..")
	// SwapMainTo's sanctioned reference sites: its own package, the manual CLI
	// switch, and the board's manual switch route. Everything else — the jobs
	// layer above all — must not name it.
	allowedSwap := map[string]bool{
		filepath.Join("internal", "core", "rotation.go"): true,
		filepath.Join("cmd", "kraken", "cmd_account.go"): true,
		filepath.Join("internal", "api", "account.go"):   true,
		// rotation.go declares the swapMain seam (`= core.SwapMainTo`) whose
		// one consumer is rescueSwap, the STANDING-agent rescue's swap half —
		// kronos/iris are main's own tenants, so theirs is the one automated
		// main swap that outlived Q2. Cell paths reach neither the seam nor
		// the symbol; a cell-side reference lands in an unlisted file and
		// still trips this guard.
		filepath.Join("internal", "jobs", "rotation.go"): true,
	}
	var swapHits, gateHits []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			// Only kraken's own Go trees; node_modules and the vite output
			// carry no Go but cost the walk real time.
			if name == "node_modules" || name == "dist" || name == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, src, 0)
		if err != nil {
			// A file the parser cannot read would hide a hit; loud, not skipped.
			return err
		}
		rel, _ := filepath.Rel(root, path)
		ast.Inspect(f, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			switch id.Name {
			case "SwapMainTo":
				if !allowedSwap[rel] {
					swapHits = append(swapHits, rel)
				}
			case "RotationGate":
				gateHits = append(gateHits, rel)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking the repo: %v", err)
	}
	if len(gateHits) > 0 {
		t.Errorf("RotationGate is retired (Q2) and must not be referenced in production code; found in: %v", gateHits)
	}
	if len(swapHits) > 0 {
		t.Errorf("SwapMainTo may be referenced only from its own file, the two manual switch surfaces (k account, POST /api/account/switch), and jobs/rotation.go's seam for the standing-agent rescue; found in: %v", swapHits)
	}
}
