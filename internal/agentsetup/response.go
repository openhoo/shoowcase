package agentsetup

import (
	"errors"
	"fmt"
	"net/http"
)

// Only the numeric status comes from the peer. Response bodies and redirect
// locations can contain setup codes, tokens, or an intermediary's login page.
func httpFailure(status int, setup bool) error {
	var advice string
	switch {
	case status == http.StatusUnauthorized && setup:
		advice = "the setup code was not accepted; copy a new command from Quickstart"
	case status == http.StatusUnauthorized:
		advice = "the agent key was not accepted; reconnect this agent in Quickstart"
	case status == http.StatusForbidden:
		advice = "access denied; check your workspace permissions and any access policy on the control host"
	case status == http.StatusProxyAuthRequired:
		advice = "proxy authentication required; check the proxy credentials on this machine"
	case (status >= 300 && status < 400) || status == http.StatusNotFound || status == http.StatusMisdirectedRequest:
		advice = "check that the control URL is the one shown in Quickstart and ends in /mcp"
	case status == http.StatusTooManyRequests:
		advice = "too many requests; wait before retrying"
	case status >= 500 && status < 600:
		advice = "service temporarily unavailable; retry when the control host is ready"
	case status == http.StatusRequestEntityTooLarge:
		advice = "request too large; reduce its size before retrying"
	default:
		advice = "request rejected; check the request and control URL"
	}
	return fmt.Errorf("Shoowcase HTTP %d: %s", status, advice)
}

var errUnreachable = errors.New("could not reach Shoowcase; check connectivity, proxy settings and certificate trust")
