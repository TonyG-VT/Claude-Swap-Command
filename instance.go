// Modified for Claude Swap Command (2026).
package main

import (
	"bufio"
	"log"
	"net"
	"time"
)

// Only one engine or installer may own persistent state. Keep the legacy port
// stable so old and new tray builds cannot run together. An upgrade must wait
// for the existing helper to quit; a newer version never takes over a live one.
const instancePort = "127.0.0.1:47823"

// lockInstance takes the single-instance port, without sending any takeover
// request. Any listener, including an old version or installer, blocks startup.
func lockInstance(addr string) (net.Listener, bool) {
	if l, err := net.Listen("tcp", addr); err == nil {
		return l, true
	}
	log.Print("Another switcher is already running; quit it before starting Claude Swap Command")
	return nil, false
}

// serveInstance retains the old handshake for compatibility, but never invokes
// quit. Read only a bounded line even when an unrelated local client connects.
func serveInstance(l net.Listener, _ func()) {
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		line, _ := bufio.NewReaderSize(conn, 256).ReadSlice('\n')
		_ = line // A newer binary must never take over a live switcher.
		conn.Write([]byte("stay\n"))
		conn.Close()
	}
}
