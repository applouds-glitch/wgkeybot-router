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

# Опечатка в аргументе иначе молча превратилась бы в 404 при скачивании.
case "$VERSION" in
	latest | uninstall | v[0-9]*) ;;
	*) err "непонятный аргумент '$VERSION'.
     Ожидается тег вида v0.1.0, 'latest' (по умолчанию) или 'uninstall'." ;;
esac

# ── Определение платформы ────────────────────────────────────────────────────
# OpenWrt узнаётся по /etc/openwrt_release. Keenetic — по каталогу Entware плюс
# ответу локального RCI прошивки: одного /opt мало, Entware ставят и на другие
# прошивки, а без RCI демону всё равно не с чем работать.
#
# Автоопределение промахивается на кастомных сборках и когда веб-интерфейс
# Keenetic выключен, поэтому неудача не фатальна: платформу можно задать
# переменной WGKEYBOT_PLATFORM или выбрать из меню. Причина возвращается строкой
# с "!" в начале — присвоить её глобальной переменной нельзя, функция вызывается
# в подоболочке $( ).
detect_platform() {
	[ -f /etc/openwrt_release ] && { echo openwrt; return; }
	if [ -d /opt/etc/init.d ]; then
		if wget -q -T 3 -O /dev/null http://127.0.0.1:79/rci/show/version 2>/dev/null; then
			echo keenetic; return
		fi
		echo "!найден Entware (/opt/etc/init.d), но локальный RCI (http://127.0.0.1:79) не отвечает — это не KeeneticOS либо веб-интерфейс прошивки отключён"
		return
	fi
	echo "!нет ни /etc/openwrt_release (OpenWrt), ни /opt/etc/init.d (Entware на Keenetic)"
}

# choose_platform выставляет $PLATFORM по ответу пользователя. $1 — причина, по
# которой не сработало автоопределение.
choose_platform() {
	echo "Не удалось определить платформу автоматически:"
	echo "  $1"
	echo ""

	# Скрипт обычно запускают как `wget -O - … | sh`, и на stdin висит его же
	# текст — спрашивать оттуда нельзя, читаем с терминала. Нет терминала
	# (cron, CI, docker без -t) — остаётся только переменная. Проверяем именно
	# открытием: без управляющего терминала /dev/tty существует и проходит
	# `test -r`, но open даёт ENXIO. Открываем в подоболочке: неудачный редирект
	# составной команды dash/ash считают фатальным и молча убивают весь скрипт,
	# а падение подоболочки — это просто ненулевой код возврата.
	if ! ( : < /dev/tty ) 2>/dev/null; then
		err "терминал недоступен, выбрать платформу не у кого.
     Укажите её явно: WGKEYBOT_PLATFORM=openwrt sh install.sh
     Допустимые значения: openwrt, keenetic"
	fi

	while :; do
		echo "Выберите платформу вручную:"
		echo "  1) OpenWrt  — UCI и procd, бинарник в /usr/bin"
		echo "  2) Keenetic — KeeneticOS с Entware, бинарник в /opt/sbin"
		echo "  3) Отмена"
		printf "Номер [1-3]: "
		if ! read -r ans < /dev/tty; then
			err "ввод оборван. Укажите платформу явно:
     WGKEYBOT_PLATFORM=openwrt sh install.sh"
		fi
		case "$ans" in
			1) PLATFORM=openwrt; break ;;
			2) PLATFORM=keenetic; break ;;
			3 | q | Q) err "установка отменена" ;;
			*) echo "Нужно ввести 1, 2 или 3."; echo "" ;;
		esac
	done
	echo "Выбрано вручную: $PLATFORM. Если выбор неверен — снесите установку"
	echo "командой 'sh install.sh uninstall'."
	echo ""
}

if [ -n "${WGKEYBOT_PLATFORM:-}" ]; then
	PLATFORM="$WGKEYBOT_PLATFORM"
	case "$PLATFORM" in
		openwrt | keenetic) echo "Платформа задана вручную: $PLATFORM" ;;
		*) err "WGKEYBOT_PLATFORM=$PLATFORM — допустимо только openwrt или keenetic" ;;
	esac
else
	PLATFORM="$(detect_platform)"
	case "$PLATFORM" in
		"!"*) choose_platform "${PLATFORM#!}" ;;
	esac
fi

have() { command -v "$1" >/dev/null 2>&1; }

