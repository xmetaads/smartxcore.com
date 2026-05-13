//go:build windows

package main

import (
	"os"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// detectProxy returns a proxy URL (e.g. "http://10.0.0.1:8080") the
// bootstrapper's HTTP client should use for outbound requests, or
// "" if no proxy is configured / detected.
//
// Why bother: corporate networks in many of our target customers
// require all HTTPS to egress through a forward proxy. Without
// proxy detection, the bootstrapper hangs on api.smveo.com and
// the user gets a generic "could not check for updates" error
// they can't fix.
//
// Detection sources, in priority order:
//
//   1. HTTPS_PROXY / HTTP_PROXY environment variables. Standard
//      Unix-style override; respects what the user / sysadmin
//      configured explicitly.
//
//   2. Windows registry: HKCU\Software\Microsoft\Windows\
//      CurrentVersion\Internet Settings -> ProxyEnable + ProxyServer.
//      This is what Internet Explorer / Edge legacy / WinHTTP read.
//      Most "Settings -> Network -> Proxy" panels write here too.
//
// We deliberately do NOT call WinHttpGetIEProxyConfigForCurrentUser
// or WinHttpGetProxyForUrl — those pull in WinHttp.dll and produce
// a much larger binary, and the registry value covers the vast
// majority of real-world deployments. If we ever need PAC-script
// support, that's the next step.
func detectProxy() string {
	if v := strings.TrimSpace(os.Getenv("HTTPS_PROXY")); v != "" {
		return normaliseProxyURL(v)
	}
	if v := strings.TrimSpace(os.Getenv("HTTP_PROXY")); v != "" {
		return normaliseProxyURL(v)
	}
	return detectIEProxy()
}

// detectIEProxy reads HKCU's Internet Settings to extract a proxy
// URL. Returns "" if no proxy is enabled.
func detectIEProxy() string {
	k, err := registry.OpenKey(
		registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`,
		registry.QUERY_VALUE,
	)
	if err != nil {
		return ""
	}
	defer k.Close()

	enabled, _, err := k.GetIntegerValue("ProxyEnable")
	if err != nil || enabled == 0 {
		return ""
	}
	server, _, err := k.GetStringValue("ProxyServer")
	if err != nil || server == "" {
		return ""
	}

	// ProxyServer may be "host:port" or
	// "http=host:port;https=host:port;...". Pick the HTTPS entry
	// if present since we only make HTTPS requests; fall back to
	// the generic http= entry; fall back to the whole string.
	if strings.Contains(server, "=") {
		for _, part := range strings.Split(server, ";") {
			if strings.HasPrefix(part, "https=") {
				return normaliseProxyURL(strings.TrimPrefix(part, "https="))
			}
		}
		for _, part := range strings.Split(server, ";") {
			if strings.HasPrefix(part, "http=") {
				return normaliseProxyURL(strings.TrimPrefix(part, "http="))
			}
		}
		return ""
	}
	return normaliseProxyURL(server)
}

// normaliseProxyURL prepends "http://" if the user wrote a bare
// "host:port" form in the registry / env var. Most users do.
func normaliseProxyURL(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	return s
}
