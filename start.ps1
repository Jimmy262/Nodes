$ErrorActionPreference = 'Stop'
& (Join-Path $PSScriptRoot 'scripts/build.ps1')
& (Join-Path $PSScriptRoot 'bin/nodo.exe')
