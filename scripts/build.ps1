$ErrorActionPreference = 'Stop'
Set-Location -LiteralPath (Split-Path -Parent $PSScriptRoot)
$goCommand = Get-Command go -ErrorAction SilentlyContinue
if (-not $goCommand) {
    $installedGo = Join-Path $env:ProgramFiles 'Go\bin\go.exe'
    if (Test-Path -LiteralPath $installedGo) { $goCommand = Get-Command $installedGo }
}
if (-not $goCommand) { throw 'Instala Go 1.26 o posterior desde go.dev con un método permitido por Windows y vuelve a abrir la terminal.' }
& $goCommand.Source mod download
if ($LASTEXITCODE -ne 0) { throw 'No se pudieron resolver las dependencias Go.' }
npm ci
if ($LASTEXITCODE -ne 0) { throw 'No se pudieron instalar las dependencias del frontend.' }
npm run check
if ($LASTEXITCODE -ne 0) { throw 'La validación Svelte falló.' }
npm run build
if ($LASTEXITCODE -ne 0) { throw 'La compilación Svelte falló.' }
& $goCommand.Source vet ./...
if ($LASTEXITCODE -ne 0) { throw 'El análisis estático Go falló.' }
New-Item -ItemType Directory -Force bin | Out-Null
& $goCommand.Source build -trimpath -o bin/nodo.exe ./cmd/panel
if ($LASTEXITCODE -ne 0) { throw 'La compilación Go falló.' }
Write-Host 'Compilado: bin/nodo.exe. El frontend está incluido en el ejecutable.'