download() {
	# $1 url, $2 dest. Вызывается только слева от `||`, поэтому set -e внутри
	# не срабатывает и неудача одной качалки честно передаёт ход следующей.
	have uclient-fetch && uclient-fetch -O "$2" "$1" && return 0
	have wget && wget -q -O "$2" "$1" && return 0
	have curl && curl -fsL -o "$2" "$1" && return 0
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

file_size() { wc -c < "$1" 2>/dev/null | tr -d ' '; }

# elf_arch печатает GOARCH по заголовку ELF или "" если это не ELF. Разбираем
# файл, а не запускаем его: так одинаково ловятся HTML-заглушка провайдера,
# тело 404, обрыв закачки и бинарник не той арки — причём до того, как что-то
# установлено, и без оглядки на noexec у /tmp.
elf_arch() {
	[ "$(od -An -N4 -tx1 "$1" 2>/dev/null | tr -d ' \n')" = "7f454c46" ] || return 0
	class=$(od -An -j4 -N1 -tu1 "$1" | tr -d ' ')  # 1 = 32 бита, 2 = 64
	data=$(od -An -j5 -N1 -tu1 "$1" | tr -d ' ')   # 1 = little-endian, 2 = big
	# e_machine — два байта по смещению 18, порядок как у data.
	set -- $(od -An -j18 -N2 -tu1 "$1")
	if [ "$data" = "1" ]; then mach=$(( $1 + $2 * 256 )); else mach=$(( $1 * 256 + $2 )); fi
	case "$mach:$class:$data" in
		62:2:1)  echo amd64 ;;   # EM_X86_64
		183:2:1) echo arm64 ;;   # EM_AARCH64
		40:1:1)  echo armv7 ;;   # EM_ARM
		8:1:1)   echo mipsle ;;  # EM_MIPS, LE
		8:1:2)   echo mips ;;    # EM_MIPS, BE
		*)       echo "ELF (машина $mach, class $class, data $data)" ;;
	esac
}

check_binary() {
	# $1 файл, $2 ожидаемый GOARCH
	got="$(elf_arch "$1")"
	[ -n "$got" ] || err "скачан не бинарник, а $(file_size "$1") байт мусора.
     Так выглядит страница 404 (неверная версия?) или заглушка провайдера.
     Проверьте URL руками: $URL"
	[ "$got" = "$2" ] || err "скачан бинарник для '$got', а роутеру нужен '$2'.
     Похоже на ошибку в ассетах релиза — сообщите о ней."
}

# verify_sha256 сверяет файл с <asset>.sha256 из того же релиза. Отсутствие
# суммы или sha256sum — предупреждение, а не отказ: старые релизы сумм не
# публиковали, а на Entware без coreutils считать нечем.
verify_sha256() {
	# $1 файл, $2 url бинарника
	have sha256sum || { echo "ВНИМАНИЕ: нет sha256sum, контрольная сумма не проверена" >&2; return 0; }
	sumf="$1.sha256"
	if ! download "$2.sha256" "$sumf" || [ ! -s "$sumf" ]; then
		rm -f "$sumf"
		echo "ВНИМАНИЕ: $2.sha256 недоступен, контрольная сумма не проверена" >&2
		return 0
	fi
	want="$(awk '{print $1; exit}' "$sumf")"
	got="$(sha256sum "$1" | awk '{print $1}')"
	rm -f "$sumf"
	[ "$want" = "$got" ] || err "контрольная сумма не совпала.
     ожидалось: $want
     получено:  $got
     Файл побился при закачке или подменён — установка прервана."
	echo "Контрольная сумма sha256 совпала."
}

# need_space отказывается ставить бинарник, если на разделе назначения не
# хватает места: на роутерах с маленьким overlay это самый частый исход, и
# обрубленный файл лучше поймать до записи.
need_space() {
	# $1 каталог назначения, $2 нужно байт
	have df || return 0
	free_kb="$(df -k "$1" 2>/dev/null | awk 'NR>1 {print $4; exit}')"
	case "$free_kb" in "" | *[!0-9]*) return 0 ;; esac
	need_kb=$(( $2 / 1024 + 512 ))
	[ "$free_kb" -ge "$need_kb" ] || err "мало места в $1: свободно ${free_kb} КБ, нужно ~${need_kb} КБ.
     Освободите флеш или разместите бинарник на USB-накопителе."
}

# ══════════════════════════════════════════════════════════════════════════════
# OpenWrt
# ══════════════════════════════════════════════════════════════════════════════

# daemon_running — работает ли сейчас демон; pidfile пишет procd
# (procd_set_param pidfile). Ответ нужен только чтобы решить, перезапускать ли
# сервис после обновления: сама подмена бинарника от него не зависит. Через
# pgrep -f не проверяем — он ловит собственную командную строку установщика.
daemon_running() {
	pid=""
	[ -f /var/run/wgkeybot.pid ] && pid="$(cat /var/run/wgkeybot.pid 2>/dev/null)"
	[ -n "$pid" ] && kill -0 "$pid" 2>/dev/null
}

