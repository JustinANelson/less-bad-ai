[CmdletBinding()]
param(
    [string]$Version = "latest",
    [string]$InstallDir = (Join-Path $env:LOCALAPPDATA "Programs\less-bad-ai\bin"),
    [switch]$NoPath
)

$ErrorActionPreference = "Stop"
$repository = "JustinANelson/less-bad-ai"
$architecture = switch ($env:PROCESSOR_ARCHITECTURE) {
    "AMD64" { "amd64" }
    "ARM64" { "arm64" }
    default { throw "Unsupported Windows architecture: $env:PROCESSOR_ARCHITECTURE" }
}
$asset = "lbai_windows_$architecture.zip"

if ($Version -eq "latest") {
    $release = Invoke-RestMethod "https://api.github.com/repos/$repository/releases/latest"
    $tag = $release.tag_name
} else {
    $tag = if ($Version.StartsWith("v")) { $Version } else { "v$Version" }
}
$baseURL = "https://github.com/$repository/releases/download/$tag"
$temporary = Join-Path ([System.IO.Path]::GetTempPath()) ("lbai-install-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $temporary | Out-Null

try {
    $archive = Join-Path $temporary $asset
    $checksums = Join-Path $temporary "checksums.txt"
    Write-Host "Installing less-bad-ai $tag for windows/$architecture..."
    Invoke-WebRequest "$baseURL/$asset" -OutFile $archive -UseBasicParsing
    Invoke-WebRequest "$baseURL/checksums.txt" -OutFile $checksums -UseBasicParsing

    $expectedLine = Get-Content $checksums | Where-Object { $_ -match "\s+$([regex]::Escape($asset))$" } | Select-Object -First 1
    if (-not $expectedLine) { throw "Release checksums do not contain $asset" }
    $expected = ($expectedLine -split "\s+")[0].ToLowerInvariant()
    $actual = (Get-FileHash $archive -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actual -ne $expected) { throw "Checksum verification failed for $asset" }

    Expand-Archive $archive -DestinationPath $temporary -Force
    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    foreach ($name in @("lbai.exe", "less-bad-ai.exe")) {
        $source = Join-Path $temporary $name
        if (-not (Test-Path $source)) { throw "Release archive is missing $name" }
        Copy-Item -LiteralPath $source -Destination (Join-Path $InstallDir $name) -Force
        Unblock-File -LiteralPath (Join-Path $InstallDir $name) -ErrorAction SilentlyContinue
    }

    if (-not $NoPath) {
        $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
        $entries = @($userPath -split ";" | Where-Object { $_ })
        if (-not ($entries | Where-Object { $_.TrimEnd("\") -ieq $InstallDir.TrimEnd("\") })) {
            $updated = (@($entries) + $InstallDir) -join ";"
            [Environment]::SetEnvironmentVariable("Path", $updated, "User")
        }
        if (-not (($env:Path -split ";") -contains $InstallDir)) {
            $env:Path = "$InstallDir;$env:Path"
        }
    }

    Write-Host "Installed lbai and less-bad-ai to $InstallDir"
    Write-Host 'Open a new terminal, then run: lbai setup'
} finally {
    if (Test-Path $temporary) {
        Remove-Item -LiteralPath $temporary -Recurse -Force
    }
}
