Extend the `less-bad-ai` (`lbai`) Go CLI project with an automated architectural rule engine and AST-based invariant checker.

Objective: Prevent coding agents from introducing forbidden dependencies, improper cross-module imports, or placing files in the wrong directories, eliminating the need for manual human diff review.

Requirements:
1. Archetype Configuration (`.lbai/rules.toml`):
   - Schema structure:
     - `[archetype]`: `name`, `version`, `allowed_extensions`
     - `[[boundaries]]`:
       - `name`: string (e.g., "UI Layer")
       - `path_pattern`: glob pattern (e.g., "src/ui/**", "client/**")
       - `forbidden_imports`: array of package regexes/names (e.g., ["net/http", "database/sql", "com.mongodb.*"])
       - `allowed_imports`: optional whitelist regexes
     - `[[forbidden_dependencies]]`: array of prohibited external packages or modules in build files (`pom.xml`, `package.json`, `go.mod`).
   - If `.lbai/rules.toml` does not exist, provide built-in fallback rules or an archetype selector.

2. Multi-Language Import Scanner (`pkg/linter`):
   - Scan modified source files:
     - Go files parsed using `go/parser` and `go/ast`.
     - Java/Kotlin parsed via regex/lexer for `import ...;`.
     - TypeScript/JavaScript parsed for ES6 `import ... from ...` and `require(...)`.
   - Map extracted imports against the boundary rules defined for that file's path.

3. Linter CLI Command:
   - Command: `lbai lint` (alias: `lbai check`)
     Flags:
       - `--path`, `-p`: Path to lint (defaults to git diff against `last_clean_head` or working tree).
       - `--strict`: Return non-zero exit code on warnings.
       - `--fix-hint`: Print human-readable guidance for how the agent should refactor the violation.

4. Output & Reporting:
   - Format diagnostics with file location and clear remediation advice:
     `[lbai][FAIL] src/ui/InventoryCard.java:14`
     `  Violation: Rule "UI Layer" forbids direct database/network imports.`
     `  Offending Import: com.mongodb.client.MongoCollection`
     `  Hint: Route data queries through services/DataRepository.`

Include unit tests with sample source files demonstrating correct detection of boundary violations.