install_openwrt() {
	# Поддерживаются только арки, актуальные в 2026-м и вывозящие двойное
	# шифрование (TURN/DTLS-прокси + wireguard-go на каждый пакет). Отсев делаем
	# явно: раньше все `arm_*` считались armv7, и на ARMv5-таргетах (kirkwood,
	# at91) роутер молча получал бинарник, падающий с illegal instruction.
	# Строки начинаются с "!" — маркер «арка распознана, но не поддерживается».
	UNSUP_ARM="!ARMv5/ARMv6 не поддерживается: сборок под него нет, и на двойное шифрование этих SoC всё равно не хватает. Нужен ARMv7+, aarch64 или x86_64."
	UNSUP_MIPS="!big-endian MIPS (ath79 и подобные) не поддерживается: сборок под него нет, и одноядерных SoC этого класса на двойное шифрование не хватает. Из MIPS поддерживается little-endian (mipsel, MT7621 и новее)."

	detect_goarch() {
		arch=""
		[ -f /etc/openwrt_release ] && . /etc/openwrt_release
		arch="$DISTRIB_ARCH"
		# Фолбэки на случай пустого DISTRIB_ARCH: apk (25.12+), opkg (24.10 и
		# старше), дальше — uname.
		[ -n "$arch" ] || arch="$(apk --print-arch 2>/dev/null)"
		[ -n "$arch" ] || arch="$(opkg print-architecture 2>/dev/null | awk '{print $2}' | grep -v -e '^all$' -e '^noarch$' | tail -1)"

		case "$arch" in
			x86_64*)        echo "amd64" ;;
			aarch64*)       echo "arm64" ;;
			# ARMv5/ARMv6-таргеты OpenWrt: kirkwood, at91, mxs, oxnas.
			arm_xscale* | arm_arm926* | arm_fa526* | arm_mpcore*)
			                echo "$UNSUP_ARM" ;;
			arm_* | armv7*) echo "armv7" ;;
			mipsel_*)       echo "mipsle" ;;
			mips_* | mips64_*) echo "$UNSUP_MIPS" ;;
			*)
				# Запасной путь через uname + endianness.
				case "$(uname -m)" in
					x86_64) echo "amd64" ;;
					aarch64) echo "arm64" ;;
					armv7l|armv7) echo "armv7" ;;
					armv5*|armv6*) echo "$UNSUP_ARM" ;;
					mips|mips64)
						# Байт 5 ELF-заголовка: 2 — big-endian. Неизвестный
						# ответ трактуем как mipsel (ходовой случай).
						b=$(od -An -j5 -N1 -tu1 /bin/busybox 2>/dev/null | tr -d ' ')
						[ "$b" = "2" ] && echo "$UNSUP_MIPS" || echo "mipsle" ;;
					*) echo "" ;;
				esac ;;
		esac
	}

	GOARCH="$(detect_goarch)"
	case "$GOARCH" in
		"") err "не удалось определить архитектуру (DISTRIB_ARCH=$DISTRIB_ARCH, uname=$(uname -m))" ;;
		"!"*) err "${GOARCH#!}" ;;
	esac
	echo "Платформа: OpenWrt, архитектура: $GOARCH"

	URL="$(asset_url "wgkeybot-$GOARCH")"
	echo "Загрузка $URL ..."
	# mktemp, а не /tmp/wgkeybot.$$: имя от PID предсказуемо, а скрипт работает
	# от root — подсунутый симлинк писал бы куда угодно.
	TMP="$(mktemp /tmp/wgkeybot.XXXXXX)" || err "не удалось создать временный файл в /tmp"
	trap 'rm -f "$TMP" "$TMP.sha256"' EXIT INT TERM
	download "$URL" "$TMP" || err "не удалось скачать $URL
     Проверьте сеть и что такая версия существует; для https нужен ca-bundle."
	[ -s "$TMP" ] || err "скачан пустой файл ($URL)"
	check_binary "$TMP" "$GOARCH"
	verify_sha256 "$TMP" "$URL"
	need_space /usr/bin "$(file_size "$TMP")"

	WAS_RUNNING=0
	daemon_running && WAS_RUNNING=1

	# Ставим рядом и переименовываем. `install` поверх работающего бинарника
	# отрабатывает (busybox снимает ETXTBSY через unlink+create), но в этот
	# момент файла на диске нет вовсе: обрыв питания оставит роутер без
	# /usr/bin/wgkeybot. rename атомарен и запущенному демону не мешает — тот
	# доживает на старом inode, поэтому сервис ниже перезапускаем сами.
	trap 'rm -f "$TMP" "$TMP.sha256" /usr/bin/wgkeybot.new' EXIT INT TERM
	install -m 0755 "$TMP" /usr/bin/wgkeybot.new
	mv -f /usr/bin/wgkeybot.new /usr/bin/wgkeybot
	rm -f "$TMP"; trap - EXIT INT TERM
	VER_OUT="$(/usr/bin/wgkeybot version 2>&1)" || err "бинарник установлен, но не запускается:
     $VER_OUT
     Заголовок ELF и контрольная сумма сошлись, так что дело не в закачке —
     повторите установку и приложите этот вывод к отчёту об ошибке."
	echo "Установлен /usr/bin/wgkeybot ($VER_OUT)"

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

	# Демону нужен TUN. Модуль может быть просто не загружен — пробуем, и лишь
	# потом жалуемся: ставить пакеты за пользователя установщик не берётся.
	[ -c /dev/net/tun ] || modprobe tun >/dev/null 2>&1 || true
	if [ ! -c /dev/net/tun ]; then
		echo ""
		echo "ВНИМАНИЕ: нет /dev/net/tun — без модуля tun демон не поднимет туннель."
		if have apk; then
			echo "  Поставьте: apk add kmod-tun"
		else
			echo "  Поставьте: opkg update && opkg install kmod-tun"
		fi
	fi

	# Демон работал до обновления — он всё ещё крутит старый inode, поднимаем
	# заново уже с новым бинарником.
	if [ "$WAS_RUNNING" = "1" ]; then
		/etc/init.d/wgkeybot restart >/dev/null 2>&1 || true
		echo ""
		echo "Сервис перезапущен с новым бинарником."
	fi

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
	# Как и на OpenWrt: "!" в начале — арка распознана, но не поддерживается.
	# err здесь звать нельзя, функция работает в подоболочке $( ) и выход из неё
	# скрипт не остановит.
	detect_goarch() {
		arch=""
		have opkg && \
			arch="$(opkg print-architecture 2>/dev/null | awk '{print $2}' | grep -v -e '^all$' -e '^noarch$' | tail -1)"
		[ -n "$arch" ] || arch="$(uname -m)"

		case "$arch" in
			aarch64*) echo "aarch64" ;;
			mipselsf* | mipsel*) echo "mipselsf" ;;
			mipssf* | mips*) echo "!big-endian MIPS не поддерживается: моделей Keenetic на нём нет" ;;
			x86_64*) echo "!x86_64 не поддерживается: Keenetic на x86 не существует. Если это OpenWrt на x86 — проверьте наличие /etc/openwrt_release либо укажите платформу через WGKEYBOT_PLATFORM=openwrt" ;;
			*) echo "!неизвестная архитектура: $arch" ;;
		esac
	}

	GOARCH_NAME="$(detect_goarch)"
	case "$GOARCH_NAME" in
		"") err "не удалось определить архитектуру (uname=$(uname -m))" ;;
		"!"*) err "${GOARCH_NAME#!}" ;;
	esac
	echo "Платформа: Keenetic (Entware), архитектура: $GOARCH_NAME"

	# Ожидаемая машина ELF для проверки скачанного.
	case "$GOARCH_NAME" in
		aarch64) WANT_ELF=arm64 ;;
		*) WANT_ELF=mipsle ;;
	esac

	URL="$(asset_url "wgkeybot-keenetic-$GOARCH_NAME")"
	echo "Скачиваю $URL ..."
	mkdir -p /opt/sbin /opt/tmp
	TMP="$(mktemp /opt/tmp/wgkeybot.XXXXXX 2>/dev/null || mktemp)" || err "не удалось создать временный файл"
	trap 'rm -f "$TMP" "$TMP.sha256"' EXIT INT TERM
	download "$URL" "$TMP" || err "не удалось скачать $URL
     Проверьте сеть и что такая версия существует; для https нужен ca-bundle
     (opkg install ca-bundle ca-certificates)."
	[ -s "$TMP" ] || err "скачан пустой файл ($URL)"
	check_binary "$TMP" "$WANT_ELF"
	verify_sha256 "$TMP" "$URL"
	need_space /opt/sbin "$(file_size "$TMP")"

	[ -x "$KEEN_INIT" ] && "$KEEN_INIT" stop 2>/dev/null || true
	# Права выставляем до подмены, чтобы файл ни секунды не лежал по месту с
	# чужими правами. mktemp выше берёт /opt/tmp — тот же раздел, что /opt/sbin,
	# так что mv здесь атомарный rename.
	chmod 755 "$TMP"
	mv -f "$TMP" "$KEEN_BIN"
	trap - EXIT INT TERM
	VER_OUT="$("$KEEN_BIN" version 2>&1)" || err "бинарник установлен, но не запускается:
     $VER_OUT"
	echo "Установлен $KEEN_BIN ($VER_OUT)"

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

	LAN_IP=$(wget -q -O - http://127.0.0.1:79/rci/show/interface/Bridge0 2>/dev/null |
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
			http://127.0.0.1:79/rci/ 2>/dev/null || true
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
