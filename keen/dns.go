package keen

import (
	"net"
	"os"
	"strings"
)

// SystemDNS возвращает upstream-DNS физической сети (без loopback). Эти серверы
// передаются TURN-резолверу, чтобы резолвить VK/TURN-хосты по WAN ещё до подъёма
// туннеля. Источник — RCI `show ip name-server` (серверы, полученные NDM от
// провайдера и заданные вручную); fallback — /opt/etc/resolv.conf Entware.
// Loopback пропускается — это сам DNS-прокси роутера.
func SystemDNS(rci *RCI) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(ip string) {
		parsed := net.ParseIP(ip)
		if parsed == nil || parsed.IsLoopback() {
			return
		}
		if !seen[ip] {
			seen[ip] = true
			out = append(out, ip)
		}
	}

	// RCI отдаёт вложенные объекты с полем "address"; формы ответа отличаются
	// между версиями (server/name-server, объект/массив) — обходим всё дерево.
	var raw any
	if err := rci.Get("show/ip/name-server", &raw); err == nil {
		for _, ip := range collectAddresses(raw) {
			add(ip)
		}
	}

	if len(out) == 0 {
		if data, err := os.ReadFile("/opt/etc/resolv.conf"); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				fields := strings.Fields(strings.TrimSpace(line))
				if len(fields) >= 2 && fields[0] == "nameserver" {
					add(fields[1])
				}
			}
		}
	}
	return out
}

// collectAddresses рекурсивно собирает значения полей "address" из ответа RCI.
func collectAddresses(v any) []string {
	var out []string
	switch t := v.(type) {
	case map[string]any:
		if a, ok := t["address"].(string); ok && a != "" {
			out = append(out, a)
		}
		for _, child := range t {
			out = append(out, collectAddresses(child)...)
		}
	case []any:
		for _, child := range t {
			out = append(out, collectAddresses(child)...)
		}
	}
	return out
}
