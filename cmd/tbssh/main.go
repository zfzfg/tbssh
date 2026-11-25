//go:build windows

// Command tbssh is an ssh client for Windows Terminal that shows
// terminal-browser (and other kitty graphics programs) running on the
// remote host, by translating kitty graphics into sixel.
package main

import (
	"os"

	"github.com/zfzfg/tbssh/internal/app"
)

func main() {
	os.Exit(app.Main(os.Args[1:]))
}
