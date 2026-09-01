[CmdletBinding()]
param(
    [string]$InstallDir = $(if ($env:CODEXLINK_INSTALL_DIR) { $env:CODEXLINK_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "CodexLink\bin" }),
    [switch]$NoStart
)
Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"
$MaxArchiveBytes = 256MB
$MaxEntryBytes = 128MB

function Get-Architecture {
    $arch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant()
    switch ($arch) {
        "x64" { "amd64" }
        "arm64" { throw "Windows ARM64 is not supported because the bundled tunnel dependency has no native upstream release" }
        default { throw "Unsupported CPU architecture: $arch" }
    }
}

function Install-GitAutomatically {
    if ($env:CODEXLINK_SKIP_GIT -eq "1" -or (Get-Command git -ErrorAction SilentlyContinue)) { return }
    Write-Host "· Git is missing; CodexLink is provisioning it automatically"
    $winget = Get-Command winget -ErrorAction SilentlyContinue
    if ($winget) {
        & $winget.Source install --id Git.Git --exact --silent --accept-package-agreements --accept-source-agreements
        $exitCode = $LASTEXITCODE
        if ($exitCode -eq 0) {
            $machinePath = [Environment]::GetEnvironmentVariable("Path", "Machine")
            $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
            $env:Path = (@($env:Path, $machinePath, $userPath) | Where-Object { $_ }) -join ';'
            if (Get-Command git -ErrorAction SilentlyContinue) { return }
            Write-Warning "Git was installed but is not visible in the current process yet"
            return
        }
        Write-Warning "winget failed to install Git (exit code $exitCode)"
    }
    Write-Warning "Git could not be provisioned automatically; non-Git CodexLink features remain available"
}

