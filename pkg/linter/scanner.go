package linter

import (
	"bufio"
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type Import struct {
	Name string
	Line int
}

type Diagnostic struct {
	Path      string `json:"path"`
	Line      int    `json:"line,omitempty"`
	Rule      string `json:"rule"`
	Violation string `json:"violation"`
	Offender  string `json:"offender"`
	Hint      string `json:"hint,omitempty"`
	Severity  string `json:"severity"`
}

var (
	javaImport = regexp.MustCompile(`^\s*import\s+(?:static\s+)?([A-Za-z_$][\w$]*(?:\.[A-Za-z_$*][\w$*]*)*)\s*;?`)
	esImport   = regexp.MustCompile(`(?:^|\s)import\s+(?:[^'";]+?\s+from\s+)?["']([^"']+)["']|require\s*\(\s*["']([^"']+)["']\s*\)|import\s*\(\s*["']([^"']+)["']\s*\)`)
)

func ExtractImports(path string) ([]Import, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go":
		return goImports(path)
	case ".java", ".kt", ".kts":
		return lineImports(path, javaImport)
	case ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs":
		return lineImports(path, esImport)
	default:
		return nil, nil
	}
}

func goImports(path string) ([]Import, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	var imports []Import
	for _, spec := range f.Imports {
		value, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return nil, err
		}
		imports = append(imports, Import{Name: value, Line: fset.Position(spec.Pos()).Line})
	}
	return imports, nil
}

func lineImports(path string, re *regexp.Regexp) ([]Import, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Import
	s := bufio.NewScanner(f)
	line := 0
	inBlock := false
	for s.Scan() {
		line++
		text := stripComments(s.Text(), &inBlock)
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			for i := 1; i < len(m); i++ {
				if m[i] != "" {
					out = append(out, Import{Name: m[i], Line: line})
					break
				}
			}
		}
	}
	return out, s.Err()
}

func stripComments(s string, inBlock *bool) string {
	var out strings.Builder
	for i := 0; i < len(s); {
		if *inBlock {
			j := strings.Index(s[i:], "*/")
			if j < 0 {
				return out.String()
			}
			*inBlock = false
			i += j + 2
			continue
		}
		if strings.HasPrefix(s[i:], "//") {
			break
		}
		if strings.HasPrefix(s[i:], "/*") {
			*inBlock = true
			i += 2
			continue
		}
		out.WriteByte(s[i])
		i++
	}
	return out.String()
}

func Scan(root string, paths []string, cfg Config) ([]Diagnostic, error) {
	var diagnostics []Diagnostic
	for _, rel := range paths {
		rel = filepath.ToSlash(filepath.Clean(rel))
		if strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
			return nil, fmt.Errorf("scan path escapes repository: %q", rel)
		}
		if isManifest(rel) {
			d, err := scanManifest(filepath.Join(root, filepath.FromSlash(rel)), rel, cfg)
			if err != nil {
				return nil, err
			}
			diagnostics = append(diagnostics, d...)
			continue
		}
		if !allowedExtension(rel, cfg.Archetype.AllowedExtensions) {
			continue
		}
		imports, err := ExtractImports(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", rel, err)
		}
		for _, boundary := range cfg.Boundaries {
			matches, _ := matchGlob(boundary.PathPattern, rel)
			if !matches {
				continue
			}
			for _, imp := range imports {
				if violates(imp.Name, boundary) {
					diagnostics = append(diagnostics, Diagnostic{Path: rel, Line: imp.Line, Rule: boundary.Name, Violation: "architectural boundary forbids this import", Offender: imp.Name, Hint: boundary.Hint, Severity: "error"})
				}
			}
		}
	}
	sort.Slice(diagnostics, func(i, j int) bool {
		if diagnostics[i].Path != diagnostics[j].Path {
			return diagnostics[i].Path < diagnostics[j].Path
		}
		if diagnostics[i].Line != diagnostics[j].Line {
			return diagnostics[i].Line < diagnostics[j].Line
		}
		return diagnostics[i].Rule < diagnostics[j].Rule
	})
	return diagnostics, nil
}

func violates(name string, b Boundary) bool {
	if len(b.AllowedImports) > 0 && !matchesAny(name, b.AllowedImports) {
		return true
	}
	return matchesAny(name, b.ForbiddenImports)
}
func matchesAny(s string, patterns []string) bool {
	for _, p := range patterns {
		if regexp.MustCompile(p).MatchString(s) {
			return true
		}
	}
	return false
}
func allowedExtension(path string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	ext := strings.ToLower(filepath.Ext(path))
	for _, a := range allowed {
		if ext == strings.ToLower(a) {
			return true
		}
	}
	return false
}
func isManifest(path string) bool {
	switch filepath.Base(path) {
	case "go.mod", "package.json", "pom.xml":
		return true
	}
	return false
}

func scanManifest(path, rel string, cfg Config) ([]Diagnostic, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	values := string(b)
	if filepath.Base(path) == "package.json" {
		var doc struct {
			Dependencies    map[string]string `json:"dependencies"`
			DevDependencies map[string]string `json:"devDependencies"`
		}
		if err := json.Unmarshal(b, &doc); err != nil {
			return nil, err
		}
		var names []string
		for k := range doc.Dependencies {
			names = append(names, k)
		}
		for k := range doc.DevDependencies {
			names = append(names, k)
		}
		values = strings.Join(names, "\n")
	}
	var out []Diagnostic
	for _, d := range cfg.ForbiddenDependencies {
		pattern := d.Pattern
		if pattern == "" {
			pattern = `(?m)^` + regexp.QuoteMeta(d.Name) + `$`
		}
		if regexp.MustCompile(pattern).MatchString(values) {
			offender := d.Name
			if offender == "" {
				offender = d.Pattern
			}
			out = append(out, Diagnostic{Path: rel, Rule: "forbidden dependency", Violation: "dependency is prohibited", Offender: offender, Hint: d.Hint, Severity: "error"})
		}
	}
	return out, nil
}

func globRegexp(pattern string) (*regexp.Regexp, error) {
	p := filepath.ToSlash(pattern)
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(p); i++ {
		switch p[i] {
		case '*':
			if i+1 < len(p) && p[i+1] == '*' {
				b.WriteString(".*")
				i++
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(p[i])))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}
func matchGlob(pattern, path string) (bool, error) {
	r, e := globRegexp(pattern)
	if e != nil {
		return false, e
	}
	return r.MatchString(filepath.ToSlash(path)), nil
}
