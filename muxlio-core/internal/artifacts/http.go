package artifacts

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// containedHTTPClient preserves caller configuration without letting redirects
// disclose artifact credentials or turn an upload into a successful GET.
func containedHTTPClient(client *http.Client) *http.Client {
	clone := *client
	clone.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) == 0 {
			return fmt.Errorf("muxlio: redirect without an original request")
		}
		if via[0].Method != http.MethodGet || !sameOrigin(req.URL, via[0].URL) {
			return fmt.Errorf("muxlio: artifact redirect changes origin or upload operation")
		}
		if client.CheckRedirect != nil {
			return client.CheckRedirect(req, via)
		}
		if len(via) >= 10 {
			return fmt.Errorf("muxlio: stopped after 10 redirects")
		}
		return nil
	}
	return &clone
}

func sameOrigin(a, b *url.URL) bool {
	port := func(u *url.URL) string {
		if p := u.Port(); p != "" {
			return p
		}
		if strings.EqualFold(u.Scheme, "https") {
			return "443"
		}
		if strings.EqualFold(u.Scheme, "http") {
			return "80"
		}
		return ""
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}
