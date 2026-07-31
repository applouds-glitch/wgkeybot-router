package owrt

import (
	"fmt"
	"log"

	"github.com/vishvananda/netlink"
)

// ConfigureInterface назначает адреса и MTU TUN-интерфейсу по имени и поднимает
// его. wireguard-go уже создал устройство (tun.CreateTUN); здесь только сетевая
// конфигурация через netlink (аналог winbridge.SetupInterface на IP Helper API).
func ConfigureInterface(name string, addresses []string, mtu int) error {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return fmt.Errorf("link %q: %w", name, err)
	}
	if mtu > 0 {
		if err := netlink.LinkSetMTU(link, mtu); err != nil {
			log.Printf("[IF] warn: set MTU %d on %s: %v", mtu, name, err)
		}
	}
	for _, a := range addresses {
		addr, err := netlink.ParseAddr(a)
		if err != nil {
			log.Printf("[IF] skip invalid address %q: %v", a, err)
			continue
		}
		if err := netlink.AddrReplace(link, addr); err != nil {
			return fmt.Errorf("add addr %s on %s: %w", a, name, err)
		}
		log.Printf("[IF] %s addr %s", name, a)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("link up %q: %w", name, err)
	}
	return nil
}
