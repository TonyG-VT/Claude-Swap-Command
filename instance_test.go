// Modified for Claude Swap Command (2026).
package main

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"
)

// Only one copy of the helper may run. Whoever knocks on the running copy's
// port is told to stay away, and a second copy cannot take the port.
func TestOnlyOneCopyOfTheHelperRuns(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	quit := make(chan struct{})
	go serveInstance(l, func() { close(quit) })
	addr := l.Addr().String()

	for _, greeting := range []string{"claude-account-switcher " + version, "claude-account-switcher 99.0.0", "anything else"} {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := conn.Write([]byte(greeting + "\n")); err != nil {
			t.Fatal(err)
		}
		answer, _ := bufio.NewReader(conn).ReadString('\n')
		conn.Close()
		if strings.TrimSpace(answer) != "stay" {
			t.Fatalf("answer to %q = %q, want stay", greeting, answer)
		}
	}
	select {
	case <-quit:
		t.Fatal("another program made the running helper quit")
	case <-time.After(20 * time.Millisecond):
	}
	if _, ok := lockInstance(addr); ok {
		t.Fatal("two copies of the helper got the same lock")
	}
}
