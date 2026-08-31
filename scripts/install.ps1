$root = Split-Path -Parent $PSScriptRoot
& (Join-Path $root "install.ps1") @args
