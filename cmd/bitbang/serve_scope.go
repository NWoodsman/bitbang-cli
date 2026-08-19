package main

import (
	"os"

	"github.com/richlegrand/bitbang/internal/fileshare"
	"github.com/richlegrand/bitbang/internal/identity"
	"github.com/richlegrand/bitbang/internal/links"
	"github.com/richlegrand/bitbang/internal/streamtype"
)

// offeredScopes lists the scope names this listener supports, which is
// what an absent scope on a link resolves to and what a requested one is
// intersected with.
//
// forward rides with shell: the TCP handler is only built in shell-
// bearing modes, so `serve shell` can mint a forward-only link but
// `serve files` cannot.
func offeredScopes(cfg serveConfig) []string {
	var out []string
	if cfg.filesEnabled {
		out = append(out, links.ScopeFiles)
	}
	if cfg.shellEnabled {
		out = append(out, links.ScopeShell, links.ScopeForward)
	}
	if cfg.proxyEnabled {
		out = append(out, links.ScopeProxy)
	}
	return out
}

// sessionHandlers is the stream-handler set for one peer, plus the two
// handlers whose resources outlive a stream and must be closed when the
// connection goes.
type sessionHandlers struct {
	all   []streamtype.StreamHandler
	shell *streamtype.ShellHandler
	tcp   *streamtype.TCPHandler
}

// buildHandlers assembles the handler set a single peer gets, given the
// scopes its link grants.
//
// Building the set rather than filtering a complete one is what makes
// scope enforcement free: sendReady derives advertised caps from the
// registered handlers, so a files-scoped link advertises caps ["file"]
// with no extra code; `bitbang connect` already fails with a legible
// "listener does not advertise the 'shell' capability"; and OnConnect
// never runs for a handler that was never built, which matters because
// the HTTP proxy's OnConnect resolves and probes its target.
//
// The listener's own browser UI is the one thing no scope gates. It is
// the shell the other scopes act through -- a files-only link still has
// to render a file browser -- so it is built for every link, showing
// only the caps that link actually grants.
func buildHandlers(cfg serveConfig, granted map[string]bool, share *fileshare.FileShare,
	shellArgv []string, id *identity.Identity, browserIP string) sessionHandlers {

	var out sessionHandlers

	if share != nil && granted[links.ScopeFiles] {
		out.all = append(out.all, streamtype.NewFile(share, cfg.verbose))
	}
	if cfg.shellEnabled && granted[links.ScopeShell] {
		sh := streamtype.NewShell(shellArgv, cfg.verbose)
		sh.MaxConcurrent = cfg.shellMaxSessions
		if cfg.shellMirror {
			sh.StdoutMirror = os.Stdout
			sh.StderrMirror = os.Stderr
		}
		out.shell = sh
		out.all = append(out.all, sh)
	}
	if cfg.shellEnabled && granted[links.ScopeForward] {
		out.tcp = streamtype.NewTCP(cfg.verbose)
		out.all = append(out.all, out.tcp)
	}

	// Fixed-target proxy-only mode (e.g. the OctoPrint plugin): every
	// request goes straight to --target, so the plain device URL serves
	// the app directly -- no dispatcher, no landing page. Here `http` IS
	// the proxy, so the proxy scope gates it outright.
	if cfg.proxyEnabled && cfg.target != "" && !cfg.shellEnabled && !cfg.filesEnabled {
		if granted[links.ScopeProxy] {
			// Only forward the client IP when explicitly enabled (the
			// backend trusts localhost for auth); otherwise withhold it so
			// requests look local and don't trip an external-access warning.
			xffIP := ""
			if cfg.forwardClientIP {
				xffIP = browserIP
			}
			httpProxy := streamtype.NewHTTPProxy(cfg.target, id.UID, cfg.server, xffIP, cfg.verbose)
			// Pair a WebSocket handler so ws:// streams resolve to the same
			// target as HTTP (otherwise: "no handler for stream type websocket").
			out.all = append(out.all, httpProxy,
				streamtype.NewWebSocket(httpProxy, xffIP, cfg.verbose))
		}
		return out
	}

	// Dispatcher mode. The local branch renders the UI for whatever this
	// link grants; the proxy branch is handed over only when the link
	// grants proxy, and a nil proxy is already how the dispatcher is told
	// there is none.
	front := buildServeHTTPHandler(
		scopedShare(share, granted),
		cfg.shellEnabled && granted[links.ScopeShell],
		cfg.proxyEnabled && granted[links.ScopeProxy],
		cfg.shellMaxSessions,
		grantedCapCount(cfg, granted) > 1,
	)
	localHTTP := streamtype.NewHTTPLocal(front, cfg.verbose)

	var proxyHTTP streamtype.StreamHandler
	if cfg.proxyEnabled && granted[links.ScopeProxy] {
		// Dynamic-target mode: withhold browser_ip so we DON'T inject
		// XFF. This mode proxies arbitrary LAN apps that may rely on
		// requests appearing local; silently forwarding the real IP
		// could break their access control. (Fixed-target mode above
		// passes it -- there the backend is known.)
		p := streamtype.NewHTTPProxy("", id.UID, cfg.server, "", cfg.verbose)
		proxyHTTP = p
		out.all = append(out.all, streamtype.NewWebSocket(p, "", cfg.verbose))
	}
	out.all = append(out.all, newHTTPDispatcher(localHTTP, proxyHTTP))
	return out
}

// scopedShare hides the file share from a link that was not granted
// files, so its browser UI has no file browser to offer.
func scopedShare(share *fileshare.FileShare, granted map[string]bool) *fileshare.FileShare {
	if granted[links.ScopeFiles] {
		return share
	}
	return nil
}

// grantedCapCount counts the browser-visible caps this link reaches.
// One means the UI can serve that cap directly, so relative URLs in its
// HTML resolve cleanly; more than one needs the launcher and its cap bar.
func grantedCapCount(cfg serveConfig, granted map[string]bool) int {
	n := 0
	if cfg.filesEnabled && granted[links.ScopeFiles] {
		n++
	}
	if cfg.shellEnabled && granted[links.ScopeShell] {
		n++
	}
	if cfg.proxyEnabled && granted[links.ScopeProxy] {
		n++
	}
	return n
}
