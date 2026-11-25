# TerminalBrowserSsh - makes `ssh` in PowerShell go through tbssh.exe, so
# terminal-browser (kitty graphics) on the remote host shows up in Windows
# Terminal as sixel. Everything tbssh does not handle itself is passed to the
# real OpenSSH client unchanged.

$script:TbSshExe = Join-Path $PSScriptRoot 'bin\tbssh.exe'

function tbssh {
    <#
    .SYNOPSIS
    Runs tbssh.exe with the given arguments (same syntax as ssh).
    #>
    & $script:TbSshExe @args
}

function Enable-TbSshShim {
    <#
    .SYNOPSIS
    Makes the `ssh` command in this session run through tbssh.
    #>
    $exe = $script:TbSshExe.Replace("'", "''")
    Set-Item -Path function:global:ssh -Value ([scriptblock]::Create("& '$exe' @args"))
}

function Disable-TbSshShim {
    <#
    .SYNOPSIS
    Restores the plain OpenSSH `ssh` command in this session.
    #>
    Remove-Item -Path function:global:ssh -ErrorAction SilentlyContinue
}

function Test-TbSsh {
    <#
    .SYNOPSIS
    Checks whether the terminal can show graphics (sixel) for tbssh.
    #>
    & $script:TbSshExe --doctor
}

function Show-TbSshDemo {
    <#
    .SYNOPSIS
    Draws an animated test image through the graphics bridge, without a server.
    #>
    & $script:TbSshExe --demo
}

$script:RemoteSnippet = @'
# >>> tbssh >>>
# Set by tbssh (Windows): terminal-browser sends only changed areas.
if [ "${LC_TERMINAL:-}" = "tbssh" ]; then export TERMINAL_BROWSER_PRESENT=patched TERMINAL_BROWSER_FRAMES=inline; fi
# <<< tbssh <<<
'@

function Invoke-TbSshRemoteScript([string]$Destination, [string]$Script) {
    $lf = $Script -replace "`r", ''
    $b64 = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($lf))
    # No tty is requested, so tbssh hands this to the real ssh.exe.
    & $script:TbSshExe -T $Destination "printf %s '$b64' | base64 -d | sh"
}

function Install-TbSshRemote {
    <#
    .SYNOPSIS
    Adds a small snippet to the shell profile on the remote host so
    terminal-browser only sends changed areas when you connect with tbssh.
    It is optional: without it terminal-browser still works, but sends whole
    frames.
    .EXAMPLE
    Install-TbSshRemote user@server
    #>
    param([Parameter(Mandatory)][string]$Destination)
    $snippet = $script:RemoteSnippet -replace "`r", ''
    $remote = @"
snippet='$snippet'
for f in "`$HOME/.profile" "`$HOME/.bashrc" "`$HOME/.bash_profile" "`$HOME/.zshrc"; do
  case "`$f" in
    */.profile|*/.bashrc) ;;
    *) [ -f "`$f" ] || continue ;;
  esac
  if [ -f "`$f" ] && grep -q '>>> tbssh >>>' "`$f"; then
    echo "tbssh: `$f is already set up"
    continue
  fi
  printf '\n%s\n' "`$snippet" >> "`$f" && echo "tbssh: added to `$f"
done
"@
    Invoke-TbSshRemoteScript $Destination $remote
}

function Uninstall-TbSshRemote {
    <#
    .SYNOPSIS
    Removes the snippet added by Install-TbSshRemote from the remote host.
    #>
    param([Parameter(Mandatory)][string]$Destination)
    $remote = @'
for f in "$HOME/.profile" "$HOME/.bashrc" "$HOME/.bash_profile" "$HOME/.zshrc"; do
  [ -f "$f" ] || continue
  if grep -q '>>> tbssh >>>' "$f"; then
    sed -i '/# >>> tbssh >>>/,/# <<< tbssh <<</d' "$f" && echo "tbssh: removed from $f"
  fi
done
'@
    Invoke-TbSshRemoteScript $Destination $remote
}

if (-not (Test-Path $script:TbSshExe)) {
    Write-Warning "TerminalBrowserSsh: $script:TbSshExe not found; run scripts\install.ps1 again."
}
elseif (-not $env:TBSSH_NO_SHIM) {
    Enable-TbSshShim
}

Export-ModuleMember -Function tbssh, Enable-TbSshShim, Disable-TbSshShim, Test-TbSsh, Show-TbSshDemo, Install-TbSshRemote, Uninstall-TbSshRemote
