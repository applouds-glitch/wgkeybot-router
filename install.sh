#!/bin/sh
# wgkeybot installer — общий для OpenWrt и Keenetic (Entware).
#
# Платформа определяется автоматически; качается соответствующий статический
# бинарник из GitHub Releases и разворачивается сервис.
#
#   wget -O - https://github.com/applouds-glitch/wgkeybot-router/releases/latest/download/install.sh | sh
#   # конкретная версия:
#   sh install.sh v0.1.0
#   # удаление:
#   sh install.sh uninstall
#
# Разворачиваемые файлы (init-скрипты, хуки, дефолтные конфиги) зеркалируют
# packaging/ — держите в синхроне.

set -e

REPO="applouds-glitch/wgkeybot-router"
VERSION="${1:-latest}"

err() { echo "ERROR: $*" >&2; exit 1; }

[ "$(id -u)" = "0" ] || err "запустите от root"

# ── Определение платформы ────────────────────────────────────────────────────
# OpenWrt узнаётся по /etc/openwrt_release. Keenetic — по каталогу Entware плюс
# ответу локального RCI прошивки: одного /opt мало, Entware ставят и на другие
# прошивки, а без RCI демону всё равно не с чем работать.
detect_platform() {
	[ -f /etc/openwrt_release ] && { echo openwrt; return; }
	if [ -d /opt/etc/init.d ]; then
		if wget -q -T 3 -O /dev/null http://localhost:79/rci/show/version 2>/dev/null; then
			echo keenetic; return
		fi
		err "найден Entware, но локальный RCI (http://localhost:79) не отвечает —
     это не KeeneticOS 4.x либо веб-интерфейс отключён"
	fi
	echo ""
}

PLATFORM="$(detect_platform)"
[ -n "$PLATFORM" ] || err "не удалось определить платформу: нет ни /etc/openwrt_release (OpenWrt),
     ни /opt/etc/init.d (Entware на Keenetic)"

download() {
	# $1 url, $2 dest
	command -v uclient-fetch >/dev/null 2>&1 && uclient-fetch -O "$2" "$1" && return 0
	command -v wget >/dev/null 2>&1 && wget -q -O "$2" "$1" && return 0
	command -v curl >/dev/null 2>&1 && curl -fsL -o "$2" "$1" && return 0
	return 1
}

asset_url() {
	# $1 имя ассета
	if [ "$VERSION" = "latest" ]; then
		echo "https://github.com/$REPO/releases/latest/download/$1"
	else
		echo "https://github.com/$REPO/releases/download/$VERSION/$1"
	fi
}

# ══════════════════════════════════════════════════════════════════════════════
# OpenWrt
# ══════════════════════════════════════════════════════════════════════════════
install_openwrt() {
	detect_goarch() {
		arch=""
		[ -f /etc/openwrt_release ] && . /etc/openwrt_release
		arch="$DISTRIB_ARCH"
		[ -n "$arch" ] || arch="$(opkg print-architecture 2>/dev/null | awk '{print $2}' | grep -v -e '^all$' -e '^noarch$' | tail -1)"

		case "$arch" in
			x86_64*)        echo "amd64" ;;
			aarch64*)       echo "arm64" ;;
			arm_*|armv7*)   echo "armv7" ;;
			mipsel_*)       echo "mipsle" ;;
			mips_*)         echo "mips" ;;
			*)
				# Запасной путь через uname + endianness.
				case "$(uname -m)" in
					x86_64) echo "amd64" ;;
					aarch64) echo "arm64" ;;
					armv7l|armv7) echo "armv7" ;;
					mips|mips64)
						b=$(od -An -j5 -N1 -tu1 /bin/busybox 2>/dev/null | tr -d ' ')
						[ "$b" = "2" ] && echo "mips" || echo "mipsle" ;;
					*) echo "" ;;
				esac ;;
		esac
	}

	GOARCH="$(detect_goarch)"
	[ -n "$GOARCH" ] || err "не удалось определить архитектуру (DISTRIB_ARCH=$DISTRIB_ARCH, uname=$(uname -m))"
	echo "Платформа: OpenWrt, архитектура: $GOARCH"

	URL="$(asset_url "wgkeybot-$GOARCH")"
	echo "Загрузка $URL ..."
	TMP="/tmp/wgkeybot.$$"
	trap 'rm -f "$TMP"' EXIT
	download "$URL" "$TMP" || err "не удалось скачать бинарник (нужен ca-bundle для https?)"
	[ -s "$TMP" ] || err "скачан пустой файл"

	install -m 0755 "$TMP" /usr/bin/wgkeybot
	rm -f "$TMP"; trap - EXIT
	echo "Установлен /usr/bin/wgkeybot ($(/usr/bin/wgkeybot version 2>/dev/null || echo '?'))"

	cat > /etc/init.d/wgkeybot <<'INITEOF'
