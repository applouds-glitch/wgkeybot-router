package keen

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/wgkeybot/router/core"
	"github.com/wgkeybot/router/pkg/proxy"
)

// backend.go — core.Backend поверх встроенного WireGuard-клиента прошивки.
//
// wireguard-go в процессе не поднимается: демон только настраивает ядерный WG
// через RCI, а endpoint его пира указывает на локальный TURN-прокси. Поэтому
// AttachWireGuard* не вызывается, UAPI никому не отдаётся, а статистика
// читается из `show interface`, а не из IpcGet.
type Backend struct {
	rci      *RCI
	cfg      *core.TunnelConfig
	settings core.Settings

	// remembered — WireguardN из state.json (может быть пустым).
	remembered string

	mu       sync.Mutex
	routeMgr *RouteManager
	wgIface  string
	up       bool
}

// Prepare определяет WAN и ставит обход до TURN-IP. Выполняется до старта
// прокси: при реконнекте WG-подключение NDM может быть ещё поднято с
// `ip global auto`, и без маршрута трафик прокси зациклился бы в туннель.
func (b *Backend) Prepare(turnIP string) error {
	rm, err := NewRouteManager(b.rci)
	if err != nil {
		return fmt.Errorf("route manager: %w", err)
	}
	b.mu.Lock()
	b.routeMgr = rm
	b.mu.Unlock()

	if turnIP != "" {
		rm.AddBypassRoutes([]string{turnIP})
	}
	return nil
}

// Bypass добавляет host-маршруты /32 через WAN для адресов, с которыми говорит
// сам прокси (динамические TURN-серверы, auth-хосты VK/OK).
func (b *Backend) Bypass(ips []string) {
	b.mu.Lock()
	rm := b.routeMgr
	b.mu.Unlock()
	if rm != nil {
		rm.AddBypassRoutes(ips)
	}
}

// Up провижинит WireguardN: выбирает интерфейс и прописывает пира с endpoint
// на локальном прокси.
func (b *Backend) Up(_ *proxy.Tunnel, listenAddr string) error {
	iface, err := ChooseWgIface(b.rci, b.settings.WgIface, b.remembered)
	if err != nil {
		return fmt.Errorf("choose wg interface: %w", err)
	}
	log.Printf("[keen] provisioning %s (endpoint %s)...", iface, listenAddr)
	if err := ProvisionWg(b.rci, iface, b.cfg, listenAddr, b.settings.Keepalive); err != nil {
		return err
	}

	b.mu.Lock()
	b.wgIface = iface
	b.up = true
	b.mu.Unlock()
	return nil
}

// Down опускает интерфейс и снимает маршруты. Само подключение в NDM не
// удаляется — остаётся видимым выключенным в веб-интерфейсе Keenetic.
func (b *Backend) Down() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.up && b.wgIface != "" {
		if err := WgDown(b.rci, b.wgIface); err != nil {
			log.Printf("[keen] warn: %s down: %v", b.wgIface, err)
		}
		b.up = false
	}
	if b.routeMgr != nil {
		b.routeMgr.Cleanup()
		b.routeMgr = nil
	}
}

// Stats читает счётчики из RCI `show interface <wg>` — вместо UAPI-вывода
// wireguard-go, которого здесь нет.
func (b *Backend) Stats() core.TunnelStats {
	b.mu.Lock()
	up := b.up
	iface := b.wgIface
	b.mu.Unlock()

	if !up || iface == "" {
		return core.TunnelStats{}
	}
	st := core.TunnelStats{Connected: true, WgIface: iface}
	i, err := b.rci.Interface(iface)
	if err != nil || i.Wireguard == nil || len(i.Wireguard.Peers) == 0 {
		return st
	}
	p := i.Wireguard.Peers[0]
	st.RxBytes = uint64(p.RxBytes)
	st.TxBytes = uint64(p.TxBytes)
	// last-handshake NDM отдаёт как «секунд назад». 0 без online-флага
	// трактуем как «рукопожатия ещё не было». VERIFY-ON-DEVICE.
	if p.LastHandshake > 0 || bool(p.Online) {
		st.LastHandshake = time.Now().Add(-time.Duration(p.LastHandshake) * time.Second)
	}
	return st
}

// NetworkChanged опрашивает WAN через RCI; при смене интерфейса (перетык
// кабеля, LTE-резерв) переустанавливает bypass-маршруты через новый WAN.
func (b *Backend) NetworkChanged() bool {
	b.mu.Lock()
	rm := b.routeMgr
	b.mu.Unlock()
	if rm == nil {
		return false
	}
	return rm.RefreshWAN()
}

// WgIface возвращает выбранный WireguardN (запоминается в state.json, чтобы
// после рестарта демон управлял тем же подключением).
func (b *Backend) WgIface() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.wgIface
}
