package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// coordinatorServers serves the API on one or two listeners: plain HTTP on
// loopback and, when configured, TLS on a specific non-loopback address.
type coordinatorServers struct {
	httpSrv   *http.Server
	tlsSrv    *http.Server
	loopback  net.Listener
	tlsListen net.Listener
	errCh     chan error
}

func (s *coordinatorServers) loopbackAddr() string {
	if s.loopback == nil {
		return ""
	}
	return s.loopback.Addr().String()
}

// loopbackURL preserves the listener's address family and any specific loopback
// address. Wildcard listeners are reached through the matching loopback address.
func (s *coordinatorServers) loopbackURL() string {
	host, port, _ := net.SplitHostPort(s.loopbackAddr())
	switch host {
	case "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

func (s *coordinatorServers) tlsAddr() string {
	if s.tlsListen == nil {
		return ""
	}
	return s.tlsListen.Addr().String()
}

func (s *coordinatorServers) start() {
	s.errCh = make(chan error, 2)
	if s.loopback != nil {
		go func() { s.errCh <- s.httpSrv.Serve(s.loopback) }()
	}
	if s.tlsListen != nil {
		go func() { s.errCh <- s.tlsSrv.Serve(s.tlsListen) }()
	}
}

func (s *coordinatorServers) waitErr() error {
	if s.errCh == nil {
		return nil
	}
	return <-s.errCh
}

func (s *coordinatorServers) shutdown(ctx context.Context) {
	if s.httpSrv != nil {
		_ = s.httpSrv.Shutdown(ctx)
	}
	if s.tlsSrv != nil {
		_ = s.tlsSrv.Shutdown(ctx)
	}
}

// listenHostIsWildcard reports bind-all-interfaces addresses.
func listenHostIsWildcard(host string) bool {
	host = strings.TrimSpace(host)
	return host == "0.0.0.0" || host == "::"
}

// listenHostAllowsPlainHTTP reports addresses that may use plain HTTP without TLS.
func listenHostAllowsPlainHTTP(host string) bool {
	host = strings.TrimSpace(host)
	if listenHostIsWildcard(host) {
		return true
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// listenHostRequiresTLS reports whether addr binds outside loopback-only use.
func listenHostRequiresTLS(host string) bool {
	return !listenHostAllowsPlainHTTP(host)
}

func splitListenHostPort(addr string) (host, port string, err error) {
	host, port, err = net.SplitHostPort(addr)
	if err != nil {
		return "", "", err
	}
	if port == "" {
		return "", "", errors.New("listen address requires a port")
	}
	return host, port, nil
}

func validateListenConfig(addr, certFile, keyFile string) error {
	host, _, err := splitListenHostPort(addr)
	if err != nil {
		return fmt.Errorf("listen address: %w", err)
	}
	if listenHostAllowsPlainHTTP(host) {
		if certFile != "" || keyFile != "" {
			return errors.New("tlsCert and tlsKey require a specific non-loopback addr (not 0.0.0.0, ::, or loopback)")
		}
		return nil
	}
	if certFile == "" || keyFile == "" {
		return errors.New("non-loopback addr requires tlsCert and tlsKey (see service.md for LAN/tailnet setup)")
	}
	return nil
}

func newHTTPServer(handler http.Handler, addr string) *http.Server {
	return &http.Server{
		Addr:         addr,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
}

func startCoordinatorServers(handler http.Handler, addr, certFile, keyFile string) (*coordinatorServers, error) {
	if err := validateListenConfig(addr, certFile, keyFile); err != nil {
		return nil, err
	}
	host, port, err := splitListenHostPort(addr)
	if err != nil {
		return nil, err
	}

	out := &coordinatorServers{}
	if listenHostRequiresTLS(host) {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("loading tls certificate: %w", err)
		}
		tlsCfg := &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
		tlsListen, err := tls.Listen("tcp", net.JoinHostPort(host, port), tlsCfg)
		if err != nil {
			return nil, fmt.Errorf("tls listen: %w", err)
		}
		loopbackListen, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", port))
		if err != nil {
			tlsListen.Close()
			return nil, fmt.Errorf("loopback listen: %w", err)
		}
		out.tlsListen = tlsListen
		out.loopback = loopbackListen
		out.httpSrv = newHTTPServer(handler, loopbackListen.Addr().String())
		out.tlsSrv = newHTTPServer(handler, tlsListen.Addr().String())
		return out, nil
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	out.loopback = ln
	out.httpSrv = newHTTPServer(handler, addr)
	return out, nil
}
