# Changelog

## v0.1.1 – 2026-11-26

- Fix sixel data showing up as text all over the screen: tbssh now hands every image to the console in a
  single write. Before, Go split large writes into 16000-character pieces, and ConPTY could insert its own
  sequences (on resize or focus changes) between them, which cut the image short
- Never draw an image into the middle of an OSC/DCS string the server has only partly sent
- A leftover fallback timer no longer delays the next update after a synchronized frame
- New escape sequence `~r` redraws all images
- Port forwarding warnings and the keepalive timeout message go through the bridge instead of straight to
  the console

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
