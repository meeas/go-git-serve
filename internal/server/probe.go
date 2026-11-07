package server

import (
	"context"
	"net"
	"net/http"
	"time"
)

// ProbeResult says what, if anything, is answering on an address.
type ProbeResult int

const (
	ProbeFree     ProbeResult = iota // nothing is listening
	ProbeSelf                        // this app (same name) is already running there
	ProbeOtherApp                    // a different go-git-serve app is using it
	ProbeForeign                     // some other program is using it
)

// probeTimeout bounds the whole probe; a local server answers in milliseconds.
const probeTimeout = 3 * time.Second

// Probe checks what is listening on the configured listen address. For
// ProbeOtherApp it also returns that app's name.
func Probe(ctx context.Context, listen, app string) (ProbeResult, string) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return ProbeFree, ""
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	addr := net.JoinHostPort(host, port)

	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return ProbeFree, ""
	}
	conn.Close()

	client := &http.Client{
		Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, "http://"+addr+"/", nil)
	if err != nil {
		return ProbeForeign, ""
	}
	resp, err := client.Do(req)
	if err != nil {
		return ProbeForeign, ""
	}
	resp.Body.Close()
	switch name := resp.Header.Get(AppHeader); {
	case name == "":
		return ProbeForeign, ""
	case name == app:
		return ProbeSelf, name
	default:
		return ProbeOtherApp, name
	}
}
