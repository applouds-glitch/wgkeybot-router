package owrt

import (
	"fmt"
	"log"
	"net"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// RouteManager управляет policy-routing'ом на время жизни туннеля в
// gateway-режиме. На Linux защита трафика самого прокси от петли достигается
// не host-маршрутами (как на Windows), а fwmark'ом на сокетах прокси
// (proxy.SetProtectFwmark) + правилом «fwmark → main». Поэтому здесь только:
//
//	table N:  default dev <tun>
//	rule 90:  fwmark <mark> lookup main      (трафик прокси → физический WAN)
//	rule 100: from <lan-subnet> lookup N     (LAN-клиенты → туннель)
type RouteManager struct {
	table    int
	rules    []*netlink.Rule
	tunRoute *netlink.Route
}

// SetupGateway включает policy-routing для gateway-режима.
// lanSubnets — подсети, чей трафик заворачивается в туннель; если пусто,
// заворачивается весь трафик (трафик прокси всё равно уходит на WAN по fwmark).
func SetupGateway(tunName string, table, fwmark int, lanSubnets []*net.IPNet) (*RouteManager, error) {
	link, err := netlink.LinkByName(tunName)
	if err != nil {
		return nil, fmt.Errorf("tun link %q: %w", tunName, err)
	}
	r := &RouteManager{table: table}

	// default dev <tun> в таблице table (on-link, без шлюза).
	_, defDst, _ := net.ParseCIDR("0.0.0.0/0")
	route := &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Dst:       defDst,
		Table:     table,
		Scope:     netlink.SCOPE_LINK,
	}
	if err := netlink.RouteReplace(route); err != nil {
		return nil, fmt.Errorf("default route in table %d: %w", table, err)
	}
	r.tunRoute = route
	log.Printf("[ROUTE] table %d: default dev %s", table, tunName)

	// rule: fwmark <mark> lookup main (pref 90) — трафик прокси мимо туннеля.
	markRule := netlink.NewRule()
	markRule.Mark = uint32(fwmark)
	markRule.Table = unix.RT_TABLE_MAIN
	markRule.Priority = 90
	if err := ruleReplace(markRule); err != nil {
		r.Teardown()
		return nil, fmt.Errorf("fwmark rule: %w", err)
	}
	r.rules = append(r.rules, markRule)
	log.Printf("[ROUTE] rule: fwmark 0x%x lookup main", fwmark)

	// rule: from <lan-subnet> lookup table (pref 100) — LAN-клиенты в туннель.
	if len(lanSubnets) == 0 {
		allRule := netlink.NewRule()
		allRule.Table = table
		allRule.Priority = 100
		if err := ruleReplace(allRule); err != nil {
			r.Teardown()
			return nil, fmt.Errorf("from-all rule: %w", err)
		}
		r.rules = append(r.rules, allRule)
		log.Printf("[ROUTE] no LAN subnets resolved — routing ALL via table %d (proxy bypass via fwmark)", table)
	} else {
		for _, subnet := range lanSubnets {
			rule := netlink.NewRule()
			rule.Src = subnet
			rule.Table = table
			rule.Priority = 100
			if err := ruleReplace(rule); err != nil {
				log.Printf("[ROUTE] warn: rule from %s: %v", subnet, err)
				continue
			}
			r.rules = append(r.rules, rule)
			log.Printf("[ROUTE] rule: from %s lookup %d", subnet, table)
		}
	}
	return r, nil
}

// Teardown снимает все добавленные правила и маршрут таблицы. Безопасно для nil.
func (r *RouteManager) Teardown() {
	if r == nil {
		return
	}
	for _, rule := range r.rules {
		if err := netlink.RuleDel(rule); err != nil {
			log.Printf("[ROUTE] warn: del rule: %v", err)
		}
	}
	r.rules = nil
	if r.tunRoute != nil {
		// Маршрут и так исчезает вместе с TUN, но снимаем явно.
		if err := netlink.RouteDel(r.tunRoute); err != nil {
			log.Printf("[ROUTE] warn: del table route: %v", err)
		}
		r.tunRoute = nil
	}
}

// ruleReplace делает добавление правила идемпотентным: сначала удаляет
// возможный дубликат (игнорируя ошибку), затем добавляет.
func ruleReplace(rule *netlink.Rule) error {
	_ = netlink.RuleDel(rule)
	return netlink.RuleAdd(rule)
}

// ResolveLanSubnets возвращает IPv4-подсети LAN-устройства (по имени network в
// UCI: network.<lan>.device / .ifname, иначе "br-<lan>"). Используется для
// правил «from <subnet> lookup table».
func ResolveLanSubnets(lanName string) []*net.IPNet {
	dev := lanDevice(lanName)
	link, err := netlink.LinkByName(dev)
	if err != nil {
		log.Printf("[ROUTE] LAN device %q not found: %v", dev, err)
		return nil
	}
	addrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return nil
	}
	var subnets []*net.IPNet
	for _, a := range addrs {
		if a.IPNet == nil || a.IP.IsLoopback() {
			continue
		}
		subnets = append(subnets, &net.IPNet{IP: a.IP.Mask(a.Mask), Mask: a.Mask})
	}
	return subnets
}

// LanHostIP возвращает собственный IPv4-адрес роутера на LAN-устройстве (для
// показа пользователю URL captcha-страницы). "" если определить не удалось.
func LanHostIP(lanName string) string {
	link, err := netlink.LinkByName(lanDevice(lanName))
	if err != nil {
		return ""
	}
	addrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		if a.IP != nil && !a.IP.IsLoopback() {
			return a.IP.String()
		}
	}
	return ""
}

func lanDevice(lanName string) string {
	if dev := uciGet("network." + lanName + ".device"); dev != "" {
		return dev
	}
	if dev := uciGet("network." + lanName + ".ifname"); dev != "" {
		return dev
	}
	return "br-" + lanName
}

// DefaultGatewayIP возвращает IP физического дефолтного шлюза (main-таблица),
// исключая интерфейс excludeTun. Используется для детекта смены сети.
func DefaultGatewayIP(excludeTun string) (string, error) {
	routes, err := netlink.RouteList(nil, netlink.FAMILY_V4)
	if err != nil {
		return "", err
	}
	best := -1
	bestPrio := 0
	for i := range routes {
		rt := &routes[i]
		if !isDefaultDst(rt.Dst) || rt.Gw == nil {
			continue
		}
		if rt.Table != 0 && rt.Table != unix.RT_TABLE_MAIN {
			continue
		}
		if link, err := netlink.LinkByIndex(rt.LinkIndex); err == nil {
			if link.Attrs().Name == excludeTun {
				continue
			}
		}
		if best == -1 || rt.Priority < bestPrio {
			best = i
			bestPrio = rt.Priority
		}
	}
	if best == -1 {
		return "", fmt.Errorf("default gateway not found")
	}
	return routes[best].Gw.String(), nil
}

// isDefaultDst сообщает, является ли назначение маршрута дефолтным (0.0.0.0/0).
func isDefaultDst(dst *net.IPNet) bool {
	if dst == nil {
		return true
	}
	ones, _ := dst.Mask.Size()
	return ones == 0 && dst.IP.IsUnspecified()
}
