package owrt

import (
	"github.com/wgkeybot/router/core"
)

// platform.go — реализация core.Platform для OpenWrt.
//
// В отличие от Keenetic туннель поднимается в процессе: userspace
// wireguard-go, интерфейс и маршруты настраиваются через netlink. Это
// сознательный выбор — kmod-wireguard есть не во всех сборках, а TURN-прокси
// всё равно обрабатывает каждый пакет, так что ядерная разгрузка добавила бы
// лишний loopback-хоп ради небольшого выигрыша.

// Пути OpenWrt — обычные системные.
var openwrtPaths = core.Paths{
	ConfigDir:     "/etc/wgkeybot",
	ControlSocket: "/var/run/wgkeybot.sock",
	InitScript:    "/etc/init.d/wgkeybot",
	// LogPath пуст: журнал идёт в stdout, его забирает procd.
}

// Platform — окружение демона на OpenWrt.
type Platform struct{}

// New создаёт платформу и объявляет ядру раскладку путей.
func New() (*Platform, error) {
	core.SetPaths(openwrtPaths)
	return &Platform{}, nil
}

func (p *Platform) Name() string { return "openwrt" }

// InitLogging/RotateLogs — no-op: procd забирает stdout демона в системный лог,
// второй файл журнала на роутере с ограниченной флеш-памятью не нужен.
func (p *Platform) InitLogging() {}
func (p *Platform) RotateLogs()  {}

func (p *Platform) ReadSettings() map[string]string { return readUCI() }

// ApplyDefaults накладывает openwrt'шные умолчания. ProxyPort остаётся нулём:
// endpoint пира собирается из t.ListenAddr() уже после готовности прокси, так
// что фиксировать порт незачем.
func (p *Platform) ApplyDefaults(s *core.Settings) {
	s.Enabled = false // включается явно: uci set wgkeybot.main.enabled=1
	s.Mode = core.ModeGateway
	s.Ifname = core.DefaultIfname
	s.SocksPort = core.DefaultSocksPort
	s.Lan = core.DefaultLan
	s.Fwmark = core.DefaultFwmark
	s.Table = core.DefaultTable
	s.NAT = true
	s.ProxyPort = 0
	s.ListenAddr = "127.0.0.1"
}

func (p *Platform) SystemDNS() []string { return SystemDNS() }

// LANIP — адрес роутера в LAN, на котором показываются captcha и панель.
// Определяется по имени network-интерфейса из настроек; при неудаче "" —
// вызывающая сторона сделает fallback на все адреса.
func (p *Platform) LANIP() string {
	lan := readUCI()["lan"]
	if lan == "" {
		lan = core.DefaultLan
	}
	return LanHostIP(lan)
}

// NewBackend создаёт бэкенд userspace-WireGuard.
func (p *Platform) NewBackend(cfg *core.TunnelConfig, s core.Settings, _ core.State) (core.Backend, error) {
	return &Backend{cfg: cfg, settings: s}, nil
}
