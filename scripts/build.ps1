$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$previousGOOS = $env:GOOS
$previousGOARCH = $env:GOARCH
$previousCGO = $env:CGO_ENABLED
Push-Location $projectRoot
try {
    New-Item -ItemType Directory -Force -Path dist | Out-Null
    $env:GOARCH = 'amd64'
    $env:CGO_ENABLED = '0'
    foreach ($target in @('windows', 'linux')) {
        $env:GOOS = $target
        $output = "dist/kick-level-farm-$target-amd64"
        if ($target -eq 'windows') { $output += '.exe' }
        go build -trimpath -ldflags '-s -w' -o $output .
        if ($LASTEXITCODE -ne 0) { throw "Build failed: $target" }
        Write-Host "Ready: $output"
    }
} finally {
    $env:GOOS = $previousGOOS
    $env:GOARCH = $previousGOARCH
    $env:CGO_ENABLED = $previousCGO
    Pop-Location
}
