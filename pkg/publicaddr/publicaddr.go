// Package publicaddr works out the address other machines can reach this host
// on, for a caller that has no request to read one from.
//
// `init` is that caller: it prints a link for a client on another machine, and
// the only address it knows for certain is the one that link must not carry.
package publicaddr

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// DefaultTimeout bounds one probe, all endpoints together.
const DefaultTimeout = 3 * time.Second

// maxBody bounds a hostile response. Cloudflare's trace body runs to a few
// hundred bytes, so anything much smaller would cut it off before its ip= line.
const maxBody = 4096

// DefaultURLs are echo services that answer with the caller's address: two
// that return it bare, and Cloudflare's trace, which returns key=value lines.
var DefaultURLs = []string{
	"https://api.ipify.org",
	"https://ipv4.icanhazip.com",
	"https://www.cloudflare.com/cdn-cgi/trace",
}

// cgnat is RFC 6598 shared address space: what a carrier-grade NAT hands out.
// netip's IsPrivate does not cover it, and it is no more reachable from outside.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// Kind says how much a Choice can be trusted.
type Kind int

const (
	// None means no usable address was found.
	None Kind = iota

	// Verified is a public address an echo service reported and this host owns.
	Verified

	// BehindNAT is a public address an echo service reported that is not on any
	// local interface, so something in front has to forward the port.
	BehindNAT

	// Unverified is a public address on a local interface that no echo service
	// confirmed.
	Unverified

	// LANOnly is a private address: reachable from the same network and no
	// further.
	LANOnly
)

// Choice is the address picked and why.
type Choice struct {
	Kind Kind

	// Host is the address to hand out. The zero Addr when Kind is None.
	Host netip.Addr

	// Port is the port to hand out, or 0 for the scheme's default.
	Port uint16

	// LAN is this host's own address when Kind is BehindNAT, for the
	// port-forward the operator has to set up. The zero Addr when unknown.
	LAN netip.Addr

	// Proxied is set when the listener is bound to loopback. Something on this
	// host must be relaying to it, so Port is left at the default rather than
	// naming a port nobody outside can reach.
	Proxied bool
}

// BaseURL is the http base for the choice, or "" when there is none.
//
// Always http: eMuleQt fetches chunks over a plain socket with no TLS.
func (c Choice) BaseURL() string {
	if c.Kind == None {
		return ""
	}

	return "http://" + HostPort(c.Host, c.Port)
}

// HostPort joins an address and a port, leaving out port 0 and port 80.
func HostPort(host netip.Addr, port uint16) string {
	if port == 0 || port == 80 {
		if host.Is6() {
			return "[" + host.String() + "]"
		}

		return host.String()
	}

	return netip.AddrPortFrom(host, port).String()
}

// IsPublic reports whether an address is routable from the internet at large.
func IsPublic(addr netip.Addr) bool {
	addr = addr.Unmap()

	if !addr.IsValid() || !addr.IsGlobalUnicast() {
		return false
	}

	return !addr.IsPrivate() && !cgnat.Contains(addr)
}

// Local lists this host's IPv4 interface addresses that another machine could
// use at all: no loopback, no link-local.
func Local() []netip.Addr {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}

	var out []netip.Addr
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}

		addr, ok := netip.AddrFromSlice(ipNet.IP)
		if !ok {
			continue
		}

		addr = addr.Unmap()
		if addr.Is4() && addr.IsGlobalUnicast() {
			out = append(out, addr)
		}
	}

	return out
}

