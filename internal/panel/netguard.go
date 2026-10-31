package panel

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Target struct {
	Scheme  string
	Host    string
	Port    int
	Path    string
	Trusted bool
}

func (t Target) Display() string {
	host := t.Host
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("%s://%s:%d%s", t.Scheme, host, t.Port, t.Path)
}

func mustPrefixes(cidrs ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(cidrs))
	for i, c := range cidrs {
		out[i] = netip.MustParsePrefix(c)
	}
	return out
}

var (
	metadataNets  = mustPrefixes("169.254.0.0/16", "fd00:ec2::/32", "0.0.0.0/8")
	alwaysBlocked = mustPrefixes("0.0.0.0/8", "100.64.0.0/10", "169.254.0.0/16", "192.0.0.0/24", "192.0.2.0/24",
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
		"2001:db8::/32", "fd00:ec2::/32", "fe80::/10", "ff00::/8")
	metadataHosts = map[string]bool{"metadata.google.internal": true, "metadata": true, "instance-data": true, "metadata.azure.internal": true}
	labelRe       = func(s string) bool {
		if s == "" || len(s) > 63 {
			return false
		}
		for _, c := range s {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return false
			}
		}
		return true
	}
)

func inAny(ip netip.Addr, nets []netip.Prefix) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func portAllowed(p int) bool {
	if cfg.DeniedPorts[p] {
		return false
	}
	for _, r := range cfg.AllowedPorts {
		if p >= r[0] && p <= r[1] {
			return true
		}
	}
	return false
}

func parseTarget(raw string, trusted bool) Target {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 300 {
		fail(400, "invalid_url", "请输入有效的 ASF 地址，例如 https://asf.example.com:1242")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		fail(400, "invalid_url", "ASF 地址格式不正确。")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		fail(400, "invalid_url", "ASF 地址只支持 http 或 https。")
	}
	if u.User != nil {
		fail(400, "invalid_url", "不要在地址里填写用户名或密码，IPC 密码请填在单独的输入框。")
	}
	if u.RawQuery != "" || u.Fragment != "" || u.ForceQuery || strings.Contains(raw, "#") {
		fail(400, "invalid_url", "ASF 地址不能包含 ? 或 # 之后的内容。")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		fail(400, "invalid_url", "ASF 地址缺少主机名。")
	}
	if _, err := netip.ParseAddr(host); err != nil {
		for _, c := range host {
			if c > 127 {
				fail(400, "invalid_url", "主机名包含非英文字符，请使用 punycode 形式（xn--…）。")
			}
		}
		for _, label := range strings.Split(host, ".") {
			if !labelRe(label) {
				fail(400, "invalid_url", "主机名格式不正确。")
			}
		}
		if metadataHosts[host] || strings.HasSuffix(host, ".internal") {
			fail(400, "blocked_address", "不允许连接到这个地址。")
		}
		if !trusted && (!strings.Contains(host, ".") || host == "localhost" || strings.HasSuffix(host, ".localhost")) {
			fail(400, "blocked_address", "请填写完整域名或公网 IP，不允许连接本机地址。")
		}
	}
	port := 443
	if scheme == "http" {
		port = 80
	}
	if ps := u.Port(); ps != "" {
		n, err := strconv.Atoi(ps)
		if err != nil || n < 1 || n > 65535 {
			fail(400, "invalid_url", "ASF 地址格式不正确。")
		}
		port = n
	}
	if !portAllowed(port) {
		fail(400, "blocked_port", fmt.Sprintf("端口 %d 不在允许范围内。", port), "请联系网站管理员确认允许的端口。")
	}
	path := strings.TrimRight(u.EscapedPath(), "/")
	if path != "" {
		for _, seg := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
			if seg == "" || seg == "." || seg == ".." {
				fail(400, "invalid_url", "地址路径格式不正确。")
			}
		}
	}
	if len(path) > 100 {
		fail(400, "invalid_url", "地址路径过长。")
	}
	return Target{Scheme: scheme, Host: host, Port: port, Path: path, Trusted: trusted}
}

func isGlobal(ip netip.Addr) bool {
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !inAny(ip, alwaysBlocked)
}

func ipAllowed(ip netip.Addr, trusted bool) (bool, bool) {
	ip = ip.Unmap()
	if inAny(ip, metadataNets) {
		return false, false
	}
	if trusted && !isGlobal(ip) && !ip.IsMulticast() && !ip.IsUnspecified() {
		return true, true
	}
	if inAny(ip, cfg.AllowPrivateCIDRs) {
		return true, true
	}
	return isGlobal(ip), false
}

func resolveTarget(ctx context.Context, t Target) string {
	var addrs []netip.Addr
	if ip, err := netip.ParseAddr(t.Host); err == nil {
		addrs = []netip.Addr{ip}
	} else {
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", t.Host)
		if err != nil || len(ips) == 0 {
			fail(502, "dns_failed", "无法解析 ASF 地址的域名。", "请检查域名是否正确。")
		}
		addrs = ips
	}
	allPrivate := true
	for _, ip := range addrs {
		ok, private := ipAllowed(ip, t.Trusted)
		if !ok {
			fail(400, "blocked_address", "这个地址指向内网、本机或保留地址，出于安全原因不允许连接。",
				"请使用可从公网访问的地址；如需连接内网 ASF，请联系网站管理员。")
		}
		allPrivate = allPrivate && private
	}
	if t.Scheme == "http" && cfg.RequireHTTPS && !allPrivate {
		fail(400, "https_required", "公网连接必须使用 https，否则 IPC 密码会以明文在网络上传输。",
			"请为 ASF 配置 HTTPS（例如放在反向代理之后）。")
	}
	return addrs[0].Unmap().String()
}

func pinnedClient(t Target, ip string, timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: timeout}
	transport := &http.Transport{
		Proxy: nil,
		// dial the ip we already checked, not whatever dns says now
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(t.Port)))
		},
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, ServerName: t.Host},
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		DisableKeepAlives:     true,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout * 2,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
