package main

import (
	"io"
	"log"
	"net"
	"os"
	"strings"
	"time"
)

const (
	bridgePort    = "3151"
	targetAddress = "127.0.0.1:3150"
	maxConnections = 32
	copyTimeout = 15 * time.Second
)

func main() {
	bind := strings.TrimSpace(os.Getenv("HIVECOSM_AUTHORITY_BRIDGE_BIND_ADDR"))
	ip := net.ParseIP(bind)
	if ip == nil || ip.To4() == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() {
		log.Fatal("bridge bind address must be explicit non-loopback IPv4")
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort(bind, bridgePort))
	if err != nil { log.Fatal(err) }
	defer listener.Close()
	sem := make(chan struct{}, maxConnections)
	for {
		client, err := listener.Accept()
		if err != nil { log.Fatal(err) }
		select {
		case sem <- struct{}{}:
			go func() { defer func() { <-sem }(); proxy(client) }()
		default:
			_ = client.Close()
		}
	}
}

func proxy(client net.Conn) {
	defer client.Close()
	upstream, err := net.DialTimeout("tcp4", targetAddress, 3*time.Second)
	if err != nil { return }
	defer upstream.Close()
	_ = client.SetDeadline(time.Now().Add(copyTimeout))
	_ = upstream.SetDeadline(time.Now().Add(copyTimeout))
	done := make(chan struct{}, 2)
	go copyDirection(upstream, client, done)
	go copyDirection(client, upstream, done)
	<-done
	<-done
}

func copyDirection(dst, src net.Conn, done chan<- struct{}) {
	_, _ = io.Copy(dst, src)
	if tcp, ok := dst.(*net.TCPConn); ok { _ = tcp.CloseWrite() }
	done <- struct{}{}
}
