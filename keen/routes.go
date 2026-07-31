package keen

import (
	"fmt"
	"log"
	"net"
	"sync"
)

// routes.go — анти-петля: host-маршруты /32 до серверов, с которыми говорит
// сам прокси (TURN, VK auth, DNS), через физический WAN. Без них с поднятым
// `ip global auto` на WG-подключении собственный трафик прокси ушёл бы в
// туннель, который сам же и несёт (аналог host-маршрутов Windows-порта;
// SO_MARK/fwmark на Keenetic не используется).
//
// Маршруты добавляются parse-командами без `system configuration save`, но
// Save() провижининга WG может захватить их в стартовую конфигурацию. Это
// безвредно: лишний /32 через WAN просто направляет один IP мимо туннеля,
// а демон после рестарта переустанавливает актуальный набор.

// RouteManager управляет набором bypass-маршрутов на время жизни туннеля.
type RouteManager struct {
	rci *RCI

	mu     sync.Mutex
	wanID  string          // id WAN-интерфейса, через который добавлены маршруты
	routes map[string]bool // ip → добавлен
}

// NewRouteManager определяет текущий WAN-интерфейс.
func NewRouteManager(rci *RCI) (*RouteManager, error) {
	wan, err := rci.WANInterface()
	if err != nil {
		return nil, err
	}
	log.Printf("[ROUTE] WAN interface: %s (%s)", wan.ID, wan.Description)
	return &RouteManager{rci: rci, wanID: wan.ID, routes: make(map[string]bool)}, nil
}

// WAN возвращает id WAN-интерфейса, зафиксированный при создании/RefreshWAN.
func (r *RouteManager) WAN() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.wanID
}

// AddBypassRoutes добавляет host-маршруты через физический WAN. Ошибки отдельных
// маршрутов логируются, но не прерывают остальные (как в Windows-порте).
func (r *RouteManager) AddBypassRoutes(ips []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			log.Printf("[ROUTE] skipping non-IP: %s", ipStr)
			continue
		}
		if ip.To4() == nil {
			// NDM `ip route` — IPv4; IPv6-адресов TURN/VK на практике нет.
			log.Printf("[ROUTE] skipping IPv6: %s", ipStr)
			continue
		}
		if r.routes[ipStr] {
			continue
		}
		if err := r.rci.Parse(fmt.Sprintf("ip route %s/32 %s auto", ipStr, r.wanID)); err != nil {
			log.Printf("[ROUTE] warn: add route %s via %s: %v", ipStr, r.wanID, err)
			continue
		}
		r.routes[ipStr] = true
		log.Printf("[ROUTE] added bypass: %s/32 via %s", ipStr, r.wanID)
	}
}

// Cleanup снимает все добавленные маршруты.
func (r *RouteManager) Cleanup() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removeAllLocked()
}

func (r *RouteManager) removeAllLocked() {
	for ipStr := range r.routes {
		if err := r.rci.Parse(fmt.Sprintf("no ip route %s/32 %s", ipStr, r.wanID)); err != nil {
			log.Printf("[ROUTE] warn: remove route %s: %v", ipStr, err)
		} else {
			log.Printf("[ROUTE] removed bypass: %s/32", ipStr)
		}
		delete(r.routes, ipStr)
	}
}

// RefreshWAN заново определяет WAN-интерфейс. При его смене (перетык кабеля,
// LTE-резерв) старые маршруты снимаются и переустанавливаются через новый WAN.
// Возвращает true, если WAN сменился.
func (r *RouteManager) RefreshWAN() bool {
	wan, err := r.rci.WANInterface()
	if err != nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if wan.ID == r.wanID {
		return false
	}
	log.Printf("[ROUTE] WAN changed: %s -> %s", r.wanID, wan.ID)
	prev := make([]string, 0, len(r.routes))
	for ip := range r.routes {
		prev = append(prev, ip)
	}
	r.removeAllLocked()
	r.wanID = wan.ID
	for _, ipStr := range prev {
		if err := r.rci.Parse(fmt.Sprintf("ip route %s/32 %s auto", ipStr, r.wanID)); err != nil {
			log.Printf("[ROUTE] warn: re-add route %s via %s: %v", ipStr, r.wanID, err)
			continue
		}
		r.routes[ipStr] = true
	}
	return true
}
