[CmdletBinding()]
param(
    [string]$InstallDir = $(if ($env:CODEXLINK_INSTALL_DIR) { $env:CODEXLINK_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "CodexLink\bin" }),
    [switch]$NoStart
)
$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

function Get-Architecture {
    $arch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant()
    switch ($arch) {
        "x64" { "amd64" }
        "arm64" { "arm64" }
        default { throw "Unsupported CPU architecture: $arch" }
    }
}

function Install-GitAutomatically {
    if ($env:CODEXLINK_SKIP_GIT -eq "1" -or (Get-Command git -ErrorAction SilentlyContinue)) { return }
    Write-Host "· Git is missing; CodexLink is provisioning it automatically"
    $winget = Get-Command winget -ErrorAction SilentlyContinue
    if ($winget) {
        & $winget.Source install --id Git.Git --exact --silent --accept-package-agreements --accept-source-agreements
        return
    }
    Write-Warning "Git could not be provisioned automatically; non-Git CodexLink features remain available"
}

$arch = Get-Architecture
$asset = "codexlink_windows_${arch}.zip"
$temp = Join-Path ([IO.Path]::GetTempPath()) ("codexlink-install-" + [Guid]::NewGuid())
New-Item -ItemType Directory -Force -Path $temp | Out-Null
try {
    $archive = Join-Path $temp $asset
    $checksum = "$archive.sha256"
    if ($env:CODEXLINK_BUNDLE_FILE) {
        Copy-Item $env:CODEXLINK_BUNDLE_FILE $archive
        if (-not $env:CODEXLINK_CHECKSUM_FILE) { throw "CODEXLINK_CHECKSUM_FILE is required with CODEXLINK_BUNDLE_FILE" }
        Copy-Item $env:CODEXLINK_CHECKSUM_FILE $checksum
    } else {
        $repo = if ($env:CODEXLINK_REPOSITORY) { $env:CODEXLINK_REPOSITORY } else { "joeykchen/codexlink" }
        $base = if ($env:CODEXLINK_VERSION) {
            "https://github.com/$repo/releases/download/v$($env:CODEXLINK_VERSION)"
        } else {
            "https://github.com/$repo/releases/latest/download"
        }
        Write-Host "CodexLink: downloading the self-contained Windows/$arch package"
        Invoke-WebRequest -UseBasicParsing "$base/$asset" -OutFile $archive
        Invoke-WebRequest -UseBasicParsing "$base/$asset.sha256" -OutFile $checksum
    }

    $expected = ((Get-Content $checksum -Raw).Trim() -split '\s+')[0].ToLowerInvariant()
    $actual = (Get-FileHash -Algorithm SHA256 $archive).Hash.ToLowerInvariant()
    if ($expected -ne $actual) { throw "Download checksum mismatch" }

    $unpacked = Join-Path $temp "unpacked"
    Expand-Archive -Path $archive -DestinationPath $unpacked -Force
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    foreach ($binary in @("codexlink.exe", "cloudflared.exe")) {
        $source = Join-Path $unpacked $binary
        if (-not (Test-Path $source)) { throw "Package is missing $binary" }
        Copy-Item $source (Join-Path $InstallDir $binary) -Force
    }

    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    $parts = @($userPath -split ';' | Where-Object { $_ })
    if ($parts -notcontains $InstallDir) {
        [Environment]::SetEnvironmentVariable("Path", (($parts + $InstallDir) -join ';'), "User")
    }
    $env:Path = "$InstallDir;$env:Path"
    Install-GitAutomatically
    Write-Host "✓ CodexLink and cloudflared installed in $InstallDir"
    Write-Host "✓ no Go, winget package command, or manual dependency installation is required"

    if (-not $NoStart -and $env:CODEXLINK_NO_START -ne "1") {
        & (Join-Path $InstallDir "codexlink.exe")
    }
} finally {
    Remove-Item -Recurse -Force $temp -ErrorAction SilentlyContinue
}
