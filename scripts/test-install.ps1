$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest
$root = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
$temp = Join-Path ([IO.Path]::GetTempPath()) ("codexlink-install-test-" + [Guid]::NewGuid())
New-Item -ItemType Directory -Force -Path $temp | Out-Null

function Write-Checksum([string]$Archive) {
    $checksum = "$Archive.sha256"
    Set-Content $checksum "$((Get-FileHash -Algorithm SHA256 $Archive).Hash.ToLowerInvariant())  $(Split-Path -Leaf $Archive)"
    return $checksum
}

function Assert-Rejected([string]$Archive, [string]$Message) {
    $env:CODEXLINK_BUNDLE_FILE = $Archive
    $env:CODEXLINK_CHECKSUM_FILE = Write-Checksum $Archive
    $rejected = $false
    try { & (Join-Path $root "install.ps1") -NoStart } catch { $rejected = $true }
    if (-not $rejected) { throw $Message }
}

function New-CustomArchive([string]$Path, [array]$Names, [string]$Symlink = "") {
    $stream = [IO.File]::Open($Path, [IO.FileMode]::CreateNew)
    $zip = [IO.Compression.ZipArchive]::new($stream, [IO.Compression.ZipArchiveMode]::Create, $false)
    try {
        foreach ($name in $Names) {
            $entry = $zip.CreateEntry($name)
            if ($name -eq $Symlink) {
                $entry.ExternalAttributes = [int]-1610612736 # 0xA0000000: Unix symlink mode in ZIP attributes
            }
            $writer = [IO.StreamWriter]::new($entry.Open())
            try { $writer.Write("test") } finally { $writer.Dispose() }
        }
    } finally {
        $zip.Dispose()
        $stream.Dispose()
    }
}

try {
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $bundle = Join-Path $temp "bundle"
    $install = Join-Path $temp "bin"
    New-Item -ItemType Directory -Force -Path $bundle | Out-Null
    Set-Content (Join-Path $bundle "codexlink.exe") "codexlink-test"
    Set-Content (Join-Path $bundle "cloudflared.exe") "cloudflared-test"
    Set-Content (Join-Path $bundle "LICENSE") "license"
    Set-Content (Join-Path $bundle "README.md") "readme"
    Set-Content (Join-Path $bundle "README.zh-CN.md") "readme zh"
    Set-Content (Join-Path $bundle "install.ps1") "# installer"
    $archive = Join-Path $temp "codexlink_windows_amd64.zip"
    [IO.Compression.ZipFile]::CreateFromDirectory($bundle, $archive)

    $env:CODEXLINK_BUNDLE_FILE = $archive
    $env:CODEXLINK_CHECKSUM_FILE = Write-Checksum $archive
    $env:CODEXLINK_INSTALL_DIR = $install
    $env:CODEXLINK_SKIP_GIT = "1"
    $env:CODEXLINK_SKIP_PATH_UPDATE = "1"
    $env:CODEXLINK_NO_START = "1"
    & (Join-Path $root "install.ps1") -NoStart
    Set-Content (Join-Path $install "codexlink.exe") "old-codexlink"
    Set-Content (Join-Path $install "cloudflared.exe") "old-cloudflared"
    & (Join-Path $root "install.ps1") -NoStart
    if ((Get-Content (Join-Path $install "codexlink.exe") -Raw).Trim() -ne "codexlink-test") { throw "CodexLink was not atomically upgraded" }
    if ((Get-Content (Join-Path $install "cloudflared.exe") -Raw).Trim() -ne "cloudflared-test") { throw "cloudflared was not atomically upgraded" }
    foreach ($name in @("codexlink.exe", "cloudflared.exe")) {
        if (-not (Test-Path (Join-Path $install $name))) { throw "Installer did not install $name" }
    }

    $required = @("codexlink.exe", "cloudflared.exe", "LICENSE", "README.md", "README.zh-CN.md", "install.ps1")
    $badPath = Join-Path $temp "bad-path.zip"
    New-CustomArchive $badPath ($required + "../escape")
    Assert-Rejected $badPath "Installer accepted an unsafe ZIP path"

    $duplicate = Join-Path $temp "duplicate.zip"
    New-CustomArchive $duplicate ($required + "codexlink.exe")
    Assert-Rejected $duplicate "Installer accepted a duplicate ZIP path"

    $symlink = Join-Path $temp "symlink.zip"
    New-CustomArchive $symlink $required "cloudflared.exe"
    Assert-Rejected $symlink "Installer accepted a symbolic-link ZIP entry"

    $special = Join-Path $temp "special.zip"
    New-CustomArchive $special $required
    $stream = [IO.File]::Open($special, [IO.FileMode]::Open, [IO.FileAccess]::ReadWrite)
    $zip = [IO.Compression.ZipArchive]::new($stream, [IO.Compression.ZipArchiveMode]::Update, $false)
    try {
        $entry = $zip.GetEntry("cloudflared.exe")
        $entry.ExternalAttributes = [int]1610612736 # 0x60000000: non-regular Unix mode in ZIP attributes
    } finally {
        $zip.Dispose()
        $stream.Dispose()
    }
    Assert-Rejected $special "Installer accepted a non-regular ZIP entry"
} finally {
    Remove-Item Env:CODEXLINK_BUNDLE_FILE -ErrorAction SilentlyContinue
    Remove-Item Env:CODEXLINK_CHECKSUM_FILE -ErrorAction SilentlyContinue
    Remove-Item Env:CODEXLINK_INSTALL_DIR -ErrorAction SilentlyContinue
    Remove-Item Env:CODEXLINK_SKIP_GIT -ErrorAction SilentlyContinue
    Remove-Item Env:CODEXLINK_SKIP_PATH_UPDATE -ErrorAction SilentlyContinue
    Remove-Item Env:CODEXLINK_NO_START -ErrorAction SilentlyContinue
    Remove-Item -Recurse -Force $temp -ErrorAction SilentlyContinue
}
