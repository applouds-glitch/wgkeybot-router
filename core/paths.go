package core

// paths.go — файловая раскладка, различающаяся между платформами. На Keenetic
// корень прошивки read-only и всё стороннее живёт в /opt (Entware); на OpenWrt
// — обычные /etc и /var.
//
// Значения по умолчанию — openwrt'шные, чтобы пакет работал даже если
// платформа почему-то не позвала SetPaths (например, в тестах core).

// Paths — набор путей, которые платформа сообщает ядру при старте.
type Paths struct {
	// ConfigDir — каталог секретов: config.conf и state.json, оба 0600.
	ConfigDir string
	// ControlSocket — unix-сокет управления демоном.
	ControlSocket string
	// InitScript — init-скрипт сервиса; печатается в подсказках CLI.
	InitScript string
	// LogPath — файл журнала. Пустой означает «журнал в файл не пишем»
	// (OpenWrt: stdout забирает procd).
	LogPath string
}

var paths = Paths{
	ConfigDir:     "/etc/wgkeybot",
	ControlSocket: "/var/run/wgkeybot.sock",
	InitScript:    "/etc/init.d/wgkeybot",
}

// SetPaths задаёт раскладку. Вызывается ровно один раз при создании платформы,
// до любой работы с конфигом, состоянием или control-сокетом.
func SetPaths(p Paths) { paths = p }

// ConfigDir — каталог секретов (0700 не требуется: сами файлы 0600).
func ConfigDir() string { return paths.ConfigDir }

// ControlSocket — путь unix-сокета управления.
func ControlSocket() string { return paths.ControlSocket }

// InitScript — путь init-скрипта сервиса.
func InitScript() string { return paths.InitScript }

// LogPath — путь файла журнала ("" — не пишем).
func LogPath() string { return paths.LogPath }