#!/bin/sh /etc/rc.common

# wgkeybot — WireGuard/TURN VPN client (procd service)

USE_PROCD=1
START=95
STOP=10

PROG=/usr/bin/wgkeybot

start_service() {
	config_load wgkeybot
	local enabled
	config_get_bool enabled main enabled 0
	if [ "$enabled" != "1" ]; then
		logger -t wgkeybot "disabled — enable with: uci set wgkeybot.main.enabled=1; uci commit"
		return 0
	fi

	procd_open_instance
	procd_set_param command "$PROG" run
	procd_set_param respawn
	procd_set_param stdout 1
	procd_set_param stderr 1
	procd_set_param pidfile /var/run/wgkeybot.pid
	procd_close_instance
}

reload_service() {
	# Изменился UCI или вызван `reload` — переподнять с обновлённым конфигом.
	"$PROG" reload >/dev/null 2>&1 || restart
}

service_triggers() {
	procd_add_reload_trigger "wgkeybot"
}
INITEOF
	chmod 0755 /etc/init.d/wgkeybot

	if [ ! -f /etc/config/wgkeybot ]; then
		cat > /etc/config/wgkeybot <<'UCIEOF'
config wgkeybot 'main'
	option enabled '0'
	option mode 'gateway'
	option ifname 'wgkb0'
	option mtu '1280'
	option socks_port '1080'
	option lan 'lan'
	option fwmark '0x4b8'
	option table '51820'
	option nat '1'
	option captcha_listen '0.0.0.0:8089'
	option panel_port '1441'
UCIEOF
		echo "Создан /etc/config/wgkeybot"
	fi

	# firewall-зона (один раз)
	IFNAME="$(uci -q get wgkeybot.main.ifname)"; [ -n "$IFNAME" ] || IFNAME="wgkb0"
	LAN="$(uci -q get wgkeybot.main.lan)"; [ -n "$LAN" ] || LAN="lan"
	if ! uci -q show firewall | grep -q "name='wgkeybot'"; then
		z="$(uci add firewall zone)"
		uci set firewall.$z.name='wgkeybot'
		uci set firewall.$z.input='REJECT'
		uci set firewall.$z.output='ACCEPT'
		uci set firewall.$z.forward='REJECT'
		uci set firewall.$z.masq='1'
		uci set firewall.$z.mtu_fix='1'
		uci add_list firewall.$z.device="$IFNAME"
		f="$(uci add firewall forwarding)"
		uci set firewall.$f.src="$LAN"
		uci set firewall.$f.dest='wgkeybot'
		uci commit firewall
		[ -x /etc/init.d/firewall ] && /etc/init.d/firewall reload >/dev/null 2>&1
		echo "Создана firewall-зона wgkeybot (masq, forward из $LAN)"
	fi

	/etc/init.d/wgkeybot enable 2>/dev/null || true

	cat <<DONE

Готово. Дальше:
  1. wgkeybot import <token>           # токен от @wg_key_bot
  2. uci set wgkeybot.main.enabled=1; uci commit wgkeybot
  3. /etc/init.d/wgkeybot start
  4. wgkeybot status

Панель доступна на http://<LAN-IP роутера>:1441 (option panel_port).
Если потребуется captcha — выполните 'wgkeybot status' и откройте показанный URL
с телефона/ПК в той же сети.
DONE
}

