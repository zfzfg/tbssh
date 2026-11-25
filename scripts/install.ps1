#Requires -Version 7.2
<#
.SYNOPSIS
Builds tbssh.exe and installs the TerminalBrowserSsh module for PowerShell 7
(also used by PowerShell 7 Preview).

.PARAMETER NoProfile
Do not add "Import-Module TerminalBrowserSsh" to your PowerShell profile.

.PARAMETER SkipBuild
Use an existing bin\tbssh.exe instead of building it.
#>
param(
    [switch]$NoProfile,
    [switch]$SkipBuild
)
$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
$exe = Join-Path $repo 'bin\tbssh.exe'

if (-not $SkipBuild) {
    $go = Get-Command go -ErrorAction SilentlyContinue
    if (-not $go) {
        $candidate = Join-Path $env:ProgramFiles 'Go\bin\go.exe'
        if (Test-Path $candidate) { $go = Get-Command $candidate }
    }
    if (-not $go) { throw 'Go is not installed (winget install GoLang.Go), or use -SkipBuild with a prebuilt bin\tbssh.exe.' }
    Write-Host 'Building tbssh.exe ...'
    Push-Location $repo
    try {
        & $go.Source build -trimpath -ldflags '-s -w' -o $exe ./cmd/tbssh
        if ($LASTEXITCODE -ne 0) { throw 'go build failed' }
    }
    finally { Pop-Location }
}
if (-not (Test-Path $exe)) { throw "$exe not found" }

$modules = Join-Path ([Environment]::GetFolderPath('MyDocuments')) 'PowerShell\Modules'
$target = Join-Path $modules 'TerminalBrowserSsh'
New-Item -ItemType Directory -Force -Path (Join-Path $target 'bin') | Out-Null
Copy-Item -Force (Join-Path $repo 'powershell\TerminalBrowserSsh\*.ps*1') $target
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
