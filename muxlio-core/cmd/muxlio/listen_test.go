package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTestCertKey(t *testing.T, dir string) (certPath, keyPath string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "muxlio-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPath = filepath.Join(dir, "test.crt")
	keyPath = filepath.Join(dir, "test.key")
	certOut, err := os.Create(certPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		certOut.Close()
		t.Fatal(err)
	}
	certOut.Close()
	keyOut, err := os.Create(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := pem.Encode(keyOut, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}); err != nil {
		keyOut.Close()
		t.Fatal(err)
	}
	keyOut.Close()
	return certPath, keyPath
}

func pickNonLoopbackIPv4(t *testing.T) string {
	t.Helper()
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil || ipnet.IP.IsLoopback() {
				continue
			}
			return ipnet.IP.String()
		}
	}
	t.Skip("no non-loopback IPv4 address available for TLS listen test")
	return ""
}

func TestValidateListenConfigRejectsNonLoopbackWithoutTLS(t *testing.T) {
	if err := validateListenConfig("192.0.2.1:7600", "", ""); err == nil {
		t.Fatal("expected error for non-loopback without TLS")
	}
	if err := validateListenConfig("0.0.0.0:7600", "a.crt", "a.key"); err == nil {
		t.Fatal("expected error for wildcard addr with TLS files")
	}
	if err := validateListenConfig("0.0.0.0:7600", "", ""); err != nil {
		t.Fatalf("wildcard plain HTTP: %v", err)
	}
	if err := validateListenConfig("127.0.0.1:7600", "a.crt", "a.key"); err == nil {
		t.Fatal("expected error for loopback with TLS files")
	}
	if err := validateListenConfig("127.0.0.1:7600", "", ""); err != nil {
		t.Fatalf("loopback without TLS: %v", err)
	}
}

func TestCoordinatorTLSListenHandshakeAndPlainHTTPRejected(t *testing.T) {
	host := pickNonLoopbackIPv4(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(ln.Addr().String())
	ln.Close()
	if err != nil {
		t.Fatal(err)
	}
	tlsAddr := net.JoinHostPort(host, port)

	dir := t.TempDir()
	certPath, keyPath := writeTestCertKey(t, dir)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	servers, err := startCoordinatorServers(handler, tlsAddr, certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		servers.shutdown(ctx)
	})
	servers.start()

	deadline := time.Now().Add(3 * time.Second)
	var tlsConn *tls.Conn
	for time.Now().Before(deadline) {
		tlsConn, err = tls.Dial("tcp", tlsAddr, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12})
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("tls dial: %v", err)
	}
	if err := tlsConn.Handshake(); err != nil {
		t.Fatalf("tls handshake: %v", err)
	}
	tlsConn.Close()

	plain, err := net.DialTimeout("tcp", tlsAddr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	_ = plain.SetDeadline(time.Now().Add(time.Second))
	if _, err := plain.Write([]byte("GET /healthz HTTP/1.1\r\nHost: test\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 256)
	n, err := plain.Read(buf)
	if err != nil && n == 0 {
		t.Fatalf("expected TLS port to reject plain HTTP cleanly, read err=%v", err)
	}
	if n > 0 && strings.Contains(string(buf[:n]), "200") {
		t.Fatalf("plain HTTP on TLS port returned success: %q", buf[:n])
	}

	loopback := "http://" + servers.loopbackAddr() + "/healthz"
	resp, err := http.Get(loopback)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("loopback http status = %d", resp.StatusCode)
	}
}
