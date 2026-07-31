# WgKeyBot для роутеров

**VPN-клиент WireGuard поверх TURN/DTLS-прокси с авторизацией через VK.**
Один демон, один статический бинарник, никаких зависимостей на роутере.

![OpenWrt 22.03+](https://img.shields.io/badge/OpenWrt-22.03%2B-00B5E2)
![KeeneticOS 4.0+](https://img.shields.io/badge/KeeneticOS-4.0%2B-0E7BC0)
![Go 1.25](https://img.shields.io/badge/Go-1.25-00ADD8)
![Лицензия](https://img.shields.io/badge/%D0%BB%D0%B8%D1%86%D0%B5%D0%BD%D0%B7%D0%B8%D1%8F-proprietary-lightgrey)

Трафик WireGuard идёт не напрямую к серверу, а через TURN-ретранслятор VK: для
сети это выглядит как обычный голосовой звонок.

---

## Быстрый старт

```sh
# 1. Установка — скрипт сам определит платформу и архитектуру
wget -O - https://github.com/applouds-glitch/wgkeybot-router/releases/latest/download/install.sh | sh

# 2. Импорт конфига по токену от @wg_key_bot
wgkeybot import <token>

# 3. Только OpenWrt — включить сервис (на Keenetic он уже запущен установщиком)
uci set wgkeybot.main.enabled=1 && uci commit wgkeybot && /etc/init.d/wgkeybot start

# 4. Проверить
wgkeybot status
```

Либо откройте веб-панель на `http://<LAN-IP роутера>:1441` и вставьте токен там.

> **Нет `wget` с https?** На голом OpenWrt это частый случай:
> `opkg update && opkg install ca-bundle` — или используйте `curl -fsSL … | sh`.

Дальше: [проверить совместимость](#поддерживаемые-версии) ·
[другие способы установки](#установка) · [настройки](#настройки) ·
[что делать, если не работает](#диагностика)

---

## Содержание

- [Поддерживаемые версии](#поддерживаемые-версии)
- [Что ещё нужно](#что-ещё-нужно)
- [Установка](#установка)
  - [Пакетом на OpenWrt](#пакетом-на-openwrt)
  - [Вручную](#вручную)
  - [Обновление и удаление](#обновление-и-удаление)
- [Управление](#управление)
- [Веб-панель и LuCI](#веб-панель-и-luci)
- [Captcha](#captcha)
- [Настройки](#настройки)
- [Как это устроено](#как-это-устроено)
- [Файлы](#файлы)
- [Диагностика](#диагностика)
- [Сборка из исходников](#сборка-из-исходников)
- [Релизы](#релизы)

---

## Поддерживаемые версии

| | Минимум | Рекомендуется |
|---|---|---|
| **OpenWrt** | 22.03 | 23.05 / 24.10 |
| **KeeneticOS** | 4.0 | 4.1 и новее |

<details>
<summary>Почему именно эти версии</summary>

**OpenWrt 22.03** — это версия, в которой firewall перешёл на fw4/nftables.
Порт опирается на него дважды: зона с `option device` (её создаёт установщик и
`uci-defaults` пакета) и подстраховочный nft-masquerade в демоне, который живёт
отдельной таблицей `inet wgkeybot` рядом с `inet fw4`. На 21.02 с fw3/iptables
сам демон запустится — wireguard-go, netlink, procd и UCI там работают, — но NAT
для туннеля не настроится, и его придётся прописывать вручную через `iptables`.
Отдельно на 21.02 порт не проверялся.

**KeeneticOS 4.0** — с неё в прошивке есть встроенный WireGuard-клиент, которым
и управляет демон, и стабилен синтаксис parse-команд RCI. На 3.x порт работать
не будет: управлять нечем. Установщик проверяет доступность RCI
(`http://localhost:79/rci/show/version`) и откажется ставиться, если его нет.

Версии выше минимальных ломающих изменений не вносили; «рекомендуется» — это то,
на чём порт гоняется, а не жёсткое требование.

</details>

## Что ещё нужно

| | OpenWrt | Keenetic |
|---|---|---|
| Доступ | root по SSH | root по SSH |
| Требования | `kmod-tun` (обычно уже стоит), `ca-bundle` для https | установленный [Entware](https://help.keenetic.com/hc/ru/articles/360021214160) |
| Место | ~20 МБ под бинарник | ~20 МБ на накопителе Entware |
| Архитектуры | x86_64, aarch64, armv7, mipsel, mips | mipsel (MT7621), aarch64 (MT7622/EN7581) |

Модели Keenetic на big-endian MIPS не поддерживаются — на 4.x таких нет,
установщик скажет об этом явно.

## Установка

Один установщик на обе платформы: определяет, куда попал, подбирает архитектуру,
качает бинарник из GitHub Releases и разворачивает сервис.

```sh
wget -O - https://github.com/applouds-glitch/wgkeybot-router/releases/latest/download/install.sh | sh
```

Что он раскладывает:

| | OpenWrt | Keenetic |
|---|---|---|
| Бинарник | `/usr/bin/wgkeybot` | `/opt/sbin/wgkeybot` |
| Сервис | procd `/etc/init.d/wgkeybot` | Entware `/opt/etc/init.d/S99wgkeybot` |
| Конфиг настроек | UCI `/etc/config/wgkeybot` | `/opt/etc/wgkeybot/wgkeybot.conf` |
| Дополнительно | firewall-зона `wgkeybot` (NAT) | ndm-хук на смену сети |

### Пакетом на OpenWrt

В релизах есть `.ipk` для aarch64_cortex-a53, mipsel_24kc и x86_64:

```sh
opkg install ./wgkeybot_*.ipk
```

Пакет ставит демон, procd-сервис и UCI-конфиг.

LuCI-приложение в релизы пока не собирается — его исходники лежат в
[`packaging/luci-app-wgkeybot/`](packaging/luci-app-wgkeybot/) и собираются
OpenWrt SDK при необходимости. Оно опционально: встроенная веб-панель работает
и без него.

### Вручную

Если установщик не подходит, достаточно положить бинарник и init-скрипт руками.
Ассеты релиза называются `wgkeybot-<goarch>` для OpenWrt (`amd64`, `arm64`,
`armv7`, `mipsle`, `mips`) и `wgkeybot-keenetic-<арка>` для Keenetic
(`mipselsf`, `aarch64`):

```sh
# OpenWrt, пример для mt7621
wget -O /usr/bin/wgkeybot https://github.com/applouds-glitch/wgkeybot-router/releases/latest/download/wgkeybot-mipsle
chmod 755 /usr/bin/wgkeybot

# Keenetic, тот же чипсет
wget -O /opt/sbin/wgkeybot https://github.com/applouds-glitch/wgkeybot-router/releases/latest/download/wgkeybot-keenetic-mipselsf
chmod 755 /opt/sbin/wgkeybot
```

Тексты init-скриптов, ndm-хука и дефолтных конфигов лежат в [`packaging/`](packaging/)
— это ровно то, что раскладывает `install.sh`.

### Обновление и удаление

```sh
sh install.sh              # обновиться до latest
sh install.sh v0.1.0       # поставить конкретную версию
sh install.sh uninstall    # удалить
```

Установщик не перезаписывает существующий конфиг настроек, поэтому повторный
запуск — штатный способ обновиться: сервис останавливается, бинарник заменяется,
сервис стартует снова. При удалении секреты и настройки остаются на месте.

## Управление

| Команда | Что делает |
|---|---|
| `wgkeybot status` | состояние туннеля |
| `wgkeybot import <token>` | импорт конфига по токену |
| `wgkeybot reload` | обновить конфиг по сохранённому токену |
| `wgkeybot captcha [<token>]` | показать URL ожидающей captcha или отправить токен |
| `wgkeybot up` / `down` | запустить / остановить сервис |

`import` и `reload` применяются на живом демоне — перезапуск не нужен.

## Веб-панель и LuCI

Встроенная панель работает на обеих платформах: статус, импорт конфига,
Reconnect/Reload, ссылка на captcha. Слушает **только LAN-IP** роутера; со
стороны WAN порт закрыт firewall'ом. Отключается настройкой `panel_port = 0`.

На OpenWrt дополнительно есть страница LuCI (*Network → WgKeyBot*) — тонкая
обёртка над тем же демоном.

## Captcha

Основной путь получения credentials (VK Calls) капчу не требует. Если VK всё же
её запросит, демон поднимает на LAN страницу решения и печатает её адрес в
`wgkeybot status`. Откройте адрес с телефона или ПК в той же сети: сначала
пробуется автоматическое решение, при неудаче показывается обычная страница.

## Настройки

<details open>
<summary><b>OpenWrt</b> — UCI <code>/etc/config/wgkeybot</code></summary>

| Опция | По умолчанию | Значение |
|---|---|---|
| `enabled` | `0` | procd запустит демон только при `1` |
| `mode` | `gateway` | `gateway` — весь трафик LAN через туннель; `socks` — локальный SOCKS5 без правки маршрутов |
| `ifname` | `wgkb0` | имя TUN-интерфейса |
| `mtu` | `1280` | MTU туннеля (запас на DTLS/TURN-оверхед) |
| `socks_port` | `1080` | порт SOCKS5 в режиме `socks` |
| `lan` | `lan` | network-интерфейс, чьи клиенты идут в туннель |
| `fwmark` | `0x4b8` | метка сокетов прокси (их трафик уходит мимо туннеля) |
| `table` | `51820` | таблица маршрутизации туннеля |
| `nat` | `1` | подстраховочный nft-masquerade помимо fw4-зоны |
| `panel_port` | `1441` | порт веб-панели; `0` — выключить |
| `captcha_listen` | `0.0.0.0:8089` | адрес страницы captcha |

После правки: `uci commit wgkeybot && /etc/init.d/wgkeybot reload`.

</details>

<details open>
<summary><b>Keenetic</b> — плоский <code>/opt/etc/wgkeybot/wgkeybot.conf</code>, формат <code>key = value</code></summary>

| Ключ | По умолчанию | Значение |
|---|---|---|
| `wg_iface` | `Wireguard0` | желаемое WG-подключение NDM; если занято чужим, берётся первое свободное |
| `proxy_port` | `51821` | UDP-порт прокси, на него смотрит endpoint ядерного WG |
| `listen_addr` | `127.0.0.1` | если прошивка не примет endpoint на loopback — поставьте LAN-IP |
| `panel_port` | `1441` | порт веб-панели; `0` — выключить |
| `keepalive` | `25` | persistent-keepalive пира |
| `captcha_listen` | `0.0.0.0:8089` | адрес страницы captcha |

После правки: `/opt/etc/init.d/S99wgkeybot restart`.

</details>

## Как это устроено

```
LAN-клиент ──► WireGuard ──► TURN-прокси (127.0.0.1) ──► TURN VK ──► сервер
```

Собственный трафик прокси не должен попасть в туннель, который он же и несёт.
Платформы решают это по-разному: OpenWrt метит сокеты прокси через `SO_MARK` и
правилом `ip rule fwmark N lookup main` уводит их на физический WAN; на Keenetic
маршрутизацией распоряжается NDM и fwmark недоступен, поэтому для каждого IP, с
которым говорит прокси, ставится host-маршрут `/32` через WAN.

Демон сам переподнимает туннель: watchdog следит за свежестью
WireGuard-рукопожатия, отдельный детектор ловит «чёрную дыру» на управляющем
канале TURN (pion не возвращает ошибку, когда аллокация умирает под живым
сокетом — только логирует), а смена аплинка отслеживается и хуком, и поллингом.

Подробности — в [ARCHITECTURE.md](ARCHITECTURE.md).

## Файлы

| | OpenWrt | Keenetic |
|---|---|---|
| Бинарник | `/usr/bin/wgkeybot` | `/opt/sbin/wgkeybot` |
| Настройки | `/etc/config/wgkeybot` | `/opt/etc/wgkeybot/wgkeybot.conf` |
| Секреты (0600) | `/etc/wgkeybot/` | `/opt/etc/wgkeybot/` |
| Сервис | `/etc/init.d/wgkeybot` | `/opt/etc/init.d/S99wgkeybot` |
| Журнал | системный (procd) | `/opt/var/log/wgkeybot.log` |
| Сокет управления | `/var/run/wgkeybot.sock` | `/opt/var/run/wgkeybot.sock` |

## Диагностика

<details>
<summary><b>«сервис не запущен»</b></summary>

Демон не отвечает на control-сокет.

- **OpenWrt** — проверьте `enabled=1` и посмотрите `logread -e wgkeybot`.
- **Keenetic** — `/opt/etc/init.d/S99wgkeybot start`, затем
  `tail -f /opt/var/log/wgkeybot.log`.

</details>

<details>
<summary><b>Туннель поднялся, трафика нет</b></summary>

На OpenWrt чаще всего дело в NAT: проверьте, что firewall-зона `wgkeybot`
содержит ваш `ifname`, либо оставьте `option nat 1`.

На Keenetic убедитесь, что у WG-подключения включено участие в выборе
интернет-подключения (`ip global`).

</details>

<details>
<summary><b>Конфиг не обновляется</b></summary>

`wgkeybot reload` требует сохранённого токена. Если его нет, повторите
`wgkeybot import <token>`.

</details>

## Сборка из исходников

Нужен Go 1.25. Бинарники статические (`CGO_ENABLED=0`).

```sh
go build -o wgkeybot .                     # OpenWrt (сборка по умолчанию)
go build -tags keenetic -o wgkeybot .      # Keenetic
```

Кросс-компиляция без тулчейна:

```sh
GOOS=linux GOARCH=mipsle GOMIPS=softfloat CGO_ENABLED=0 go build -tags keenetic -o wgkeybot .
```

## Релизы

Сборку делает GitHub Actions, вручную ничего собирать не нужно. Пуш тега `v*`
запускает [`release.yml`](.github/workflows/release.yml), который кладёт в релиз:

- семь статических бинарников — `wgkeybot-{amd64,arm64,armv7,mipsle,mips}`
  для OpenWrt и `wgkeybot-keenetic-{mipselsf,aarch64}` для Keenetic
  (обе матрицы собираются из одного дерева, различаются только меткой сборки);
- `.ipk` для aarch64_cortex-a53 / mipsel_24kc / x86_64 через OpenWrt SDK;
- сам `install.sh` — на него ссылается команда установки выше.

Версия подставляется из тега в `main.Version` и в `PKG_VERSION` пакета.

Каждый пуш в любую ветку прогоняет [`ci.yml`](.github/workflows/ci.yml): `go vet`
под обеими метками, `gofmt`, кросс-сборка всех пяти арок × двух меток и проверка
по таблице символов, что метки действительно выбрасывают неиспользуемый мост.
