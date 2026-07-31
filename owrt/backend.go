package owrt

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/wgkeybot/router/core"
	"github.com/wgkeybot/router/pkg/proxy"
)

// backend.go — core.Backend поверх userspace wireguard-go.
//
// Собственный трафик прокси удерживается вне туннеля не маршрутами, а меткой:
// каждый TURN/DNS-сокет получает SO_MARK, а правило `ip rule fwmark N lookup
// main` уводит помеченное на физический WAN. Поэтому Bypass здесь no-op —
// метка покрывает все сокеты прокси разом, включая те, чьи адреса становятся
// известны уже после bootstrap.
type Backend struct {
	cfg      *core.TunnelConfig
	settings core.Settings

	mu       sync.Mutex
	tunnel   *proxy.Tunnel
	routeMgr *RouteManager
	socks    *SocksServer
	ifname   string
	prevGW   string // физический шлюз на момент Up (детект смены сети)
	natOn    bool
}

// Prepare помечает сокеты прокси меткой fwmark. В режиме socks защита не нужна:
// системная маршрутизация не меняется, зацикливаться нечему.
func (b *Backend) Prepare(_ string) error {
	if b.settings.Mode == core.ModeGateway {
		proxy.SetProtectFwmark(b.settings.Fwmark)
	} else {
		proxy.SetProtectFwmark(0)
	}
	return nil
}

// Bypass — no-op: обход трафика прокси обеспечивает fwmark из Prepare.
func (b *Backend) Bypass([]string) {}

// Up поднимает wireguard-go с endpoint пира на локальном прокси и, в
// gateway-режиме, настраивает интерфейс, policy-routing и NAT.
func (b *Backend) Up(t *proxy.Tunnel, listenAddr string) error {
	s := b.settings

	mtu := b.cfg.MTU
	if mtu <= 0 {
		mtu = s.MTU
	}
	wgCfg := proxy.WireGuardConfig{
		InterfaceName: s.Ifname,
		MTU:           mtu,
		UAPIConfig:    b.cfg.BuildWGUAPIConfig(listenAddr),
		Address:       strings.Join(b.cfg.Address, ","),
		DNS:           b.cfg.DNS,
	}

	if s.Mode == core.ModeSOCKS {
		log.Printf("[owrt] attaching WireGuard (netstack/SOCKS)...")
		netDev, err := t.AttachWireGuardNetstack(wgCfg)
		if err != nil {
			return fmt.Errorf("attach WireGuard (netstack): %w", err)
		}
		addr := fmt.Sprintf("127.0.0.1:%d", s.SocksPort)
		srv, err := NewSocksServer(addr, netDev.DialContext)
		if err != nil {
			return fmt.Errorf("start SOCKS server: %w", err)
		}
		go srv.Serve()
		log.Printf("[owrt] SOCKS5 proxy listening at %s", srv.Addr())

		b.mu.Lock()
		b.tunnel = t
		b.socks = srv
		b.mu.Unlock()
		return nil
	}

	log.Printf("[owrt] attaching WireGuard interface %q...", s.Ifname)
	if err := t.AttachWireGuard(wgCfg); err != nil {
		return fmt.Errorf("attach WireGuard: %w", err)
	}
	name := t.InterfaceName()
	if name == "" {
		name = s.Ifname
	}
	if err := ConfigureInterface(name, b.cfg.Address, mtu); err != nil {
		return fmt.Errorf("configure interface %s: %w", name, err)
	}
	rm, err := SetupGateway(name, s.Table, s.Fwmark, ResolveLanSubnets(s.Lan))
	if err != nil {
		return fmt.Errorf("setup gateway routing: %w", err)
	}

	natOn := false
	if s.NAT {
		if err := EnableNAT(name); err != nil {
			log.Printf("[owrt] warn: nft NAT fallback: %v (полагаемся на fw4-зону)", err)
		} else {
			natOn = true
		}
	}
	var prevGW string
	if gw, err := DefaultGatewayIP(name); err == nil {
		prevGW = gw
	}

	b.mu.Lock()
	b.tunnel = t
	b.routeMgr = rm
	b.ifname = name
	b.prevGW = prevGW
	b.natOn = natOn
	b.mu.Unlock()
	return nil
}

// Down снимает SOCKS/маршруты/NAT. Идемпотентен: Manager зовёт его и при
// откате неудачного Connect, когда Up ещё не выполнялся.
func (b *Backend) Down() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.socks != nil {
		b.socks.Close()
		b.socks = nil
	}
	if b.routeMgr != nil {
		b.routeMgr.Teardown()
		b.routeMgr = nil
	}
	if b.natOn {
		DisableNAT()
		b.natOn = false
	}
	b.tunnel = nil
}

// Stats читает счётчики из UAPI wireguard-go.
func (b *Backend) Stats() core.TunnelStats {
	b.mu.Lock()
	t := b.tunnel
	ifname := b.ifname
	b.mu.Unlock()

	if t == nil {
		return core.TunnelStats{}
	}
	uapi, err := t.IpcGet()
	if err != nil {
		return core.TunnelStats{Connected: true, WgIface: ifname}
	}
	st := parseWGStats(uapi)
	st.WgIface = ifname
	return st
}

// NetworkChanged сравнивает текущий дефолтный шлюз с зафиксированным при Up.
func (b *Backend) NetworkChanged() bool {
	b.mu.Lock()
	prevGW := b.prevGW
	ifname := b.ifname
	b.mu.Unlock()

	if prevGW == "" {
		return false
	}
	gw, err := DefaultGatewayIP(ifname)
	if err != nil || gw == "" || gw == prevGW {
		return false
	}
	log.Printf("[owrt] gateway changed: %s -> %s", prevGW, gw)
	b.mu.Lock()
	b.prevGW = gw
	b.mu.Unlock()
	return true
}

// WgIface возвращает имя поднятого TUN-интерфейса ("" в режиме socks —
// системного интерфейса там нет).
func (b *Backend) WgIface() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ifname
}

// parseWGStats извлекает счётчики трафика и время рукопожатия из UAPI-ответа.
func parseWGStats(uapi string) core.TunnelStats {
	st := core.TunnelStats{Connected: true}
	var lastHandshake int64
	for _, line := range strings.Split(uapi, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "rx_bytes":
			fmt.Sscanf(v, "%d", &st.RxBytes)
		case "tx_bytes":
			fmt.Sscanf(v, "%d", &st.TxBytes)
		case "last_handshake_time_sec":
			fmt.Sscanf(v, "%d", &lastHandshake)
		}
	}
	if lastHandshake > 0 {
		st.LastHandshake = time.Unix(lastHandshake, 0)
	}
	return st
}
