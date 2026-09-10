package topology

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jnels/less-bad-ai/pkg/linter"
)

type Node struct {
	ID       string `json:"id"`
	Layer    string `json:"layer"`
	Modified bool   `json:"modified"`
}
type Edge struct {
	Source string `json:"source"`
	Target string `json:"target"`
}
type Graph struct {
	Nodes   []Node `json:"nodes"`
	Edges   []Edge `json:"edges"`
	Prompt  string `json:"prompt,omitempty"`
	Summary string `json:"summary,omitempty"`
}

func Build(root string, modified []string) (Graph, error) {
	module := readModule(root)
	mods := map[string]bool{}
	for _, f := range modified {
		mods[moduleID(filepath.ToSlash(f))] = true
	}
	nodes := map[string]Node{}
	edges := map[string]Edge{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		source := moduleID(filepath.ToSlash(rel))
		nodes[source] = Node{ID: source, Layer: layer(source), Modified: mods[source]}
		imports, err := linter.ExtractImports(path)
		if err != nil {
			return err
		}
		for _, imp := range imports {
			if module == "" || !strings.HasPrefix(imp.Name, module) {
				continue
			}
			target := strings.TrimPrefix(strings.TrimPrefix(imp.Name, module), "/")
			if target == "" {
				target = "."
			}
			nodes[target] = Node{ID: target, Layer: layer(target), Modified: mods[target]}
			key := source + "\x00" + target
			edges[key] = Edge{Source: source, Target: target}
		}
		return nil
	})
	if err != nil {
		return Graph{}, err
	}
	g := Graph{}
	for _, n := range nodes {
		g.Nodes = append(g.Nodes, n)
	}
	for _, e := range edges {
		g.Edges = append(g.Edges, e)
	}
	sort.Slice(g.Nodes, func(i, j int) bool { return g.Nodes[i].ID < g.Nodes[j].ID })
	sort.Slice(g.Edges, func(i, j int) bool {
		if g.Edges[i].Source != g.Edges[j].Source {
			return g.Edges[i].Source < g.Edges[j].Source
		}
		return g.Edges[i].Target < g.Edges[j].Target
	})
	return g, nil
}
func readModule(root string) string {
	f, err := os.Open(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) == 2 && fields[0] == "module" {
			v, _ := strconv.Unquote(fields[1])
			if v != "" {
				return v
			}
			return fields[1]
		}
	}
	return ""
}
func moduleID(rel string) string {
	dir := filepath.ToSlash(filepath.Dir(rel))
	if dir == "." {
		return "."
	}
	return strings.TrimPrefix(dir, "./")
}
func layer(id string) string {
	s := strings.ToLower(id)
	switch {
	case strings.Contains(s, "ui") || strings.Contains(s, "web"):
		return "ui"
	case strings.Contains(s, "data") || strings.Contains(s, "store"):
		return "data"
	default:
		return "core"
	}
}
