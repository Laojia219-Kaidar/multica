package main

import (
	"flag"
	"io"
	"log"
	"net"
	"os"
	"strings"
)

func main() {
	port := flag.String("port", "3151", "port")
	target := flag.String("target", "127.0.0.1:3150", "target")
	flag.Parse()
	bind := strings.TrimSpace(os.Getenv("HIVECOSM_AUTHORITY_BRIDGE_BIND_ADDR"))
	ip := net.ParseIP(bind)
	if ip == nil || ip.To4() == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() {
		log.Fatal("bridge bind address must be explicit non-loopback IPv4")
	}
	l, e := net.Listen("tcp4", net.JoinHostPort(bind, *port))
	if e != nil {
		log.Fatal(e)
	}
	defer l.Close()
	for {
		c, e := l.Accept()
		if e != nil {
			log.Fatal(e)
		}
		go proxy(c, *target)
	}
}
func proxy(c net.Conn, t string) {
	defer c.Close()
	u, e := net.Dial("tcp4", t)
	if e != nil {
		return
	}
	defer u.Close()
	d := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(u, c); d <- struct{}{} }()
	go func() { _, _ = io.Copy(c, u); d <- struct{}{} }()
	<-d
}
