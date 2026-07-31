//go:build !linux

package proxy

import (
	"context"
	"net"
	"syscall"
	"time"
)

// SetProtectFwmark — no-op вне Linux (fwmark-маршрутизация недоступна).
func SetProtectFwmark(int) {}

// protectControl — no-op вне Linux. Защита от петли на не-Linux достигается
// host-маршрутами для TURN-серверов, добавляемыми до подъёма туннеля.
func protectControl(_ string, _ string, _ syscall.RawConn) error { return nil }

// reuseAddrControl — no-op вне Linux: семантика SO_REUSEADDR для UDP там
// другая, а быстрый рестарт слушателя проблем не создаёт (см. listenUDP).
func reuseAddrControl(_ string, _ string, _ syscall.RawConn) error { return nil }

// protectAndDial дайлит соединение без модификаций сокета (см. protectControl).
func protectAndDial(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{
		Timeout:   5 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	return dialer.DialContext(ctx, network, addr)
}
