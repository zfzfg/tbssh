# tbssh – terminal-browser über SSH in Windows Terminal

`tbssh` sorgt dafür, dass [terminal-browser](https://github.com/zenbu-labs/terminal-browser) auf einem
Linux-Server auch dann ein echtes Browserbild zeigt, wenn du dich aus **PowerShell 7 in Windows Terminal**
per `ssh` verbindest. Du verbindest dich weiter ganz normal mit `ssh user@server`.

## Warum das nötig ist

terminal-browser zeichnet nur über das **kitty graphics protocol**. Windows Terminal kann dieses
Protokoll nicht, aber **Sixel** (ab Version 1.22). Ohne Hilfe fällt terminal-browser deshalb auf
einen groben Textmodus zurück.

`tbssh` ist ein SSH-Client, der zwischen Server und Windows Terminal sitzt. Er leitet alles
unverändert weiter und macht nur drei Dinge zusätzlich:

1. **kitty-Grafik abfangen:** Bilder, Teilbilder und Löschbefehle landen in einem lokalen
   Compositor und werden als **Sixel** ausgegeben. Dabei werden nur Pixel übertragen, die sich
   geändert haben.
2. **Abfragen beantworten:** terminal-browser fragt beim Start, ob das Terminal kitty-Grafik kann
   und wie groß eine Zelle in Pixeln ist. tbssh antwortet passend, mit 10×20 px pro Zelle. Das ist
   genau das Sixel-Raster von Windows Terminal.
3. **Pixelgröße melden:** Die echte Fenstergröße in Pixeln geht über die SSH-Verbindung an den
   Server, auch beim Ändern der Fenstergröße.

Auf dem Server muss nichts installiert werden.

## Voraussetzungen

- Windows 10/11 mit **Windows Terminal 1.22 oder neuer**
- **PowerShell 7** (auch 7 Preview)
- der Windows-eigene **OpenSSH-Client** (normalerweise vorinstalliert, `ssh -V`). tbssh liest
  darüber deine `~/.ssh/config` aus.
- zum Bauen: **Go** (`winget install GoLang.Go`)

## Installation

```powershell
cd <dieses Repository>
.\scripts\install.ps1
```

Das Skript baut `tbssh.exe` und installiert das Modul `TerminalBrowserSsh` nach
`Dokumente\PowerShell\Modules`. Außerdem trägt es `Import-Module TerminalBrowserSsh` in dein
PowerShell-Profil ein; PowerShell 7 und 7 Preview nutzen dasselbe Profil. In jedem neuen Fenster
läuft `ssh` dann über tbssh.

Danach kurz prüfen:

```powershell
Test-TbSsh        # meldet, ob Windows Terminal Sixel kann, und die Zellgröße
Show-TbSshDemo    # Farbverlauf mit Gitter und eine gelbe Kugel, gezeichnet über die Bridge
```

## Benutzung

```powershell
ssh user@server
# auf dem Server:
terminal-browser open https://example.com
```

### Optional: weniger Bandbreite

Ohne weitere Einstellung schickt terminal-browser über SSH bei jeder Änderung das ganze Bild.
Mit einem kleinen Eintrag im Shell-Profil auf dem Server schickt es nur die geänderten Bereiche:

```powershell
Install-TbSshRemote user@server     # wieder entfernen: Uninstall-TbSshRemote user@server
```

Der Eintrag greift nur, wenn du über tbssh verbunden bist. tbssh sendet dafür
`LC_TERMINAL=tbssh`; das akzeptiert der Standard-sshd (`AcceptEnv LANG LC_*`).

## Was an ssh.exe weitergereicht wird

tbssh übernimmt nur **interaktive Sitzungen mit Terminal**. Alles andere startet unverändert das
echte `ssh.exe`, zum Beispiel:

- Befehle ohne Terminal: `ssh host ls`, Pipes, Umleitungen, `-T`
- `-N`, `-f`, `-W`, `-X`/`-Y`, `-O`/`-M`/`-S`, `-s`, `-G`, `-V`
- SessionType, LocalCommand und ControlMaster aus der Konfiguration

scp, sftp und git benutzen ohnehin direkt `ssh.exe`.

Im interaktiven Modus unterstützt tbssh:

- Konfiguration: alles, was `ssh -G` auflöst (Host, HostName, User, Port, IdentityFile,
  IdentitiesOnly, Include, Match, `-o …`)
- Anmeldung: Windows-ssh-agent, Schlüsseldateien mit Passphrase-Abfrage, Zertifikate,
  Keyboard-Interactive (2FA), Passwort
- known_hosts mit StrictHostKeyChecking: `ask`, `yes`, `accept-new`, `no`
- ProxyJump und ProxyCommand
- ForwardAgent (`-A`), `-L`, `-R`, `-D`
- SendEnv/SetEnv, ServerAliveInterval
- Escape-Zeichen `~.` und `~?`; der Exit-Code der Gegenseite wird durchgereicht

Zum Umgehen: `ssh.exe …` aufrufen oder `Disable-TbSshShim` (nur für das aktuelle Fenster).
Dauerhaft abschalten: `$env:TBSSH_NO_SHIM = 1` vor dem `Import-Module` setzen, oder die Zeile aus
dem Profil entfernen.

## Einstellungen (Umgebungsvariablen)

| Variable | Bedeutung |
|---|---|
| `TBSSH_GRAPHICS=auto\|on\|off` | Grafik-Übersetzung. `auto` (Standard) schaltet sie ein, wenn das Terminal Sixel meldet. |
| `TBSSH_FPS=60` | Bildraten-Grenze für nicht synchronisierte Updates |
| `TBSSH_SSH=pfad` | anderer OpenSSH-Client für Konfiguration und Weitergabe |
| `TBSSH_DEBUG=1` | Log nach `%LOCALAPPDATA%\tbssh\tbssh.log`, mit Statistik alle 5 s |
| `TBSSH_RECORD=datei` | rohe Server-Ausgabe aufzeichnen; abspielen mit `tbssh --replay datei` |

## Bekannte Grenzen

- **Tastenkürzel von Windows Terminal** haben Vorrang, z. B. `ctrl+shift+f` (Suche),
  `ctrl+shift+t` und `ctrl+shift+w`. Löse sie in den WT-Einstellungen unter „Aktionen“, oder nutze
  die Befehlspalette von terminal-browser (`ctrl+k`).
- Windows Terminal unterstützt das kitty-Tastaturprotokoll nicht. Einige Kombinationen sind deshalb
  nicht unterscheidbar, z. B. `ctrl+shift+i` und Tab; für die DevTools nimm `F12`.
- **Mauspositionen** kommen zellgenau an, nicht pixelgenau.
- Bei **Windows-Skalierung über 100 %** wirkt das Bild etwas weich: Windows Terminal zeichnet Sixel
  in einem festen 10×20-Raster und skaliert es auf die Schriftgröße.
- Die Farben laufen über eine feste 256-Farben-Palette mit leichtem Dithering. Text bleibt scharf,
  Fotos zeigen leichtes Raster.
- Animierte kitty-Bilder (Frames außer dem ersten) und Unicode-Platzhalter werden nicht angezeigt.

## Aufbau

```
cmd/tbssh              Einstiegspunkt
internal/app           Sitzung, Weitergabe an ssh.exe, --doctor/--demo/--replay
internal/sshclient     ssh -G, Verbindung, Anmeldung, known_hosts, Weiterleitungen, PTY mit Pixelgröße
internal/console       Windows-Konsole: Raw-Modus, Eingabe, Größe, Terminal-Abfragen
internal/bridge        Abfangen der kitty-Befehle, Antworten auf Abfragen, Taktung der Ausgabe
internal/vtstream      byte-genauer VT-Tokenizer und Cursor-Verfolgung
internal/kitty         kitty-Grafik: Befehle, Chunks, zlib/RGB/RGBA/PNG
internal/compositor    Bilder, Platzierungen, z-Reihenfolge, Frame-Edits, Löschen
internal/sixel         Sixel-Encoder (feste Palette, nur geänderte Pixel, DECSDM)
powershell/            PowerShell-Modul (ssh-Shim, Install-TbSshRemote, …)
scripts/install.ps1    Bauen und Installieren
```

Tests: `go test ./...`. Sie enthalten einen End-to-End-Test mit einem eingebauten SSH-Server, der
sich wie terminal-browser verhält: Er stellt die kitty-Abfrage, wartet auf die Antworten und
sendet einen Frame.

Der Sixel-Weg (Display-Modus, transparente Bilder mit nur geänderten Pixeln, feste Palette) folgt
dem Vorschlag in [zenbu-labs/terminal-browser#205](https://github.com/zenbu-labs/terminal-browser/pull/205),
mit dem terminal-browser selbst Windows Terminal unterstützen soll.
