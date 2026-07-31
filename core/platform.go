package core

import (
	"time"

	"github.com/wgkeybot/router/pkg/proxy"
)

// platform.go — граница между платформенно-нейтральным ядром и мостами
// keen/ (KeeneticOS через RCI) и owrt/ (OpenWrt через netlink + wireguard-go).
//
// Разделение проходит ровно по тому, что у платформ реально различается:
// откуда читаются настройки и DNS, куда кладутся файлы — это Platform;
// как поднимается WireGuard и как трафик самого прокси удерживается вне
// туннеля — это Backend. Всё остальное (поток подключения, watchdog, captcha,
// control-сокет, панель, CLI) живёт здесь в одном экземпляре.

// Platform — платформенное окружение демона. Реализации: keen.Platform, owrt.Platform.
type Platform interface {
	// Name — "keenetic" или "openwrt"; попадает в STATUS и логи.
	Name() string

	// InitLogging настраивает журналирование. Keenetic пишет в файл с
	// ротацией и в syslog; на OpenWrt это no-op — stdout забирает procd.
	InitLogging()
	// RotateLogs вызывается периодически из главного цикла демона.
	RotateLogs()

	// ReadSettings возвращает сырые пары ключ-значение из платформенного
	// источника (плоский conf на Keenetic, UCI на OpenWrt). Разбор значений
	// делает ядро — см. applySetting.
	ReadSettings() map[string]string
	// ApplyDefaults накладывает платформенные умолчания поверх общих. Зовётся
	// до разбора пользовательских значений.
	ApplyDefaults(*Settings)

	// SystemDNS — upstream-DNS провайдера для резолва VK/TURN до туннеля.
	// Loopback (DNS-прокси самого роутера) исключается вызываемой стороной.
	SystemDNS() []string

	// LANIP — адрес роутера в LAN: на нём показываются captcha-страница и
	// веб-панель. "" если определить не удалось.
	LANIP() string

	// NewBackend создаёт бэкенд туннеля под этот конфиг и настройки.
	NewBackend(cfg *TunnelConfig, s Settings, st State) (Backend, error)
}

// Backend — способ поднять WireGuard поверх готового TURN-прокси и удержать
// собственный трафик прокси вне туннеля.
//
// Две реализации решают вторую задачу по-разному. OpenWrt метит сокеты прокси
// через SO_MARK и правилом `ip rule fwmark N lookup main` уводит их на
// физический WAN. На Keenetic маршрутизацией распоряжается NDM и fwmark
// недоступен, поэтому для каждого IP, с которым говорит прокси, ставится
// host-маршрут /32 через WAN. Отсюда два метода вместо одного: Prepare знает
// только TurnIP из конфига и вызывается до старта прокси, а Bypass добирает
// адреса, которые становятся известны уже после bootstrap (динамические
// TURN-серверы и auth-хосты VK/OK).
type Backend interface {
	// Prepare выполняется до proxy.NewTunnel, пока не отправлено ни одного
	// пакета: owrt ставит fwmark, keen определяет WAN и открывает обход до
	// turnIP. turnIP может быть пустым.
	Prepare(turnIP string) error

	// Bypass добавляет обход для IP, с которыми говорит сам прокси. Вызывается
	// после готовности прокси и до подъёма WireGuard: иначе ре-фетч
	// credentials ушёл бы в туннель, которому эти credentials и нужны.
	// На OpenWrt no-op — fwmark уже покрывает все сокеты прокси.
	Bypass(ips []string)

	// Up поднимает WireGuard с endpoint пира = listenAddr (локальный прокси).
	Up(t *proxy.Tunnel, listenAddr string) error

	// Down опускает интерфейс и снимает всё, что поставили Prepare/Bypass/Up.
	// Должен быть идемпотентен: Manager зовёт его и при откате неудачного
	// Connect, когда Up ещё не выполнялся.
	Down()

	// Stats — счётчики и время последнего рукопожатия. Нулевое
	// LastHandshake означает «рукопожатия ещё не было».
	Stats() TunnelStats

	// NetworkChanged сообщает, сменился ли физический аплинк с прошлой
	// проверки, и переустанавливает то, что к нему привязано.
	NetworkChanged() bool

	// WgIface — имя поднятого интерфейса. На Keenetic это выбранный
	// WireguardN (запоминается в state.json), на OpenWrt — имя TUN.
	WgIface() string
}

// TunnelStats — снимок состояния туннеля.
type TunnelStats struct {
	Connected     bool
	WgIface       string
	RxBytes       uint64
	TxBytes       uint64
	LastHandshake time.Time
}
