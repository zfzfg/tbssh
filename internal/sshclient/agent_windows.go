//go:build windows

package sshclient

import (
	"net"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
)

func dialAgent(path string) (net.Conn, error) {
	if strings.HasPrefix(path, `\\.\pipe\`) || strings.HasPrefix(path, `//./pipe/`) {
		timeout := 2 * time.Second
		return winio.DialPipe(strings.ReplaceAll(path, "/", `\`), &timeout)
	}
	return net.DialTimeout("unix", path, 2*time.Second)
}
