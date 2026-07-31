package proxy

import "golang.zx2c4.com/wireguard/tun"

// InterfaceName возвращает имя нижележащего Linux TUN-устройства (например
// "wgkb0"), либо "" если туннель работает в netstack/SOCKS-режиме. Используется
// owrt-мостом для настройки адреса/маршрутов интерфейса через netlink по имени
// (на Linux нет аналога Windows LUID).
func (t *Tunnel) InterfaceName() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if nt, ok := t.tunDev.(*tun.NativeTun); ok {
		if name, err := nt.Name(); err == nil {
			return name
		}
	}
	return ""
}
