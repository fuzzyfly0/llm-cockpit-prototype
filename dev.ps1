# Startet das Cockpit immer mit dem neuesten Code.
# Im Cockpit mit q beenden, dann Enter = neu bauen und starten, x = Ende.
$env:Path = "C:\Program Files\Go\bin;$env:Path"
Set-Location $PSScriptRoot
$exe = Join-Path $env:TEMP "llm-cockpit-dev.exe"
while ($true) {
    go build -o $exe ./cmd/cockpit
    if ($LASTEXITCODE -eq 0) {
        & $exe
    } else {
        Write-Host "Build fehlgeschlagen." -ForegroundColor Red
    }
    $answer = Read-Host "Enter = neu bauen und starten, x = Ende"
    if ($answer -eq "x") { break }
}
