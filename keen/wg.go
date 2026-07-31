package keen

import (
	"fmt"
	"net"
	"strings"

	"github.com/wgkeybot/router/core"
)

// wg.go — провижининг встроенного WireGuard-клиента KeeneticOS через RCI.
// В отличие от Windows/OpenWrt-портов wireguard-go в процессе не поднимается:
// демон только настраивает ядерный WG прошивки, endpoint которого указывает на
// локальный TURN-прокси.

// WgDescription — маркер владения: WG-подключение с этим description создано
// демоном, и только его демон имеет право перенастраивать.
const WgDescription = "WgKeyBot"

// ChooseWgIface выбирает имя WG-интерфейса NDM. Приоритет: remembered из
// state.json (если всё ещё наш) → desired из настроек (если свободен или наш) →
// существующий наш под другим именем → первый свободный WireguardN.
// Чужие подключения пользователя (другой description) не трогаются.
func ChooseWgIface(rci *RCI, desired, remembered string) (string, error) {
	m, err := rci.Interfaces()
	if err != nil {
		return "", err
	}
	isOurs := func(name string) bool {
		i, ok := m[name]
		return ok && i.Description == WgDescription
	}

	if remembered != "" && isOurs(remembered) {
		return remembered, nil
	}
	if desired != "" {
		if _, exists := m[desired]; !exists || isOurs(desired) {
			return desired, nil
		}
	}
	for name, i := range m {
		if strings.HasPrefix(name, "Wireguard") && i.Description == WgDescription {
			return name, nil
		}
	}
	for n := 0; n < 32; n++ {
		name := fmt.Sprintf("Wireguard%d", n)
		if _, exists := m[name]; !exists {
			return name, nil
		}
	}
	return "", fmt.Errorf("no free WireguardN interface (0-31 taken)")
}

// buildProvisionCmds строит parse-команды идемпотентной настройки интерфейса.
// stalePeers — публичные ключи существующих пиров, которых нет в конфиге
// (снимаются, чтобы смена ключа в конфиге не оставляла мёртвых пиров).
// VERIFY-ON-DEVICE: синтаксис записан по официальной инструкции Keenetic по
// WG-клиенту и примерам сообщества для 4.x (`ip global auto` в т.ч.).
func buildProvisionCmds(name string, cfg *core.TunnelConfig, endpoint string, keepalive int, stalePeers []string) ([]string, error) {
	if cfg.PrivateKey == "" || cfg.PublicKey == "" {
		return nil, fmt.Errorf("config is missing PrivateKey/PublicKey")
	}
	if len(cfg.Address) == 0 {
		return nil, fmt.Errorf("config is missing interface Address")
	}
	if err := core.ValidateEndpoint(endpoint); err != nil {
		return nil, fmt.Errorf("bad endpoint %q: %w", endpoint, err)
	}
	ip, mask, err := cidrToAddrMask(cfg.Address[0])
	if err != nil {
		return nil, fmt.Errorf("bad address %q: %w", cfg.Address[0], err)
	}

	iface := "interface " + name
	cmds := []string{
		iface,
		iface + " description " + WgDescription,
		iface + " security-level public",
		fmt.Sprintf("%s ip address %s %s", iface, ip, mask),
	}
	if cfg.MTU > 0 {
		cmds = append(cmds, fmt.Sprintf("%s ip mtu %d", iface, cfg.MTU))
	}
	cmds = append(cmds, iface+" wireguard private-key "+cfg.PrivateKey)

	for _, pub := range stalePeers {
		cmds = append(cmds, iface+" no wireguard peer "+pub)
	}

	peer := iface + " wireguard peer " + cfg.PublicKey
	cmds = append(cmds, peer+" endpoint "+endpoint)
	if cfg.PresharedKey != "" {
		cmds = append(cmds, peer+" preshared-key "+cfg.PresharedKey)
	}
	ka := cfg.PersistentKeepalive
	if ka <= 0 {
		ka = keepalive
	}
	if ka > 0 {
		cmds = append(cmds, fmt.Sprintf("%s keepalive-interval %d", peer, ka))
	}
	allowed := cfg.AllowedIPs
	if len(allowed) == 0 {
		allowed = []string{"0.0.0.0/0"}
	}
	for _, a := range allowed {
		cmds = append(cmds, peer+" allow-ips "+a)
	}
	// Участие в выборе интернет-подключения: без него NDM не пустит трафик
	// LAN-клиентов через туннель даже при allow-ips 0.0.0.0/0.
	cmds = append(cmds,
		iface+" ip global auto",
		iface+" up",
	)
	return cmds, nil
}

// ProvisionWg применяет конфиг к интерфейсу NDM и персистит конфигурацию.
// endpoint — адрес локального TURN-прокси (например 127.0.0.1:51821).
func ProvisionWg(rci *RCI, name string, cfg *core.TunnelConfig, endpoint string, keepalive int) error {
	var stale []string
	if existing, err := rci.Interface(name); err == nil && existing.Wireguard != nil {
		for _, p := range existing.Wireguard.Peers {
			if p.PublicKey != "" && p.PublicKey != cfg.PublicKey {
				stale = append(stale, p.PublicKey)
			}
		}
	}
	cmds, err := buildProvisionCmds(name, cfg, endpoint, keepalive, stale)
	if err != nil {
		return err
	}
	if err := rci.Parse(cmds...); err != nil {
		return fmt.Errorf("provision %s: %w", name, err)
	}
	return rci.Save()
}

// WgUp поднимает интерфейс.
func WgUp(rci *RCI, name string) error {
	return rci.Parse("interface " + name + " up")
}

// WgDown опускает интерфейс. Само подключение не удаляется — остаётся видимым
// (выключенным) в веб-интерфейсе Keenetic.
func WgDown(rci *RCI, name string) error {
	return rci.Parse("interface " + name + " down")
}

// cidrToAddrMask конвертирует "10.0.0.5/32" в ("10.0.0.5", "255.255.255.255").
// Адрес без префикса трактуется как /32. NDM-команда `ip address` в форме
// <address> <mask> принимается всеми версиями, в отличие от CIDR-формы.
func cidrToAddrMask(cidr string) (string, string, error) {
	if !strings.Contains(cidr, "/") {
		cidr += "/32"
	}
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", "", err
	}
	v4 := ip.To4()
	if v4 == nil {
		return "", "", fmt.Errorf("only IPv4 interface addresses are supported, got %q", cidr)
	}
	mask := net.IP(ipnet.Mask)
	if len(ipnet.Mask) != net.IPv4len {
		return "", "", fmt.Errorf("bad IPv4 mask in %q", cidr)
	}
	return v4.String(), mask.String(), nil
}
