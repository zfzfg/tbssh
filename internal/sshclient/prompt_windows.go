//go:build windows

package sshclient

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf16"

	"golang.org/x/sys/windows"
	"golang.org/x/term"
)

// ConsolePrompter asks on the console, before raw mode is switched on.
type ConsolePrompter struct{}

// Secret reads a line without echo.
func (ConsolePrompter) Secret(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprint(os.Stderr, "\r\n")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Line reads a line with echo.
func (ConsolePrompter) Line(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	h := windows.Handle(os.Stdin.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return "", errors.New("no console for the question")
	}
	cooked := mode | windows.ENABLE_ECHO_INPUT | windows.ENABLE_LINE_INPUT | windows.ENABLE_PROCESSED_INPUT
	cooked &^= windows.ENABLE_VIRTUAL_TERMINAL_INPUT
	windows.SetConsoleMode(h, cooked)
	defer windows.SetConsoleMode(h, mode)
	buf := make([]uint16, 1024)
	var n uint32
	if err := windows.ReadConsole(h, &buf[0], uint32(len(buf)), &n, nil); err != nil {
		return "", err
	}
	s := string(utf16.Decode(buf[:n]))
	return strings.TrimRight(s, "\r\n"), nil
}