uninstall_openwrt() {
	[ -x /etc/init.d/wgkeybot ] && /etc/init.d/wgkeybot stop 2>/dev/null || true
	[ -x /etc/init.d/wgkeybot ] && /etc/init.d/wgkeybot disable 2>/dev/null || true
	rm -f /usr/bin/wgkeybot /etc/init.d/wgkeybot
	echo "Файлы удалены. Настройки (/etc/config/wgkeybot), секреты (/etc/wgkeybot)"
	echo "и firewall-зона оставлены; удалить: rm -rf /etc/wgkeybot /etc/config/wgkeybot"
}

# ══════════════════════════════════════════════════════════════════════════════
# Keenetic (Entware)
# ══════════════════════════════════════════════════════════════════════════════
KEEN_BIN=/opt/sbin/wgkeybot
KEEN_INIT=/opt/etc/init.d/S99wgkeybot
KEEN_HOOK=/opt/etc/ndm/ifstatechanged.d/50-wgkeybot.sh
KEEN_CONF_DIR=/opt/etc/wgkeybot
KEEN_CONF=$KEEN_CONF_DIR/wgkeybot.conf

install_keenetic() {
	detect_goarch() {
		arch=""
		command -v opkg >/dev/null 2>&1 && \
			arch="$(opkg print-architecture 2>/dev/null | awk '{print $2}' | grep -v -e '^all$' -e '^noarch$' | tail -1)"
		[ -n "$arch" ] || arch="$(uname -m)"

		case "$arch" in
			aarch64*) echo "aarch64" ;;
			mipselsf* | mipsel*) echo "mipselsf" ;;
			mipssf* | mips*) err "big-endian MIPS не поддерживается (нет моделей Keenetic 4.x на нём)" ;;
			x86_64*) err "x86_64 не поддерживается: Keenetic на x86 не существует.
     Если это OpenWrt на x86 — установщик должен был определить платформу как
     OpenWrt; проверьте наличие /etc/openwrt_release" ;;
			*) err "неизвестная архитектура: $arch" ;;
		esac
	}

	GOARCH_NAME=$(detect_goarch)
	echo "Платформа: Keenetic (Entware), архитектура: $GOARCH_NAME"

	URL="$(asset_url "wgkeybot-keenetic-$GOARCH_NAME")"
	echo "Скачиваю $URL ..."
	TMP=$(mktemp /opt/tmp/wgkeybot.XXXXXX 2>/dev/null || mktemp)
	trap 'rm -f "$TMP"' EXIT
	download "$URL" "$TMP" || err "не удалось скачать бинарник ($URL)"
	[ -s "$TMP" ] || err "скачан пустой файл"

	[ -x "$KEEN_INIT" ] && "$KEEN_INIT" stop 2>/dev/null || true
	mkdir -p /opt/sbin
	mv "$TMP" "$KEEN_BIN"
	trap - EXIT
	chmod 755 "$KEEN_BIN"

	cat > "$KEEN_INIT" <<'EOF'
#!/bin/sh
# wgkeybot — WireGuard/TURN VPN client for Keenetic.
# Стандартный init-скрипт Entware (/opt/etc/init.d/rc.func).
# Демон сам супервизирует реконнекты; при падении процесса его перезапустит
# ndm-хук 50-wgkeybot.sh (check+start) при ближайшем сетевом событии.

ENABLED=yes
PROCS=wgkeybot
ARGS="run"
PREARGS=""
DESC="WgKeyBot VPN daemon"
PATH=/opt/sbin:/opt/bin:/opt/usr/sbin:/opt/usr/bin:/usr/sbin:/usr/bin:/sbin:/bin

. /opt/etc/init.d/rc.func
EOF
	chmod 755 "$KEEN_INIT"

	mkdir -p /opt/etc/ndm/ifstatechanged.d
	cat > "$KEEN_HOOK" <<'EOF'
#!/bin/sh
# ndm-хук /opt/etc/ndm/ifstatechanged.d/50-wgkeybot.sh
# Вызывается NDM при смене состояния любого интерфейса. Шлёт демону мягкий
# NETCHANGE-пинок (проверка смены WAN, переустановка bypass-маршрутов) —
# события вместо чистого поллинга. Заодно перезапускает демон, если тот упал.
#
# Переменные окружения от NDM: $id, $change ("link"), $connected, $link, $up.

WGKEYBOT=/opt/sbin/wgkeybot
SOCK=/opt/var/run/wgkeybot.sock
INIT=/opt/etc/init.d/S99wgkeybot

