# tbssh

[![CI](https://github.com/zfzfg/tbssh/actions/workflows/ci.yml/badge.svg)](https://github.com/zfzfg/tbssh/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/zfzfg/tbssh)](https://github.com/zfzfg/tbssh/releases/latest)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**See [terminal-browser](https://github.com/zenbu-labs/terminal-browser) running on a Linux server, rendered as real pixels inside Windows Terminal, over a plain `ssh user@server`.**

tbssh is a drop-in SSH client for PowerShell 7 on Windows. It passes your session through untouched,
except for one thing: it catches the **kitty graphics** that terminal-browser draws and re-encodes them
as **Sixel**, which Windows Terminal can display. You keep typing `ssh user@server`. Nothing has to be
installed on the server.

<!-- Screenshot: add docs/screenshot.png and uncomment
![terminal-browser over SSH in Windows Terminal](docs/screenshot.png)
-->

## Why

terminal-browser draws web pages exclusively with the
[kitty graphics protocol](https://sw.kovidgoyal.net/kitty/graphics-protocol/). Windows Terminal does not
support that protocol, but it has supported **Sixel** since version 1.22. Without a translator,
terminal-browser falls back to a coarse text mode when you connect from Windows.

## How it works

```
 Linux server                         Windows
┌──────────────────┐   SSH   ┌──────────────────────────────────────┐   ┌──────────────────┐
│ terminal-browser │ ──────► │ tbssh                                │   │ Windows Terminal │
│  (kitty graphics)│         │  VT tokenizer ─► text passes through ├──►│  (text + Sixel)  │
│                  │ ◄────── │       └─► kitty cmds ─► compositor   │   │                  │
└──────────────────┘ replies │                  └─► Sixel encoder ──┘   └──────────────────┘
                     + size  └──────────────────────────────────────┘
```

tbssh sits between the server and Windows Terminal and does three things on top of forwarding bytes:

1. **Translates graphics.** kitty images, partial frame updates and delete commands go into a local
   compositor (images, placements, z-order). The result is emitted as Sixel, and only pixels that
   changed are sent to the terminal.
2. **Answers capability queries.** On startup terminal-browser asks whether the terminal speaks kitty
   graphics and how large a cell is in pixels. tbssh answers on the terminal's behalf with a 10×20 px
   cell, exactly Windows Terminal's Sixel grid.
3. **Reports pixel size.** The real window size in pixels goes to the server in the PTY request and on
   every resize, so terminal-browser renders at the right resolution.

## Requirements

- Windows 10/11 with **Windows Terminal 1.22 or newer**
- **PowerShell 7.2+** (PowerShell 7 Preview works too)
- The built-in **Windows OpenSSH client** (`ssh -V`). tbssh uses it to resolve your `~/.ssh/config`
  and hands everything it does not handle itself over to it.
- To build from source: **Go** (`winget install GoLang.Go`)

## Installation

### Option A: prebuilt release (no Go needed)

1. Download `tbssh-<version>-windows-amd64.zip` (or `-arm64`) from the
   [latest release](https://github.com/zfzfg/tbssh/releases/latest).
2. Extract it and run the installer in PowerShell 7:

   ```powershell
   cd <extracted folder>
   .\install.ps1
   ```

### Option B: from source

```powershell
git clone https://github.com/zfzfg/tbssh.git
cd tbssh
.\scripts\install.ps1           # builds with Go; without Go it downloads the latest release
```

Either way the installer puts the `TerminalBrowserSsh` module into `Documents\PowerShell\Modules` and adds
`Import-Module TerminalBrowserSsh` to your PowerShell profile (shared by PowerShell 7 and 7 Preview).
From then on, `ssh` in every new window runs through tbssh. Pass `-NoProfile` to skip the profile change.

### Option C: just the binary

```powershell
go install github.com/zfzfg/tbssh/cmd/tbssh@latest
```

This gives you `tbssh.exe` without the PowerShell shim; call it like `ssh`.

## Quick check

```powershell
Test-TbSsh        # reports whether Windows Terminal supports Sixel, and the cell size
Show-TbSshDemo    # draws a gradient, a grid and a yellow ball through the bridge
```

## Usage

```powershell
ssh user@server
# on the server:
terminal-browser open https://example.com
```

### Optional: less bandwidth

By default terminal-browser re-sends the whole frame over SSH on every change. A small snippet in the
server's shell profile makes it send only the regions that changed:

```powershell
Install-TbSshRemote user@server     # undo with: Uninstall-TbSshRemote user@server
```

The snippet only takes effect in sessions opened through tbssh. tbssh marks those by sending
`LC_TERMINAL=tbssh`, which a default sshd accepts (`AcceptEnv LANG LC_*`).

## What gets handed to ssh.exe

tbssh only takes over **interactive sessions with a terminal**. Everything else runs the real
`ssh.exe` unchanged, for example:

- commands without a TTY: `ssh host ls`, pipes, redirects, `-T`
- `-N`, `-f`, `-W`, `-X`/`-Y`, `-O`/`-M`/`-S`, `-s`, `-G`, `-V`
- `SessionType`, `LocalCommand` and `ControlMaster` from your config

scp, sftp and git call `ssh.exe` directly anyway.

In interactive mode tbssh supports:

| Area | Supported |
|---|---|
| Configuration | everything `ssh -G` resolves: `Host`, `HostName`, `User`, `Port`, `IdentityFile`, `IdentitiesOnly`, `Include`, `Match`, `-o …` |
| Authentication | Windows ssh-agent, key files with passphrase prompt, certificates, keyboard-interactive (2FA), password |
| Host keys | `known_hosts` with `StrictHostKeyChecking` `ask`, `yes`, `accept-new`, `no` |
| Proxies | `ProxyJump`, `ProxyCommand` |
| Forwarding | `ForwardAgent` (`-A`), `-L`, `-R`, `-D` |
| Session | `SendEnv`/`SetEnv`, `ServerAliveInterval`, escape sequences `~.` and `~?`, remote exit code |

## Configuration

| Environment variable | Meaning |
|---|---|
| `TBSSH_GRAPHICS=auto\|on\|off` | Graphics translation. `auto` (default) enables it when the terminal reports Sixel support. |
| `TBSSH_FPS=60` | Frame-rate cap for unsynchronized updates |
| `TBSSH_SSH=path` | Use a different OpenSSH client for config resolution and pass-through |
| `TBSSH_DEBUG=1` | Log to `%LOCALAPPDATA%\tbssh\tbssh.log`, with statistics every 5 s |
| `TBSSH_RECORD=file` | Record raw server output; replay it with `tbssh --replay file` |
| `TBSSH_NO_SHIM=1` | Set before `Import-Module` to load the module without replacing `ssh` |

### Bypassing and uninstalling

- One command: call `ssh.exe …` explicitly.
- Current window: `Disable-TbSshShim` (turn back on with `Enable-TbSshShim`).
- Everywhere: run `uninstall.ps1` (in the release ZIP, or `scripts\uninstall.ps1` in the repo). It removes
  the module and the profile line. Run `Uninstall-TbSshRemote user@server` first for servers where you
  used `Install-TbSshRemote`.

## Known limitations

- **Windows Terminal key bindings win**, e.g. `ctrl+shift+f` (search), `ctrl+shift+t`, `ctrl+shift+w`.
  Unbind them in Windows Terminal under *Settings → Actions*, or use terminal-browser's command palette
  (`ctrl+k`).
- Windows Terminal does not implement the kitty keyboard protocol, so some combinations are
  indistinguishable, e.g. `ctrl+shift+i` and Tab. Use `F12` for DevTools.
- **Mouse positions** are reported per cell, not per pixel.
- With **Windows display scaling above 100 %** the image looks slightly soft: Windows Terminal draws
  Sixel on a fixed 10×20 grid and scales it to the font size.
- Colors go through a fixed 256-color palette with light dithering. Text stays crisp, photos show a
  faint pattern.
- Animated kitty images (frames other than the first) and Unicode placeholders are not shown.

## Troubleshooting

- `tbssh --doctor` (or `Test-TbSsh`) checks Sixel support and the cell size reported by the terminal.
- No graphics, only text? Make sure you are in Windows Terminal ≥ 1.22, not the legacy console host, and
  try `TBSSH_GRAPHICS=on`.
- Set `TBSSH_DEBUG=1` and look at `%LOCALAPPDATA%\tbssh\tbssh.log`.
- For rendering bugs, record a session with `TBSSH_RECORD=session.bin`, reproduce the problem, and
  replay it locally with `tbssh --replay session.bin`. Attaching that file to an issue helps a lot
  (it contains everything the server sent, so check it for anything private first).

## Development

```
cmd/tbssh              entry point
internal/app           session, pass-through to ssh.exe, --doctor/--demo/--replay
internal/sshclient     ssh -G, dialing, auth, known_hosts, forwarding, PTY with pixel size
internal/console       Windows console: raw mode, input, size, terminal queries
internal/bridge        intercepting kitty commands, answering queries, output pacing
internal/vtstream      byte-exact VT tokenizer and cursor tracking
internal/kitty         kitty graphics: commands, chunking, zlib/RGB/RGBA/PNG
internal/compositor    images, placements, z-order, frame edits, deletes
internal/sixel         Sixel encoder (fixed palette, changed pixels only, DECSDM)
powershell/            PowerShell module (ssh shim, Install-TbSshRemote, …)
scripts/               install.ps1, uninstall.ps1
```

```powershell
go test ./...
go build -o bin\tbssh.exe .\cmd\tbssh
```

The tests include an end-to-end test with an embedded SSH server that behaves like terminal-browser:
it sends the kitty capability query, waits for the replies, and then sends a frame.

Releases are built by GitHub Actions: pushing a tag `vX.Y.Z` builds amd64 and arm64 ZIPs with a
`SHA256SUMS.txt` and publishes them as a GitHub release.

## Credits

- [terminal-browser](https://github.com/zenbu-labs/terminal-browser) by zenbu-labs, the program this
  bridge exists for. tbssh is an independent project and is not affiliated with zenbu-labs.
- The Sixel path (display mode, transparent images with only changed pixels, fixed palette) follows the
  proposal in [zenbu-labs/terminal-browser#205](https://github.com/zenbu-labs/terminal-browser/pull/205),
  which aims to add Windows Terminal support to terminal-browser itself.

## License

[MIT](LICENSE) © 2026 Collin Lerche (zfzfg)
