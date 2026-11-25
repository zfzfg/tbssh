#Requires -Version 7.2
<#
.SYNOPSIS
Removes the TerminalBrowserSsh module and its line in your PowerShell profile.

.DESCRIPTION
Run Uninstall-TbSshRemote user@server first for every server where you used
Install-TbSshRemote; this script only cleans up the local machine.
#>
$ErrorActionPreference = 'Stop'

if (Get-Module TerminalBrowserSsh) {
    Disable-TbSshShim
    Remove-Module TerminalBrowserSsh -Force
}

$target = Join-Path ([Environment]::GetFolderPath('MyDocuments')) 'PowerShell\Modules\TerminalBrowserSsh'
if (Test-Path $target) {
    Remove-Item -Recurse -Force $target
    Write-Host "Removed $target"
}
else {
    Write-Host "$target is not installed"
}

$profilePath = $PROFILE.CurrentUserAllHosts
if (Test-Path $profilePath) {
    $lines = Get-Content $profilePath
    $kept = $lines | Where-Object { $_ -ne 'Import-Module TerminalBrowserSsh' -and $_ -ne '# tbssh: ssh with terminal-browser graphics' }
    if ($kept.Count -ne $lines.Count) {
        Set-Content -Path $profilePath -Value $kept
        Write-Host "Removed the tbssh lines from $profilePath"
    }
}
Write-Host 'Done. Open a new PowerShell window to use the plain OpenSSH ssh again.'
