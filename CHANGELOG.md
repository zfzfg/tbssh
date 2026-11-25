# Changelog

## v0.1.0 – 2026-11-25

First public release.

- kitty graphics → Sixel bridge for terminal-browser in Windows Terminal, with a local compositor and
  changed-pixels-only Sixel output
- Answers kitty capability and cell-size queries; reports the window's pixel size to the server
- Interactive SSH client built on `ssh -G`: agent, key files, certificates, 2FA, password, known_hosts,
  ProxyJump/ProxyCommand, agent and port forwarding, escape sequences
- Everything non-interactive is passed to `ssh.exe` unchanged
- PowerShell module with `ssh` shim, `Test-TbSsh`, `Show-TbSshDemo`, `Install-TbSshRemote`
- Installer that builds from source or downloads a prebuilt release; uninstaller
