package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"time"
)

const (
	bridgePort     = "3151"
	targetAddress  = "127.0.0.1:3150"
	maxConnections = 32
	dialTimeout    = 3 * time.Second
	copyTimeout    = 15 * time.Second
)

type proxyConfig struct {
	target         string
	maxConnections int
	dialTimeout    time.Duration
	copyTimeout    time.Duration
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() (retErr error) {
	bind := strings.TrimSpace(os.Getenv("HIVECOSM_AUTHORITY_BRIDGE_BIND_ADDR"))
	if err := validateBindAddress(bind); err != nil {
		return err
	}

	listener, err := net.Listen("tcp4", net.JoinHostPort(bind, bridgePort))
	if err != nil {
		return fmt.Errorf("listen on bridge address: %w", err)
	}
	defer func() {
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			retErr = errors.Join(retErr, fmt.Errorf("close listener: %w", err))
		}
	}()

	return serve(listener, proxyConfig{
		target:         targetAddress,
		maxConnections: maxConnections,
		dialTimeout:    dialTimeout,
		copyTimeout:    copyTimeout,
	}, func(err error) {
		log.Printf("authority bridge connection failed: %v", err)
	})
}

func validateBindAddress(bind string) error {
	ip := net.ParseIP(bind)
	if ip == nil || ip.To4() == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() {
		return errors.New("bridge bind address must be explicit non-loopback IPv4")
	}
	return nil
}

func serve(listener net.Listener, cfg proxyConfig, report func(error)) error {
	if cfg.maxConnections <= 0 {
		return errors.New("max connections must be positive")
	}
	if report == nil {
		report = func(error) {}
	}

	sem := make(chan struct{}, cfg.maxConnections)
	for {
		client, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept bridge connection: %w", err)
		}

		select {
		case sem <- struct{}{}:
			go func() {
				defer func() { <-sem }()
				if err := proxyConnection(client, cfg); err != nil {
					report(err)
				}
			}()
		default:
			if err := client.Close(); err != nil && !isExpectedCloseError(err) {
				report(fmt.Errorf("close over-limit client: %w", err))
			}
		}
	}
}

func proxyConnection(client net.Conn, cfg proxyConfig) (retErr error) {
	defer func() {
		if err := client.Close(); err != nil && !isExpectedCloseError(err) {
			retErr = errors.Join(retErr, fmt.Errorf("close client: %w", err))
		}
	}()

	upstream, err := net.DialTimeout("tcp4", cfg.target, cfg.dialTimeout)
	if err != nil {
		return fmt.Errorf("dial target %s: %w", cfg.target, err)
	}
	defer func() {
		if err := upstream.Close(); err != nil && !isExpectedCloseError(err) {
			retErr = errors.Join(retErr, fmt.Errorf("close target: %w", err))
		}
	}()

	deadline := time.Now().Add(cfg.copyTimeout)
	if err := client.SetDeadline(deadline); err != nil {
		return fmt.Errorf("set client deadline: %w", err)
	}
	if err := upstream.SetDeadline(deadline); err != nil {
		return fmt.Errorf("set target deadline: %w", err)
	}

	type copyResult struct {
		direction string
		err       error
	}
	results := make(chan copyResult, 2)
	go func() {
		results <- copyResult{direction: "client-to-target", err: copyAndHalfClose(upstream, client)}
	}()
	go func() {
		results <- copyResult{direction: "target-to-client", err: copyAndHalfClose(client, upstream)}
	}()

	for range 2 {
		result := <-results
		if result.err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("%s: %w", result.direction, result.err))
		}
	}
	return retErr
}

func copyAndHalfClose(dst, src net.Conn) error {
	_, copyErr := io.Copy(dst, src)
	if copyErr != nil && isExpectedCloseError(copyErr) {
		copyErr = nil
	}

	var closeErr error
	if tcp, ok := dst.(interface{ CloseWrite() error }); ok {
		closeErr = tcp.CloseWrite()
		if closeErr != nil && isExpectedCloseError(closeErr) {
			closeErr = nil
		}
	}

	return errors.Join(copyErr, closeErr)
}

func isExpectedCloseError(err error) bool {
	return err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed)
}