# Реагируем только на изменения состояния линка/подключения.
case "$change" in
	link | connected) ;;
	*) exit 0 ;;
esac

# Смена состояния нашего же Wireguard-интерфейса — не повод дёргаться.
case "$id" in
	Wireguard*) exit 0 ;;
esac

if [ -S "$SOCK" ]; then
	"$WGKEYBOT" netchange >/dev/null 2>&1 &
elif [ -x "$INIT" ]; then
	# Демон должен работать, но сокета нет — вероятно, упал. Перезапуск.
	"$INIT" start >/dev/null 2>&1 &
fi

exit 0
EOF
	chmod 755 "$KEEN_HOOK"

	mkdir -p "$KEEN_CONF_DIR"
	chmod 700 "$KEEN_CONF_DIR"
	if [ ! -f "$KEEN_CONF" ]; then
		cat > "$KEEN_CONF" <<'EOF'
# /opt/etc/wgkeybot/wgkeybot.conf — настройки демона wgkeybot.
# Формат: key = value. Строки с # — комментарии. Отсутствующие ключи = дефолты.

# Желаемое имя WG-подключения NDM. Если занято чужим подключением,
# демон возьмёт первый свободный WireguardN (запоминается в state.json).
#wg_iface = Wireguard0

# Фиксированный UDP-порт TURN-прокси (на него указывает endpoint ядерного WG).
# #@wgt:LocalPort из конфига туннеля имеет приоритет.
#proxy_port = 51821

# Адрес, на котором слушает прокси. Если прошивка не примет endpoint на
# loopback — поставьте сюда LAN-IP роутера (например 192.168.1.1).
#listen_addr = 127.0.0.1

# Порт веб-панели на LAN. 0 — выключить панель.
#panel_port = 1441

# Адрес страницы captcha (legacy-fallback, обычно не требуется).
#captcha_listen = 0.0.0.0:8089

# persistent-keepalive пира (секунды).
#keepalive = 25
EOF
		chmod 600 "$KEEN_CONF"
	fi

	"$KEEN_INIT" start || true

	LAN_IP=$(wget -q -O - http://localhost:79/rci/show/interface/Bridge0 2>/dev/null |
		sed -n 's/.*"address": *"\([0-9.]*\)".*/\1/p' | head -1)
	[ -n "$LAN_IP" ] || LAN_IP="<LAN-IP роутера>"

	echo ""
	echo "Готово! wgkeybot установлен и запущен."
	echo ""
	echo "  1. Откройте http://$LAN_IP:1441 и вставьте токен от @wg_key_bot,"
	echo "     либо выполните: wgkeybot import <токен>"
	echo "  2. Состояние: wgkeybot status"
	echo ""
}

uninstall_keenetic() {
	echo "Останавливаю сервис..."
	[ -x "$KEEN_INIT" ] && "$KEEN_INIT" stop 2>/dev/null || true

	# Best-effort: опустить WG-подключение (само подключение остаётся в NDM —
	# удалите его в веб-интерфейсе Keenetic, если оно больше не нужно).
	WG_IFACE=$(sed -n 's/.*"wg_iface"[^"]*"\([^"]*\)".*/\1/p' "$KEEN_CONF_DIR/state.json" 2>/dev/null)
	if [ -n "$WG_IFACE" ]; then
		wget -q -O /dev/null --post-data="[{\"parse\":\"interface $WG_IFACE down\"}]" \
			http://localhost:79/rci/ 2>/dev/null || true
		echo "Интерфейс $WG_IFACE выключен (подключение осталось в NDM — удалите в веб-интерфейсе при желании)."
	fi

	rm -f "$KEEN_BIN" "$KEEN_INIT" "$KEEN_HOOK"
	echo "Файлы удалены. Секреты в $KEEN_CONF_DIR оставлены; удалить: rm -rf $KEEN_CONF_DIR"
}

# ── Диспетчер ────────────────────────────────────────────────────────────────
if [ "$VERSION" = "uninstall" ]; then
	case "$PLATFORM" in
		openwrt)  uninstall_openwrt ;;
		keenetic) uninstall_keenetic ;;
	esac
	exit 0
fi

case "$PLATFORM" in
	openwrt)  install_openwrt ;;
	keenetic) install_keenetic ;;
esac
