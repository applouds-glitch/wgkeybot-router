package owrt

import (
	"net"
	"os"
	"strings"
)

// resolvPaths — источники upstream-DNS в порядке приоритета. На OpenWrt
// /etc/resolv.conf обычно указывает на dnsmasq (127.0.0.1), а реальные DNS,
// полученные от провайдера по DHCP/PPP, лежат в resolv.conf.auto.
var resolvPaths = []string{
	"/tmp/resolv.conf.d/resolv.conf.auto",
	"/tmp/resolv.conf.auto",
	"/etc/resolv.conf",
}

// SystemDNS возвращает upstream-DNS физической сети (без loopback). Эти серверы
// передаются TURN-резолверу, чтобы резолвить VK/TURN-хосты по WAN ещё до подъёма
// туннеля. Loopback (dnsmasq) пропускается — он сам ходит наружу через нас.
func SystemDNS() []string {
	seen := make(map[string]bool)
	var out []string
	for _, p := range resolvPaths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "nameserver") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			ip := fields[1]
			parsed := net.ParseIP(ip)
			if parsed == nil || parsed.IsLoopback() {
				continue
			}
			if !seen[ip] {
				seen[ip] = true
				out = append(out, ip)
			}
		}
	}
	return out
}
