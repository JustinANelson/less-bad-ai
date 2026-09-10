package linter

import (
	"bufio"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"go/parser"
	"go/token"
	"io"
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
)

func ExtractImports(path string) ([]Import, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go":
		return goImports(path)
	case ".java", ".kt", ".kts":
		return lineImports(path, javaImport)
	case ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs":
		return javascriptImports(path)
	default:
		return nil, nil
	}
}

type jsToken struct {
	kind  byte
	value string
	line  int
}

func javascriptImports(path string) ([]Import, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	tokens, err := lexJavaScript(b)
	if err != nil {
		return nil, err
	}
	var imports []Import
	for i, token := range tokens {
		if token.kind != 'i' {
			continue
		}
		switch token.value {
		case "require":
			if i+2 < len(tokens) && tokens[i+1].value == "(" && tokens[i+2].kind == 's' {
				imports = append(imports, Import{Name: tokens[i+2].value, Line: token.line})
			}
		case "import":
			if i+2 < len(tokens) && tokens[i+1].value == "(" && tokens[i+2].kind == 's' {
				imports = append(imports, Import{Name: tokens[i+2].value, Line: token.line})
				continue
			}
			for j := i + 1; j < len(tokens) && j <= i+20; j++ {
				if tokens[j].value == ";" || tokens[j].value == "=" {
					break
				}
				if tokens[j].kind == 's' {
					imports = append(imports, Import{Name: tokens[j].value, Line: token.line})
					break
				}
			}
		}
	}
	return imports, nil
}

func lexJavaScript(source []byte) ([]jsToken, error) {
	var tokens []jsToken
	line := 1
	for i := 0; i < len(source); {
		switch {
		case source[i] == '\n':
			line++
			i++
		case source[i] == ' ' || source[i] == '\t' || source[i] == '\r':
			i++
		case i+1 < len(source) && source[i] == '/' && source[i+1] == '/':
			i += 2
			for i < len(source) && source[i] != '\n' {
				i++
			}
		case i+1 < len(source) && source[i] == '/' && source[i+1] == '*':
			i += 2
			closed := false
			for i < len(source) {
				if source[i] == '\n' {
					line++
				}
				if i+1 < len(source) && source[i] == '*' && source[i+1] == '/' {
					i += 2
					closed = true
					break
				}
				i++
			}
			if !closed {
				return nil, fmt.Errorf("unterminated block comment at line %d", line)
			}
		case source[i] == '\'' || source[i] == '"':
			quote, startLine := source[i], line
			i++
			var value strings.Builder
			closed := false
			for i < len(source) {
				if source[i] == '\\' && i+1 < len(source) {
					value.WriteByte(source[i+1])
					i += 2
					continue
				}
				if source[i] == quote {
					i++
					closed = true
					break
				}
				if source[i] == '\n' {
					line++
				}
				value.WriteByte(source[i])
				i++
			}
			if !closed {
				return nil, fmt.Errorf("unterminated string at line %d", startLine)
			}
			tokens = append(tokens, jsToken{kind: 's', value: value.String(), line: startLine})
		case source[i] == '`':
			startLine := line
			i++
			closed := false
			for i < len(source) {
				if source[i] == '\\' && i+1 < len(source) {
					i += 2
					continue
				}
				if source[i] == '`' {
					i++
					closed = true
					break
				}
				if source[i] == '\n' {
					line++
				}
				i++
			}
			if !closed {
				return nil, fmt.Errorf("unterminated template string at line %d", startLine)
			}
		case isJSIdentifierStart(source[i]):
			start := i
			for i++; i < len(source) && isJSIdentifierPart(source[i]); i++ {
			}
			tokens = append(tokens, jsToken{kind: 'i', value: string(source[start:i]), line: line})
		default:
			tokens = append(tokens, jsToken{kind: 'p', value: string(source[i]), line: line})
			i++
		}
	}
	return tokens, nil
}

func isJSIdentifierStart(b byte) bool {
	return b == '_' || b == '$' || b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z'
}

func isJSIdentifierPart(b byte) bool {
	return isJSIdentifierStart(b) || b >= '0' && b <= '9'
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
	dependencies, err := manifestDependencies(filepath.Base(path), b)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", rel, err)
	}
	values := strings.Join(dependencies, "\n")
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

func manifestDependencies(name string, b []byte) ([]string, error) {
	switch name {
	case "package.json":
		var doc struct {
			Dependencies         map[string]string `json:"dependencies"`
			DevDependencies      map[string]string `json:"devDependencies"`
			PeerDependencies     map[string]string `json:"peerDependencies"`
			OptionalDependencies map[string]string `json:"optionalDependencies"`
		}
		if err := json.Unmarshal(b, &doc); err != nil {
			return nil, err
		}
		set := make(map[string]struct{})
		for _, group := range []map[string]string{doc.Dependencies, doc.DevDependencies, doc.PeerDependencies, doc.OptionalDependencies} {
			for dependency := range group {
				set[dependency] = struct{}{}
			}
		}
		return sortedKeys(set), nil
	case "go.mod":
		return goModDependencies(b), nil
	case "pom.xml":
		return pomDependencies(b)
	default:
		return nil, nil
	}
}

func goModDependencies(b []byte) []string {
	var dependencies []string
	inRequireBlock := false
	scanner := bufio.NewScanner(strings.NewReader(string(b)))
	for scanner.Scan() {
		line := strings.TrimSpace(strings.SplitN(scanner.Text(), "//", 2)[0])
		if line == "" {
			continue
		}
		if inRequireBlock {
			if line == ")" {
				inRequireBlock = false
				continue
			}
			if fields := strings.Fields(line); len(fields) >= 2 {
				dependencies = append(dependencies, fields[0])
			}
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "require" {
			if fields[1] == "(" {
				inRequireBlock = true
			} else {
				dependencies = append(dependencies, fields[1])
			}
		}
	}
	sort.Strings(dependencies)
	return dependencies
}

func pomDependencies(b []byte) ([]string, error) {
	decoder := xml.NewDecoder(strings.NewReader(string(b)))
	var dependencies []string
	for {
		token, err := decoder.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "dependency" {
			continue
		}
		var dependency struct {
			GroupID    string `xml:"groupId"`
			ArtifactID string `xml:"artifactId"`
		}
		if err := decoder.DecodeElement(&dependency, &start); err != nil {
			return nil, err
		}
		group := strings.TrimSpace(dependency.GroupID)
		artifact := strings.TrimSpace(dependency.ArtifactID)
		if artifact != "" {
			dependencies = append(dependencies, artifact)
			if group != "" {
				dependencies = append(dependencies, group+":"+artifact)
			}
		}
	}
	sort.Strings(dependencies)
	return dependencies, nil
}

func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
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
