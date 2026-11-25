#Requires -Version 7.2
<#
.SYNOPSIS
Installs tbssh.exe and the TerminalBrowserSsh module for PowerShell 7
(also used by PowerShell 7 Preview).

.DESCRIPTION
Works in three situations:
  - Run from an extracted release ZIP: installs the files next to this script.
  - Run from a source checkout with Go installed: builds tbssh.exe, then installs.
  - Run from a source checkout without Go (or with -FromRelease): downloads the
    latest release from GitHub, then installs.

.PARAMETER NoProfile
Do not add "Import-Module TerminalBrowserSsh" to your PowerShell profile.

.PARAMETER SkipBuild
Source checkout only: use an existing bin\tbssh.exe instead of building it.

.PARAMETER FromRelease
Source checkout only: download the latest GitHub release instead of building.
#>
param(
    [switch]$NoProfile,
    [switch]$SkipBuild,
    [switch]$FromRelease
)
$ErrorActionPreference = 'Stop'
$repoSlug = 'zfzfg/tbssh'

function Get-TbSshRelease {
    $arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
    Write-Host "Looking up the latest release of $repoSlug ..."
    $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$repoSlug/releases/latest" -Headers @{ 'User-Agent' = 'tbssh-install' }
    $asset = $release.assets | Where-Object { $_.name -like "*-windows-$arch.zip" } | Select-Object -First 1
    if (-not $asset) { throw "Release $($release.tag_name) has no windows-$arch ZIP." }
    $dir = Join-Path ([IO.Path]::GetTempPath()) ("tbssh-" + [guid]::NewGuid())
    New-Item -ItemType Directory -Path $dir | Out-Null
    $zip = Join-Path $dir $asset.name
    Write-Host "Downloading $($asset.name) ..."
    Invoke-WebRequest -Uri $asset.browser_download_url -OutFile $zip -Headers @{ 'User-Agent' = 'tbssh-install' }
    $sums = $release.assets | Where-Object { $_.name -eq 'SHA256SUMS.txt' } | Select-Object -First 1
    if ($sums) {
        $expected = (Invoke-RestMethod -Uri $sums.browser_download_url -Headers @{ 'User-Agent' = 'tbssh-install' }) -split "`n" |
            Where-Object { $_ -match [regex]::Escape($asset.name) } | ForEach-Object { ($_ -split '\s+')[0] } | Select-Object -First 1
        $actual = (Get-FileHash -Algorithm SHA256 $zip).Hash
        if (-not $expected -or $expected -ne $actual) { throw "Checksum mismatch for $($asset.name)." }
    }
    Expand-Archive -Path $zip -DestinationPath $dir
    return $dir
}

# Locate tbssh.exe and the module sources.
if ((Test-Path (Join-Path $PSScriptRoot 'tbssh.exe')) -and (Test-Path (Join-Path $PSScriptRoot 'TerminalBrowserSsh'))) {
    # Extracted release ZIP.
    $exe = Join-Path $PSScriptRoot 'tbssh.exe'
    $moduleSrc = Join-Path $PSScriptRoot 'TerminalBrowserSsh'
}
else {
    $repo = Split-Path -Parent $PSScriptRoot
    $exe = Join-Path $repo 'bin\tbssh.exe'
    $moduleSrc = Join-Path $repo 'powershell\TerminalBrowserSsh'

    $go = $null
    if (-not $SkipBuild -and -not $FromRelease) {
        $go = Get-Command go -ErrorAction SilentlyContinue
        if (-not $go) {
            $candidate = Join-Path $env:ProgramFiles 'Go\bin\go.exe'
            if (Test-Path $candidate) { $go = Get-Command $candidate }
        }
        if (-not $go) {
            Write-Host 'Go is not installed; using the latest prebuilt release instead.'
            $FromRelease = $true
        }
    }

    if ($FromRelease) {
        $dir = Get-TbSshRelease
        $exe = Join-Path $dir 'tbssh.exe'
        $moduleSrc = Join-Path $dir 'TerminalBrowserSsh'
    }
    elseif (-not $SkipBuild) {
        $version = (& git -C $repo describe --tags --always --dirty 2>$null)
        if (-not $version) { $version = 'dev' }
        Write-Host "Building tbssh.exe ($version) ..."
        Push-Location $repo
        try {
            & $go.Source build -trimpath -ldflags "-s -w -X github.com/zfzfg/tbssh/internal/app.Version=$version" -o $exe ./cmd/tbssh
            if ($LASTEXITCODE -ne 0) { throw 'go build failed' }
        }
        finally { Pop-Location }
    }
}
if (-not (Test-Path $exe)) { throw "$exe not found" }

$modules = Join-Path ([Environment]::GetFolderPath('MyDocuments')) 'PowerShell\Modules'
$target = Join-Path $modules 'TerminalBrowserSsh'
if (Get-Module TerminalBrowserSsh) { Remove-Module TerminalBrowserSsh -Force }
New-Item -ItemType Directory -Force -Path (Join-Path $target 'bin') | Out-Null
Copy-Item -Force (Join-Path $moduleSrc '*.ps*1') $target
Copy-Item -Force $exe (Join-Path $target 'bin\tbssh.exe')
Write-Host "Installed module to $target"

if (-not $NoProfile) {
    $profilePath = $PROFILE.CurrentUserAllHosts
    $line = 'Import-Module TerminalBrowserSsh'
    $existing = if (Test-Path $profilePath) { Get-Content -Raw $profilePath } else { '' }
    if ($existing -notmatch [regex]::Escape($line)) {
        New-Item -ItemType Directory -Force -Path (Split-Path $profilePath) | Out-Null
        Add-Content -Path $profilePath -Value "`n# tbssh: ssh with terminal-browser graphics`n$line"
        Write-Host "Added '$line' to $profilePath"
    }
    else {
        Write-Host "$profilePath already imports the module"
    }
}

Import-Module (Join-Path $target 'TerminalBrowserSsh.psd1') -Force
Write-Host ''
Write-Host 'Done. In this and every new PowerShell window, "ssh" now goes through tbssh.'
Write-Host 'Check the terminal with:  Test-TbSsh      Visual test:  Show-TbSshDemo'
Write-Host 'Optional, per server:     Install-TbSshRemote user@server'
