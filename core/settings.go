package core

import (
	"strconv"
	"strings"
)

// settings.go — пользовательские настройки демона. Структура одна на обе
// платформы: наборы ключей не пересекаются (кроме captcha_listen), поэтому
// разбор общий, а платформа отвечает только за то, откуда взялись пары
// ключ-значение (плоский conf на Keenetic, UCI на OpenWrt) и какие умолчания
// для неё осмысленны.
//
// Поля, не имеющие смысла на конкретной платформе, просто не читаются её
// бэкендом — это дешевле, чем вторая структура и приведение типов на границе.

// Mode определяет, как поднимать туннель.
type Mode string

const (
	// ModeGateway — интерфейс + маршрутизация: трафик LAN-клиентов в туннель.
	ModeGateway Mode = "gateway"
	// ModeSOCKS — userspace netstack + локальный SOCKS5, без изменения
	// системной маршрутизации. Только OpenWrt: на Keenetic туннель поднимает
	// ядро прошивки, netstack там неоткуда взять.
	ModeSOCKS Mode = "socks"
)

// Общие умолчания.
const (
	DefaultCaptchaListen = "0.0.0.0:8089"
	DefaultPanelPort     = 1441
	DefaultMTU           = 1280
)

// Умолчания OpenWrt.
const (
	DefaultIfname    = "wgkb0"
	DefaultSocksPort = 1080
	DefaultLan       = "lan"
	DefaultFwmark    = 0x4b8 // 1208
	DefaultTable     = 51820
)

// Умолчания Keenetic.
const (
	DefaultWgIface    = "Wireguard0"
	DefaultProxyPort  = 51821
	DefaultListenAddr = "127.0.0.1"
	DefaultKeepalive  = 25
	// Именно 127.0.0.1, не localhost: в KeeneticOS 5.1.2 запросы к RCI с
	// Host: localhost отдают 403 (исправлено только в 5.2). IP-форма
	// принимается всеми версиями.
	DefaultRCIURL = "http://127.0.0.1:79/rci"
)

// Settings — настройки демона.
type Settings struct {
	// ── Общие ───────────────────────────────────────────────────────────

	// Enabled — гейт запуска. На OpenWrt его проверяет init-скрипт (UCI
	// option enabled), на Keenetic всегда true: сервис включается установкой.
	Enabled bool
	// Mode — gateway или socks. На Keenetic всегда gateway.
	Mode Mode
	// CaptchaListen — адрес captcha reverse-proxy (legacy-fallback поток).
	CaptchaListen string
	// PanelPort — порт веб-панели на LAN; 0 — панель выключена.
	PanelPort int
	// MTU — MTU туннеля; MTU из конфига имеет приоритет.
	MTU int

	// ── OpenWrt ─────────────────────────────────────────────────────────

	// Ifname — имя создаваемого TUN-интерфейса.
	Ifname string
	// SocksPort — порт локального SOCKS5 в режиме socks.
	SocksPort int
	// Lan — имя network-интерфейса OpenWrt, чьи клиенты идут в туннель.
	Lan string
	// Fwmark — метка сокетов прокси (SO_MARK) для обхода туннеля.
	Fwmark int
	// Table — номер таблицы маршрутизации туннеля.
	Table int
	// NAT — ставить ли nft-masquerade fallback помимо fw4-зоны.
	NAT bool

	// ── Keenetic ────────────────────────────────────────────────────────

	// WgIface — желаемое имя WG-интерфейса NDM. Если занято чужим
	// подключением, демон берёт первый свободный WireguardN и запоминает его.
	WgIface string
	// ProxyPort — фиксированный UDP-порт TURN-прокси; на него указывает
	// endpoint ядерного WireGuard. 0 — выбрать свободный (так работает
	// OpenWrt, где endpoint отдаётся wireguard-go уже после старта прокси).
	ProxyPort int
	// ListenAddr — адрес, на котором слушает прокси. 127.0.0.1 по умолчанию;
	// если прошивка отвергнет loopback-endpoint — сюда ставится LAN-IP.
	ListenAddr string
	// Keepalive — persistent-keepalive для пира (секунды).
	Keepalive int
	// RCIURL — базовый URL локального REST API прошивки. Меняется только
	// в тестах (mock-сервер).
	RCIURL string
}

