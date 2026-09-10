[CmdletBinding()]
param(
    [ValidateSet("deterministic", "live")]
    [string]$Mode = "deterministic",

    [ValidateSet("auto", "codex", "claude", "aider")]
    [string]$Agent = "auto",

    [string]$Prompt = "Add a FetchTitle use case that retrieves text from an HTTP endpoint. Keep it simple and add tests.",

    [string]$OutputRoot
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest
$utf8NoBom = New-Object System.Text.UTF8Encoding($false)

$projectRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
if ([string]::IsNullOrWhiteSpace($OutputRoot)) {
    $runID = (Get-Date).ToUniversalTime().ToString("yyyyMMddTHHmmssZ")
    $OutputRoot = Join-Path $projectRoot ".manual-eval\$runID"
}
$OutputRoot = [System.IO.Path]::GetFullPath($OutputRoot)
New-Item -ItemType Directory -Path $OutputRoot -Force | Out-Null

$toolDir = Join-Path $OutputRoot "tools"
$seedDir = Join-Path $OutputRoot "seed"
$controlDir = Join-Path $OutputRoot "without-lbai"
$guardedDir = Join-Path $OutputRoot "with-lbai"
foreach ($reservedPath in @($toolDir, $seedDir, $controlDir, $guardedDir)) {
    if (Test-Path $reservedPath) {
        throw "comparison output already exists: $reservedPath"
    }
}
New-Item -ItemType Directory -Path $toolDir -Force | Out-Null

$exeSuffix = if ($env:OS -eq "Windows_NT") { ".exe" } else { "" }
$lbaiPath = Join-Path $toolDir ("lbai" + $exeSuffix)
$fakeAgentPath = Join-Path $toolDir ("manual-eval-agent" + $exeSuffix)

Write-Host "[compare] Building lbai..."
& go -C $projectRoot build -o $lbaiPath ./cmd/lbai
if ($LASTEXITCODE -ne 0) { throw "failed to build lbai" }

function Resolve-AgentCommand {
    if ($Mode -eq "deterministic") {
        Write-Host "[compare] Building deterministic agent stand-in..."
        & go -C $projectRoot build -o $fakeAgentPath ./tools/manual-eval-agent
        if ($LASTEXITCODE -ne 0) { throw "failed to build deterministic evaluation agent" }
        return @($fakeAgentPath)
    }

    $candidates = if ($Agent -eq "auto") { @("codex", "claude", "aider") } else { @($Agent) }
    foreach ($candidate in $candidates) {
        if (Get-Command $candidate -ErrorAction SilentlyContinue) {
            switch ($candidate) {
                "codex" { return @("codex", "exec", "--sandbox", "workspace-write", "--ephemeral") }
                "claude" { return @("claude", "-p", "--permission-mode", "acceptEdits") }
                "aider" { return @("aider", "--yes", "--message") }
            }
        }
    }
    throw "no supported live agent found (tried: $($candidates -join ', '))"
}

function ConvertTo-TomlArray([string[]]$Values) {
    $encoded = foreach ($value in $Values) { ConvertTo-Json $value -Compress }
    return "[" + ($encoded -join ", ") + "]"
}

$agentCommand = @(Resolve-AgentCommand)
Write-Host "[compare] Agent command: $($agentCommand -join ' ')"

New-Item -ItemType Directory -Path $seedDir -Force | Out-Null
Get-ChildItem -Force (Join-Path $projectRoot "testdata\manual-comparison\project") |
    Copy-Item -Destination $seedDir -Recurse -Force

$config = @"
[worker]
type = "command"
command = $(ConvertTo-TomlArray $agentCommand)

[reviewer]
type = "command"
command = $(ConvertTo-TomlArray $agentCommand)

[[checks]]
name = "test"
executable = "go"
args = ["test", "./..."]
"@
[System.IO.File]::WriteAllText((Join-Path $seedDir ".lbai\config.toml"), $config, $utf8NoBom)

Push-Location $seedDir
try {
    & git init --quiet
    & git config user.name "LBAI Manual Evaluation"
    & git config user.email "lbai-eval@example.invalid"
    & git config core.autocrlf false
    & git add --all
    & git commit --quiet -m "chore: seed manual comparison fixture"
    if ($LASTEXITCODE -ne 0) { throw "failed to commit comparison fixture" }
} finally {
    Pop-Location
}

& git clone --quiet $seedDir $controlDir
if ($LASTEXITCODE -ne 0) { throw "failed to clone control project" }
& git clone --quiet $seedDir $guardedDir
if ($LASTEXITCODE -ne 0) { throw "failed to clone guarded project" }

function Invoke-NativeCaptured([string]$WorkingDirectory, [string]$Transcript, [string]$Executable, [string[]]$Arguments) {
    $previousErrorAction = $ErrorActionPreference
    Push-Location $WorkingDirectory
    try {
        # Windows PowerShell surfaces native stderr as ErrorRecord objects when
        # ErrorActionPreference is Stop, even when the exit is expected. Capture
        # objects and stringify them to retain stderr without PowerShell's stack.
        $ErrorActionPreference = "Continue"
        $records = @(& $Executable @Arguments 2>&1)
        $exitCode = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousErrorAction
        Pop-Location
    }
    $combined = (($records | ForEach-Object { $_.ToString() }) -join [Environment]::NewLine)
    if ($combined) { $combined += [Environment]::NewLine }
    [System.IO.File]::WriteAllText($Transcript, $combined, $utf8NoBom)
    if ($combined) { Write-Host $combined.TrimEnd() }
    return $exitCode
}

Write-Host "`n[compare] Running the agent directly (without lbai)..."
$controlArguments = @()
if ($agentCommand.Count -gt 1) { $controlArguments += $agentCommand[1..($agentCommand.Count - 1)] }
$controlArguments += $Prompt
$controlAgent = Invoke-NativeCaptured $controlDir (Join-Path $OutputRoot "without-lbai-agent.txt") $agentCommand[0] $controlArguments

Write-Host "`n[compare] Running the same agent through lbai..."
$guardedAgent = Invoke-NativeCaptured $guardedDir (Join-Path $OutputRoot "with-lbai-run.txt") $lbaiPath @("run", "--max-retries", "2", $Prompt)

Write-Host "`n[compare] Verifying both resulting projects..."
$controlTests = Invoke-NativeCaptured $controlDir (Join-Path $OutputRoot "without-lbai-tests.txt") "go" @("test", "./...")
$guardedTests = Invoke-NativeCaptured $guardedDir (Join-Path $OutputRoot "with-lbai-tests.txt") "go" @("test", "./...")
$controlLint = Invoke-NativeCaptured $controlDir (Join-Path $OutputRoot "without-lbai-lint.txt") $lbaiPath @("lint", "--path", ".", "--fix-hint")
$guardedLint = Invoke-NativeCaptured $guardedDir (Join-Path $OutputRoot "with-lbai-lint.txt") $lbaiPath @("lint", "--path", ".", "--fix-hint")

$controlStatus = (& git -C $controlDir status --short) -join "`n"
$guardedStatus = (& git -C $guardedDir status --short) -join "`n"
$controlStatusDisplay = if ($controlStatus) { ($controlStatus -replace "`r?`n", "<br>") -replace '\|', '\|' } else { "clean" }
$guardedStatusDisplay = if ($guardedStatus) { ($guardedStatus -replace "`r?`n", "<br>") -replace '\|', '\|' } else { "clean" }
$guardedCommitCount = [int]((& git -C $guardedDir rev-list --count HEAD).Trim())
$traceCount = @(Get-ChildItem (Join-Path $guardedDir ".lbai\traces\*.json") -ErrorAction SilentlyContinue).Count

$expected = if ($Mode -eq "deterministic") {
    $controlAgent -eq 0 -and $guardedAgent -eq 0 -and
    $controlTests -eq 0 -and $guardedTests -eq 0 -and
    $controlLint -ne 0 -and $guardedLint -eq 0 -and
    $guardedCommitCount -eq 3 -and $traceCount -eq 1
} else {
    $guardedAgent -eq 0 -and $guardedTests -eq 0 -and $guardedLint -eq 0
}

$result = if ($expected) { "PASS" } else { "REVIEW" }
$report = @"
# LBAI manual comparison

- Mode: $Mode
- Result: $result
- Prompt: $Prompt
- Agent command: $($agentCommand -join ' ')

| Measurement | Without LBAI | With LBAI |
| --- | ---: | ---: |
| Agent/run exit code | $controlAgent | $guardedAgent |
| `go test ./...` exit code | $controlTests | $guardedTests |
| Architecture lint exit code | $controlLint | $guardedLint |
| Git status | $controlStatusDisplay | $guardedStatusDisplay |
| Commit count | $((& git -C $controlDir rev-list --count HEAD).Trim()) | $guardedCommitCount |
| Durable traces | 0 | $traceCount |

The two project directories were cloned from the same seed commit. Inspect
`without-lbai` and `with-lbai`, along with the adjacent transcript files, to
compare the exact changes and verification feedback.
"@
$reportPath = Join-Path $OutputRoot "REPORT.md"
[System.IO.File]::WriteAllText($reportPath, $report, $utf8NoBom)

Write-Host "`n[compare] Result: $result"
Write-Host "[compare] Report: $reportPath"
Write-Host "[compare] Without LBAI: $controlDir"
Write-Host "[compare] With LBAI:    $guardedDir"

if (-not $expected) { exit 1 }
