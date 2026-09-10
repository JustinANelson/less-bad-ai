package topology

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildFindsInternalPackageEdge(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "go.mod"), "module example.com/app\n")
	mustWrite(t, filepath.Join(root, "pkg", "a", "a.go"), "package a\nimport _ \"example.com/app/pkg/b\"\n")
	mustWrite(t, filepath.Join(root, "pkg", "b", "b.go"), "package b\n")
	g, err := Build(root, []string{"pkg/a/a.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Nodes) != 2 || len(g.Edges) != 1 {
		t.Fatalf("unexpected graph: %#v", g)
	}
	if g.Edges[0].Source != "pkg/a" || g.Edges[0].Target != "pkg/b" {
		t.Fatalf("unexpected edge: %#v", g.Edges[0])
	}
	if !g.Nodes[0].Modified {
		t.Fatalf("modified node not marked: %#v", g.Nodes)
	}
}

func TestBuildRejectsModulePrefixCollisionsAndDeduplicatesEdges(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "go.mod"), "module example.com/app\n")
	mustWrite(t, filepath.Join(root, "pkg", "a", "one.go"), "package a\nimport (\n _ \"example.com/app/pkg/b\"\n _ \"example.com/application/pkg/external\"\n)\n")
	mustWrite(t, filepath.Join(root, "pkg", "a", "two.go"), "package a\nimport _ \"example.com/app/pkg/b\"\n")
	mustWrite(t, filepath.Join(root, "pkg", "b", "b.go"), "package b\n")
	g, err := Build(root, []string{"pkg/b/b.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Nodes) != 2 || len(g.Edges) != 1 {
		t.Fatalf("unexpected deduplicated graph: %#v", g)
	}
	if g.Edges[0] != (Edge{Source: "pkg/a", Target: "pkg/b"}) {
		t.Fatalf("unexpected edge: %#v", g.Edges[0])
	}
	if !g.Nodes[1].Modified || g.Nodes[1].ID != "pkg/b" {
		t.Fatalf("modified package not highlighted: %#v", g.Nodes)
	}
}
func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