// LoadSettings читает настройки платформы и приводит их к валидному виду.
// Отсутствующий источник или некорректные значения — не ошибка: поля
// заполняются умолчаниями.
func LoadSettings(p Platform) Settings {
	s := Settings{
		Mode:          ModeGateway,
		CaptchaListen: DefaultCaptchaListen,
		PanelPort:     DefaultPanelPort,
		MTU:           DefaultMTU,
	}
	p.ApplyDefaults(&s)
	for k, v := range p.ReadSettings() {
		applySetting(&s, k, v)
	}
	s.normalize()
	return s
}

// applySetting разбирает одну пару ключ-значение. Неизвестные ключи молча
// игнорируются: чужие опции в UCI-секции и закомментированные примеры в conf —
// норма, а не повод падать.
func applySetting(s *Settings, k, v string) {
	switch k {
	// Общие
	case "enabled":
		s.Enabled = parseBool(v)
	case "mode":
		s.Mode = Mode(strings.ToLower(strings.TrimSpace(v)))
	case "captcha_listen":
		s.CaptchaListen = v
	case "panel_port":
		setInt(&s.PanelPort, v)
	case "mtu":
		setInt(&s.MTU, v)

	// OpenWrt
	case "ifname":
		s.Ifname = v
	case "socks_port":
		setInt(&s.SocksPort, v)
	case "lan":
		s.Lan = v
	case "fwmark":
		if n, err := parseIntAuto(v); err == nil {
			s.Fwmark = n
		}
	case "table":
		setInt(&s.Table, v)
	case "nat":
		s.NAT = parseBool(v)

	// Keenetic
	case "wg_iface":
		s.WgIface = v
	case "proxy_port":
		setInt(&s.ProxyPort, v)
	case "listen_addr":
		s.ListenAddr = v
	case "keepalive":
		setInt(&s.Keepalive, v)
	case "rci_url":
		s.RCIURL = v
	}
}

func (s *Settings) normalize() {
	if s.Mode != ModeGateway && s.Mode != ModeSOCKS {
		s.Mode = ModeGateway
	}
	if s.CaptchaListen == "" {
		s.CaptchaListen = DefaultCaptchaListen
	}
	// PanelPort == 0 — валидное значение «панель выключена».
	if s.PanelPort < 0 || s.PanelPort > 65535 {
		s.PanelPort = DefaultPanelPort
	}
	if s.MTU < 1280 || s.MTU > 9000 {
		s.MTU = DefaultMTU
	}

	if s.Ifname == "" {
		s.Ifname = DefaultIfname
	}
	if s.SocksPort < 1 || s.SocksPort > 65535 {
		s.SocksPort = DefaultSocksPort
	}
	if s.Lan == "" {
		s.Lan = DefaultLan
	}
	// Верхняя граница fwmark не проверяется: на 32-битных арках int не вмещает
	// uint32, а практические метки малы. Некорректное отсекается по <= 0.
	if s.Fwmark <= 0 {
		s.Fwmark = DefaultFwmark
	}
	if s.Table <= 0 {
		s.Table = DefaultTable
	}

	if s.WgIface == "" {
		s.WgIface = DefaultWgIface
	}
	// ProxyPort == 0 — валидное значение «взять свободный порт».
	if s.ProxyPort < 0 || s.ProxyPort > 65535 {
		s.ProxyPort = DefaultProxyPort
	}
	if s.ListenAddr == "" {
		s.ListenAddr = DefaultListenAddr
	}
	if s.Keepalive < 0 || s.Keepalive > 3600 {
		s.Keepalive = DefaultKeepalive
	}
	s.RCIURL = strings.TrimRight(s.RCIURL, "/")
	if s.RCIURL == "" {
		s.RCIURL = DefaultRCIURL
	}
}

func setInt(dst *int, v string) {
	if n, err := strconv.Atoi(v); err == nil {
		*dst = n
	}
}

// parseIntAuto разбирает int в десятичном или 0x-hex виде (fwmark в UCI
// принято записывать шестнадцатерично).
func parseIntAuto(v string) (int, error) {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, "0x") || strings.HasPrefix(v, "0X") {
		n, err := strconv.ParseInt(v[2:], 16, 64)
		return int(n), err
	}
	n, err := strconv.ParseInt(v, 10, 64)
	return int(n), err
}

func parseBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
