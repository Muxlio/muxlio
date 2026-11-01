package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
)

type serverProfile struct {
	URL   string `json:"url"`
	Token string `json:"token,omitempty"`
}

// resolveServer resolves a server identifier (which can be a profile name like "macmini",
// or an explicit http:// / https:// URL) to its actual URL and auth token.
// If serverArg is empty, it checks MUXLIO_SERVER, then config.json's "defaultServer",
// and falls back to "http://127.0.0.1:7600".
func resolveServer(serverArg, dataDir string) (serverURL string, token string, err error) {
	if serverArg == "" {
		serverArg = os.Getenv("MUXLIO_SERVER")
	}

	// 1. Direct URL provided
	if strings.HasPrefix(serverArg, "http://") || strings.HasPrefix(serverArg, "https://") {
		u, err := url.Parse(serverArg)
		if err != nil || u.Host == "" {
			return "", "", fmt.Errorf("invalid server URL %q", serverArg)
		}
		token := os.Getenv("MUXLIO_TOKEN")
		if token == "" && isLocalCoordinatorURL(u, dataDir) {
			token = readConfiguredAuthToken(authConfigPath(dataDir))
		}
		return strings.TrimRight(serverArg, "/"), token, nil
	}

	// 2. Look up in config.json
	cfgPath := authConfigPath(dataDir)
	if raw, readErr := os.ReadFile(cfgPath); readErr == nil {
		var cfg struct {
			DefaultServer string                     `json:"defaultServer"`
			Token         string                     `json:"token"`
			Servers       map[string]json.RawMessage `json:"servers"`
			Profiles      map[string]json.RawMessage `json:"profiles"`
		}
		if err := json.Unmarshal(raw, &cfg); err == nil {
			target := serverArg
			if target == "" {
				target = cfg.DefaultServer
			}
			lookup := func(m map[string]json.RawMessage) *serverProfile {
				if m == nil || target == "" {
					return nil
				}
				v, ok := m[target]
				if !ok {
					return nil
				}
				// Can be a string "http://..." or a profile object
				var s string
				if err := json.Unmarshal(v, &s); err == nil {
					return &serverProfile{URL: s}
				}
				var p serverProfile
				if err := json.Unmarshal(v, &p); err == nil {
					return &p
				}
				return nil
			}

			if p := lookup(cfg.Servers); p != nil {
				tok := p.Token
				if tok == "" {
					tok = cfg.Token
				}
				if tok == "" {
					tok = os.Getenv("MUXLIO_TOKEN")
				}
				return strings.TrimRight(p.URL, "/"), tok, nil
			}
			if p := lookup(cfg.Profiles); p != nil {
				tok := p.Token
				if tok == "" {
					tok = cfg.Token
				}
				if tok == "" {
					tok = os.Getenv("MUXLIO_TOKEN")
				}
				return strings.TrimRight(p.URL, "/"), tok, nil
			}
		}
	}

	// 3. If serverArg was empty, fallback to localhost default
	if serverArg == "" {
		token := os.Getenv("MUXLIO_TOKEN")
		if token == "" {
			if raw, readErr := os.ReadFile(cfgPath); readErr == nil {
				var cfg struct {
					Token string `json:"token"`
				}
				_ = json.Unmarshal(raw, &cfg)
				token = cfg.Token
			}
		}
		return "http://127.0.0.1:7600", token, nil
	}

	return "", "", fmt.Errorf("unknown server profile %q (not found in %s)", serverArg, cfgPath)
}

func isLocalCoordinatorURL(u *url.URL, dataDir string) bool {
	if u == nil || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	configPath := authConfigPath(dataDir)
	addr := "127.0.0.1:7600"
	if raw, err := os.ReadFile(configPath); err == nil {
		var config struct {
			Addr string `json:"addr"`
		}
		_ = json.Unmarshal(raw, &config)
		if config.Addr != "" {
			addr = config.Addr
		}
	}
	if u.Host == addr {
		return true
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	uHost, uPort, err := net.SplitHostPort(u.Host)
	if err != nil || uPort != port {
		return false
	}
	if host == "127.0.0.1" || strings.EqualFold(host, "localhost") {
		return uHost == "127.0.0.1" || strings.EqualFold(uHost, "localhost")
	}
	if listenHostIsWildcard(host) {
		return isThisMachineIP(uHost)
	}
	return uHost == host
}

func isThisMachineIP(host string) bool {
	if host == "127.0.0.1" || strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ifaceIP net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ifaceIP = v.IP
			case *net.IPAddr:
				ifaceIP = v.IP
			}
			if ifaceIP != nil && ifaceIP.Equal(ip) {
				return true
			}
		}
	}
	return false
}

// setAuthHeader applies the Bearer token to an HTTP request if present.
func setAuthHeader(req *http.Request, token string) {
	if token == "" {
		token = os.Getenv("MUXLIO_TOKEN")
	}
	token = strings.TrimSpace(token)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

// loadToken reads a token from a literal flag, a file path, or the environment.
func loadToken(flagToken, flagTokenFile string) string {
	if flagToken != "" {
		return strings.TrimSpace(flagToken)
	}
	if flagTokenFile != "" {
		b, err := os.ReadFile(flagTokenFile)
		if err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	return strings.TrimSpace(os.Getenv("MUXLIO_TOKEN"))
}

// loadWorkerToken reads the worker credential from flags, files, or config.
func loadWorkerToken(flagToken, flagTokenFile, dataDir string) string {
	if flagToken != "" {
		return strings.TrimSpace(flagToken)
	}
	if flagTokenFile != "" {
		b, err := os.ReadFile(flagTokenFile)
		if err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	if v := strings.TrimSpace(os.Getenv("MUXLIO_WORKER_TOKEN")); v != "" {
		return v
	}
	return readConfiguredWorkerToken(authConfigPath(dataDir))
}