// Probe asks the echo services for this host's public IPv4 address and returns
// the first acceptable answer, with the URL that gave it.
//
// Concurrent and first-answer-wins: one slow endpoint must not hold up the
// rest, and every one has to fail for failure to be the answer. The connection
// is forced to IPv4 so a dual-stack endpoint reports the family asked about.
func Probe(ctx context.Context, urls []string, timeout time.Duration) (netip.Addr, string, error) {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	var targets []string
	for _, raw := range urls {
		if trimmed := strings.TrimSpace(raw); trimmed != "" {
			targets = append(targets, trimmed)
		}
	}
	if len(targets) == 0 {
		return netip.Addr{}, "", fmt.Errorf("no echo service to ask")
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client := newClient(timeout)
	defer client.CloseIdleConnections()

	type answer struct {
		addr netip.Addr
		url  string
		err  error
	}

	answers := make(chan answer, len(targets))
	for _, target := range targets {
		go func() {
			addr, err := fetch(ctx, client, target)
			answers <- answer{addr: addr, url: target, err: err}
		}()
	}

	var last error
	for range targets {
		got := <-answers
		if got.err == nil {
			return got.addr, got.url, nil
		}
		last = fmt.Errorf("%s: %w", got.url, got.err)
	}

	return netip.Addr{}, "", fmt.Errorf("no echo service answered with a public address (last: %v)", last)
}

// Choose picks the address to hand out from what the probe said (the zero Addr
// if it failed), the local interface addresses, and the listen address.
func Choose(probed netip.Addr, locals []netip.Addr, listenAddr string) Choice {
	bound, port := splitListen(listenAddr)

	choice := Choice{Port: port}
	if bound.IsValid() && bound.IsLoopback() {
		choice.Proxied = true
		choice.Port = 0
	}

	// A listener bound to one address answers on that address and no other, so
	// the interfaces it is not bound to are not candidates.
	if bound.IsValid() && !bound.IsLoopback() && !bound.IsUnspecified() {
		locals = []netip.Addr{bound}
	}

	lan, public := netip.Addr{}, netip.Addr{}
	for _, addr := range locals {
		switch {
		case IsPublic(addr) && !public.IsValid():
			public = addr
		case !IsPublic(addr) && !lan.IsValid():
			lan = addr
		}
	}

	switch {
	case probed.IsValid() && contains(locals, probed):
		choice.Kind, choice.Host = Verified, probed
	case probed.IsValid():
		choice.Kind, choice.Host, choice.LAN = BehindNAT, probed, lan
	case public.IsValid():
		choice.Kind, choice.Host = Unverified, public
	case lan.IsValid():
		choice.Kind, choice.Host = LANOnly, lan
	default:
		return Choice{}
	}

	return choice
}

// -- internals ---------------------------------------------------------------

// newClient forces IPv4 for every dial.
func newClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: timeout}

	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				if network == "tcp" {
					network = "tcp4"
				}

				return dialer.DialContext(ctx, network, addr)
			},
		},
	}
}

// fetch asks one echo service.
func fetch(ctx context.Context, client *http.Client, url string) (netip.Addr, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return netip.Addr{}, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return netip.Addr{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return netip.Addr{}, fmt.Errorf("status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return netip.Addr{}, err
	}

	value := parseEcho(string(body))

	addr, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("not an address: %.40q", value)
	}

	addr = addr.Unmap()
	if !addr.Is4() || !IsPublic(addr) {
		return netip.Addr{}, fmt.Errorf("not a public IPv4 address: %s", addr)
	}

	return addr, nil
}

// parseEcho extracts the address from an echo-service body: the ip= line of a
// Cloudflare trace, or else the whole body.
func parseEcho(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "ip="); ok {
			return strings.TrimSpace(rest)
		}
	}

	return strings.Trim(strings.TrimSpace(body), `"`)
}

// splitListen reads a listen address like ":8080" or "127.0.0.1:8080". The
// host is the zero Addr when it is empty or not an IP.
func splitListen(listenAddr string) (netip.Addr, uint16) {
	host, rawPort, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return netip.Addr{}, 0
	}

	var port uint16
	if n, err := net.LookupPort("tcp", rawPort); err == nil {
		port = uint16(n)
	}

	if strings.EqualFold(host, "localhost") {
		return netip.AddrFrom4([4]byte{127, 0, 0, 1}), port
	}

	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, port
	}

	return addr.Unmap(), port
}

func contains(addrs []netip.Addr, want netip.Addr) bool {
	for _, addr := range addrs {
		if addr == want {
			return true
		}
	}

	return false
}
