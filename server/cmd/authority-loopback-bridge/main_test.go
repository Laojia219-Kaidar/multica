package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

const testTimeout = 3 * time.Second

func TestBridgeConstantsAreLocked(t *testing.T) {
	if bridgePort != "3151" || targetAddress != "127.0.0.1:3150" || maxConnections != 32 {
		t.Fatal("authority bridge endpoint or connection limit drifted")
	}
}

func TestValidateBindAddress(t *testing.T) {
	for _, bind := range []string{"", "localhost", "127.0.0.1", "0.0.0.0", "::", "224.0.0.1"} {
		t.Run(bind, func(t *testing.T) {
			if err := validateBindAddress(bind); err == nil {
				t.Fatalf("validateBindAddress(%q) unexpectedly succeeded", bind)
			}
		})
	}
	if err := validateBindAddress("172.19.0.1"); err != nil {
		t.Fatalf("explicit bridge IPv4 rejected: %v", err)
	}
}

func TestProxyFullDuplexAndClientCloseWrite(t *testing.T) {
	target, targetErrors := startTarget(t, func(conn *net.TCPConn) error {
		if _, err := conn.Write([]byte("ready:")); err != nil {
			return fmt.Errorf("write greeting: %w", err)
		}
		request, err := io.ReadAll(conn)
		if err != nil {
			return fmt.Errorf("read request: %w", err)
		}
		if _, err := conn.Write(append([]byte("response:"), request...)); err != nil {
			return fmt.Errorf("write response: %w", err)
		}
		return conn.CloseWrite()
	})

	bridge, bridgeErrors, stopBridge := startBridge(t, target.Addr().String(), 32)
	defer stopBridge()

	client := dialTCP(t, bridge.Addr().String())
	defer client.Close()
	if err := client.SetDeadline(time.Now().Add(testTimeout)); err != nil {
		t.Fatal(err)
	}

	greeting := make([]byte, len("ready:"))
	if _, err := io.ReadFull(client, greeting); err != nil {
		t.Fatalf("read greeting before request half-close: %v", err)
	}
	if string(greeting) != "ready:" {
		t.Fatalf("unexpected greeting %q", greeting)
	}
	if _, err := client.Write([]byte("payload")); err != nil {
		t.Fatalf("write request: %v", err)
	}
	if err := client.CloseWrite(); err != nil {
		t.Fatalf("half-close request: %v", err)
	}
	response, err := io.ReadAll(client)
	if err != nil {
		t.Fatalf("read response after CloseWrite: %v", err)
	}
	if string(response) != "response:payload" {
		t.Fatalf("incomplete response after CloseWrite: %q", response)
	}

	assertNoAsyncError(t, targetErrors)
	assertNoReportedError(t, bridgeErrors)
}

func TestTargetUnavailableClosesClientAndReportsError(t *testing.T) {
	unavailable := reserveThenCloseAddress(t)
	bridge, bridgeErrors, stopBridge := startBridge(t, unavailable, 32)
	defer stopBridge()

	client := dialTCP(t, bridge.Addr().String())
	defer client.Close()
	if err := client.SetReadDeadline(time.Now().Add(testTimeout)); err != nil {
		t.Fatal(err)
	}
	one := make([]byte, 1)
	if n, err := client.Read(one); n != 0 || err == nil {
		t.Fatalf("unavailable target left client open: n=%d err=%v", n, err)
	}

	select {
	case err := <-bridgeErrors:
		if err == nil || !strings.Contains(err.Error(), "dial target") {
			t.Fatalf("unexpected bridge error: %v", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("bridge did not report unavailable target")
	}
}

func TestConnectionLimitClosesOverLimitClient(t *testing.T) {
	accepted := make(chan *net.TCPConn, 1)
	release := make(chan struct{})
	target, targetErrors := startTarget(t, func(conn *net.TCPConn) error {
		accepted <- conn
		<-release
		return nil
	})
	bridge, bridgeErrors, stopBridge := startBridge(t, target.Addr().String(), 1)
	defer stopBridge()

	first := dialTCP(t, bridge.Addr().String())
	defer first.Close()
	select {
	case <-accepted:
	case <-time.After(testTimeout):
		t.Fatal("first connection did not occupy the bridge slot")
	}

	second := dialTCP(t, bridge.Addr().String())
	defer second.Close()
	if err := second.SetReadDeadline(time.Now().Add(testTimeout)); err != nil {
		t.Fatal(err)
	}
	one := make([]byte, 1)
	if n, err := second.Read(one); n != 0 || err == nil {
		t.Fatalf("over-limit connection was not closed: n=%d err=%v", n, err)
	}

	close(release)
	if err := first.CloseWrite(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatalf("release first connection: %v", err)
	}
	assertNoAsyncError(t, targetErrors)
	assertNoReportedError(t, bridgeErrors)
}

func startTarget(t *testing.T, handler func(*net.TCPConn) error) (*net.TCPListener, <-chan error) {
	t.Helper()
	listener := listenTCP(t)
	errorsOut := make(chan error, 1)
	go func() {
		conn, err := listener.AcceptTCP()
		if err != nil {
			errorsOut <- err
			return
		}
		defer conn.Close()
		errorsOut <- handler(conn)
	}()
	t.Cleanup(func() { _ = listener.Close() })
	return listener, errorsOut
}

func startBridge(t *testing.T, target string, limit int) (*net.TCPListener, <-chan error, func()) {
	t.Helper()
	listener := listenTCP(t)
	reported := make(chan error, 8)
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- serve(listener, proxyConfig{
			target:         target,
			maxConnections: limit,
			dialTimeout:    time.Second,
			copyTimeout:    testTimeout,
		}, func(err error) { reported <- err })
	}()

	var once sync.Once
	stop := func() {
		once.Do(func() {
			if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				t.Errorf("close bridge listener: %v", err)
			}
			select {
			case err := <-serveDone:
				if err != nil {
					t.Errorf("serve returned error: %v", err)
				}
			case <-time.After(testTimeout):
				t.Error("bridge server did not stop")
			}
		})
	}
	t.Cleanup(stop)
	return listener, reported, stop
}

func listenTCP(t *testing.T) *net.TCPListener {
	t.Helper()
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return listener
}

func dialTCP(t *testing.T, address string) *net.TCPConn {
	t.Helper()
	conn, err := net.DialTimeout("tcp4", address, testTimeout)
	if err != nil {
		t.Fatalf("dial %s: %v", address, err)
	}
	return conn.(*net.TCPConn)
}

func reserveThenCloseAddress(t *testing.T) string {
	t.Helper()
	listener := listenTCP(t)
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close reserved listener: %v", err)
	}
	return address
}

func assertNoAsyncError(t *testing.T, errorsIn <-chan error) {
	t.Helper()
	select {
	case err := <-errorsIn:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for asynchronous operation")
	}
}

func assertNoReportedError(t *testing.T, errorsIn <-chan error) {
	t.Helper()
	select {
	case err := <-errorsIn:
		t.Fatalf("unexpected bridge error: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
}
