# Always runs the cockpit with the latest code.
# Quit the cockpit (ctrl+c twice), then Enter = rebuild and start, x = stop.
$env:Path = "C:\Program Files\Go\bin;$env:Path"
Set-Location $PSScriptRoot
$exe = Join-Path $env:TEMP "llm-cockpit-dev.exe"
while ($true) {
    go build -o $exe ./cmd/cockpit
    if ($LASTEXITCODE -eq 0) {
        & $exe
    } else {
        Write-Host "Build failed." -ForegroundColor Red
    }
    $answer = Read-Host "Enter = rebuild and start, x = stop"
    if ($answer -eq "x") { break }
}
