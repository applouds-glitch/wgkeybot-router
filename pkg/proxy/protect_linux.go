package proxy

import (
	"context"
	"net"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// protectFwmark — SO_MARK, который ставится на исходящие TURN/DNS-сокеты, чтобы
// их пакеты уходили мимо VPN-туннеля (правило `ip rule fwmark <mark> lookup
// main` направляет помеченный трафик через физический WAN). 0 — отключено.
var protectFwmark atomic.Int32

// SetProtectFwmark задаёт fwmark для всех защищённых сокетов. Вызывать один раз
// при старте до подъёма туннеля. mark == 0 отключает защиту (no-op).
func SetProtectFwmark(mark int) { protectFwmark.Store(int32(mark)) }

// protectControl — Control-хук для net.Dialer/ListenConfig. На Linux ставит
// SO_MARK на сокет, чтобы ядро маршрутизировало его пакеты через физический
// интерфейс (правило `ip rule fwmark <mark> lookup main` держит их вне туннеля).
//
// На Android этому соответствовал VpnService.protect(); здесь ту же роль играет
// fwmark-policy-routing: трафик TURN/DNS не должен зацикливаться обратно в
// WireGuard-туннель, который сам же несёт прокси.
func protectControl(_ string, _ string, c syscall.RawConn) error {
	mark := int(protectFwmark.Load())
	if mark == 0 {
		return nil
	}
	var sockErr error
	if err := c.Control(func(fd uintptr) {
		sockErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK, mark)
	}); err != nil {
		return err
	}
	return sockErr
}

// reuseAddrControl — Control-хук для net.ListenConfig: ставит SO_REUSEADDR,
// чтобы быстрый рестарт туннеля мог занять тот же локальный порт, пока
// предыдущий слушатель ещё закрывается (см. listenUDP).
func reuseAddrControl(_ string, _ string, c syscall.RawConn) error {
	var sockErr error
	if err := c.Control(func(fd uintptr) {
		sockErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1)
	}); err != nil {
		return err
	}
	return sockErr
}

// protectAndDial дайлит TCP/UDP-соединение с protect-хуком, чтобы соединение шло
// мимо туннеля (см. protectControl).
func protectAndDial(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{
		Timeout:   5 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   protectControl,
	}
	return dialer.DialContext(ctx, network, addr)
}
