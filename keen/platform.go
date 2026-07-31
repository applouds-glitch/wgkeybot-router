package keen

import (
	"bufio"
	"os"
	"strings"

	"github.com/wgkeybot/router/core"
)

// platform.go — реализация core.Platform для KeeneticOS.
//
// Ключевое отличие от OpenWrt: WireGuard здесь не поднимается в процессе.
// Туннель ведёт встроенный ядерный клиент прошивки, которому демон через RCI
// прописывает endpoint локального TURN-прокси. Отсюда и всё остальное —
// настройки читаются плоским conf-файлом (UCI на Keenetic нет), DNS и LAN-IP
// приходят из RCI, а собственный трафик прокси удерживается вне туннеля
// host-маршрутами, потому что маршрутизацией распоряжается NDM и fwmark
// недоступен.

// SettingsPath — плоский conf-файл настроек демона, формат `key = value`.
const SettingsPath = "/opt/etc/wgkeybot/wgkeybot.conf"

// Пути Keenetic: корень прошивки read-only, всё стороннее живёт в /opt.
var keeneticPaths = core.Paths{
	ConfigDir:     "/opt/etc/wgkeybot",
	ControlSocket: "/opt/var/run/wgkeybot.sock",
	InitScript:    "/opt/etc/init.d/S99wgkeybot",
	LogPath:       "/opt/var/log/wgkeybot.log",
}

// Platform — окружение демона на Keenetic.
type Platform struct {
	rci *RCI
}

// New создаёт платформу и объявляет ядру раскладку путей. RCI-клиент создаётся
// лениво в rciClient: базовый URL приходит из настроек, а настройки читаются
// уже через эту же платформу.
func New() (*Platform, error) {
	core.SetPaths(keeneticPaths)
	return &Platform{}, nil
}

func (p *Platform) Name() string { return "keenetic" }

func (p *Platform) InitLogging() { core.InitLogging() }
func (p *Platform) RotateLogs()  { core.RotateLogs() }

// RCI возвращает клиент прошивки, создавая его при первом обращении. Адрес
// берётся из настроек (в тестах — mock-сервер), поэтому Settings читаются
// напрямую из файла, минуя core.LoadSettings: иначе получилась бы рекурсия.
func (p *Platform) RCI() *RCI {
	if p.rci == nil {
		p.rci = NewRCI(readSettingsFile(SettingsPath)["rci_url"])
	}
	return p.rci
}

func (p *Platform) ReadSettings() map[string]string { return readSettingsFile(SettingsPath) }

// ApplyDefaults накладывает keenetic'овские умолчания. Фиксированный ProxyPort
// обязателен: endpoint ядерного WG прописывается через RCI ещё до того, как
// прокси сообщит свой реальный адрес.
func (p *Platform) ApplyDefaults(s *core.Settings) {
	s.Enabled = true // сервис включается установкой, отдельного гейта нет
	s.Mode = core.ModeGateway
	s.WgIface = core.DefaultWgIface
	s.ProxyPort = core.DefaultProxyPort
	s.ListenAddr = core.DefaultListenAddr
	s.Keepalive = core.DefaultKeepalive
	s.RCIURL = core.DefaultRCIURL
}

func (p *Platform) SystemDNS() []string { return SystemDNS(p.RCI()) }
func (p *Platform) LANIP() string       { return p.RCI().LANIP() }

// NewBackend создаёт бэкенд ядерного WireGuard через RCI.
func (p *Platform) NewBackend(cfg *core.TunnelConfig, s core.Settings, st core.State) (core.Backend, error) {
	// Настройки могли поменять rci_url между стартами демона — берём тот,
	// что действует сейчас.
	p.rci = NewRCI(s.RCIURL)
	return &Backend{rci: p.rci, cfg: cfg, settings: s, remembered: st.WgIface}, nil
}

// readSettingsFile разбирает плоский conf: `key = value`, строки с # и ; —
// комментарии. Отсутствующий файл — не ошибка, возвращается пустая карта
// (ядро подставит умолчания).
func readSettingsFile(path string) map[string]string {
	kv := make(map[string]string)
	f, err := os.Open(path)
	if err != nil {
		return kv
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		kv[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	return kv
}