function Open-SafeArchive([string]$Archive) {
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    if ((Get-Item $Archive).Length -gt $MaxArchiveBytes) { throw "Package exceeds the 256 MiB safety limit" }
    $expected = @{
        "codexlink.exe" = $true
        "cloudflared.exe" = $true
        "LICENSE" = $true
        "README.md" = $true
        "README.zh-CN.md" = $true
        "install.ps1" = $true
    }
    $seen = @{}
    $total = [int64]0
    $zip = [System.IO.Compression.ZipFile]::OpenRead($Archive)
    try {
        foreach ($entry in $zip.Entries) {
            $name = $entry.FullName
            if (-not $name -or $entry.Name -ne $name -or $name.Contains("/") -or $name.Contains("\")) {
                throw "Package contains a nested or unsafe path: $name"
            }
            if (-not $expected.ContainsKey($name)) { throw "Package contains an unexpected path: $name" }
            if ($seen.ContainsKey($name)) { throw "Package contains a duplicate path: $name" }
            if ($entry.Length -gt $MaxEntryBytes) { throw "Package entry is too large: $name" }
            $attributes = [BitConverter]::ToUInt32([BitConverter]::GetBytes([int]$entry.ExternalAttributes), 0)
            $fileType = ($attributes -shr 16) -band 0xF000
            if ($fileType -ne 0 -and $fileType -ne 0x8000) {
                throw "Package contains a link or non-regular entry: $name"
            }
            $seen[$name] = $true
            $total += $entry.Length
            if ($total -gt $MaxArchiveBytes) { throw "Expanded package exceeds the 256 MiB safety limit" }
        }
        if ($seen.Count -ne $expected.Count) {
            $missing = @($expected.Keys | Where-Object { -not $seen.ContainsKey($_) }) -join ", "
            throw "Package is missing required files: $missing"
        }
    } catch {
        $zip.Dispose()
        throw
    }
    return $zip
}

function Expand-SafeArchive([string]$Archive, [string]$Destination) {
    $zip = Open-SafeArchive $Archive
    try {
        New-Item -ItemType Directory -Force -Path $Destination | Out-Null
        foreach ($entry in $zip.Entries) {
            $target = Join-Path $Destination $entry.Name
            [System.IO.Compression.ZipFileExtensions]::ExtractToFile($entry, $target, $true)
        }
    } finally {
        $zip.Dispose()
    }
}

function Stop-InstalledProcess([string]$Path) {
    $expected = [IO.Path]::GetFullPath($Path)
    $name = [IO.Path]::GetFileNameWithoutExtension($Path)
    foreach ($process in @(Get-Process -Name $name -ErrorAction SilentlyContinue)) {
        try {
            if ($process.Path -and [IO.Path]::GetFullPath($process.Path) -eq $expected) {
                Stop-Process -Id $process.Id -Force -ErrorAction Stop
                [void]$process.WaitForExit(10000)
            }
        } catch {
            Write-Verbose "Could not inspect or stop process $($process.Id): $_"
        }
    }
}

function Install-BinariesAtomic([array]$Items) {
    foreach ($item in $Items) {
        if (Test-Path $item.Target -PathType Container) { throw "$($item.Target) is a directory" }
        $item.Next = "$($item.Target).new.$PID"
        $item.Backup = "$($item.Target).old.$PID"
        Remove-Item $item.Next, $item.Backup -Force -ErrorAction SilentlyContinue
        Copy-Item $item.Source $item.Next -Force
    }

    try {
        foreach ($item in $Items) { Stop-InstalledProcess $item.Target }
        foreach ($item in $Items) {
            if (Test-Path $item.Target) {
                Move-Item $item.Target $item.Backup -Force
                $item.BackedUp = $true
            }
        }
        foreach ($item in $Items) {
            Move-Item $item.Next $item.Target -Force
            $item.Installed = $true
        }
    } catch {
        $original = $_
        $rollbackErrors = @()
        for ($index = $Items.Count - 1; $index -ge 0; $index--) {
            $item = $Items[$index]
            try {
                if ($item.Installed) {
                    Remove-Item $item.Target -Force -ErrorAction SilentlyContinue
                    $item.Installed = $false
                }
                if ($item.BackedUp -and (Test-Path $item.Backup)) {
                    Move-Item $item.Backup $item.Target -Force
                    $item.BackedUp = $false
                }
            } catch {
                $rollbackErrors += $_.Exception.Message
            }
        }
        foreach ($item in $Items) { Remove-Item $item.Next -Force -ErrorAction SilentlyContinue }
        if ($rollbackErrors.Count -gt 0) {
            throw "Installation failed: $($original.Exception.Message). Rollback was incomplete; backups were preserved: $($rollbackErrors -join '; ')"
        }
        throw $original
    }

    foreach ($item in $Items) {
        Remove-Item $item.Next, $item.Backup -Force -ErrorAction SilentlyContinue
    }
}


$arch = Get-Architecture
$asset = "codexlink_windows_${arch}.zip"
$temp = Join-Path ([IO.Path]::GetTempPath()) ("codexlink-install-" + [Guid]::NewGuid())
New-Item -ItemType Directory -Force -Path $temp | Out-Null
$startPath = $null
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

    if ((Get-Item $checksum).Length -gt 4096) { throw "Checksum file is too large" }
    $expected = ((Get-Content $checksum -Raw).Trim() -split '\s+')[0].ToLowerInvariant()
    if ($expected -notmatch '^[0-9a-f]{64}$') { throw "Invalid checksum file" }
    $actual = (Get-FileHash -Algorithm SHA256 $archive).Hash.ToLowerInvariant()
    if ($expected -ne $actual) { throw "Download checksum mismatch" }

    $unpacked = Join-Path $temp "unpacked"
    Expand-SafeArchive $archive $unpacked
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    $items = @(
        [pscustomobject]@{ Source = Join-Path $unpacked "codexlink.exe"; Target = Join-Path $InstallDir "codexlink.exe"; Next = $null; Backup = $null; BackedUp = $false; Installed = $false },
        [pscustomobject]@{ Source = Join-Path $unpacked "cloudflared.exe"; Target = Join-Path $InstallDir "cloudflared.exe"; Next = $null; Backup = $null; BackedUp = $false; Installed = $false }
    )
    Install-BinariesAtomic $items

    if ($env:CODEXLINK_SKIP_PATH_UPDATE -ne "1") {
        $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
        $parts = @($userPath -split ';' | Where-Object { $_ })
        if ($parts -notcontains $InstallDir) {
            [Environment]::SetEnvironmentVariable("Path", (($parts + $InstallDir) -join ';'), "User")
        }
    }
    $env:Path = "$InstallDir;$env:Path"
    Install-GitAutomatically
    Write-Host "✓ CodexLink and cloudflared installed in $InstallDir"
    Write-Host "✓ no Go or manual product dependency installation is required"

    if (-not $NoStart -and $env:CODEXLINK_NO_START -ne "1") {
        $startPath = Join-Path $InstallDir "codexlink.exe"
    }
} finally {
    Remove-Item -Recurse -Force $temp -ErrorAction SilentlyContinue
}
if ($startPath) {
    & $startPath
}
