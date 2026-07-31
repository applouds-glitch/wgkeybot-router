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
