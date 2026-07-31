# Архитектура порта

Документ для тех, кто правит код: устройство, границы между слоями и процедуры,
которые нельзя выполнять по интуиции (синк ядра, build-теги, версии платформ).
Инструкция по установке и настройке — в [README](README.md).

## Обзор

Единый порт wgkeybot на роутеры: **OpenWrt** и **KeeneticOS**. Один репозиторий,
одна копия ядра, два платформенных моста за общим интерфейсом. Заменяет
разошедшиеся `wgkeybot-openwrt` и `wgkeybot-keenetic`.

- Модуль: `github.com/wgkeybot/router`, Go 1.25, Linux only, `CGO_ENABLED=0`.
- **Сборка по умолчанию — OpenWrt**; Keenetic собирается с `-tags keenetic`.
- Целевые арки: `arm64`, `arm GOARM=7`, `mipsle GOMIPS=softfloat`,
  `mips GOMIPS=softfloat`, `amd64`.
- Минимальные версии: **OpenWrt 22.03**, **KeeneticOS 4.0**.

Границы версий не произвольные, и понижать их без правок нельзя:

- **OpenWrt 22.03** — первая с fw4/nftables. От него зависят зона с
  `option device` (установщик и `uci-defaults` пакета) и nft-фолбэк в
  `owrt/firewall.go`, который держит таблицу `inet wgkeybot` рядом с `inet fw4`.
  На fw3/iptables демон запустится, но NAT не настроится. Поддержка 21.02
  означала бы вторую ветку NAT на iptables.
- **KeeneticOS 4.0** — с неё есть встроенный WireGuard-клиент, которым управляет
  `keen/backend.go`, и стабилен синтаксис parse-команд RCI. На 3.x управлять
  нечем.

Различие платформ — в том, кто ведёт WireGuard:

| | OpenWrt | Keenetic |
|---|---|---|
| WireGuard | userspace wireguard-go (без kmod) | встроенный ядерный клиент прошивки |
| Настройка туннеля | UAPI + netlink | RCI `http://localhost:79/rci` (parse-команды) |
| Анти-петля | `SO_MARK` + `ip rule fwmark N lookup main` | host-маршруты `/32` через WAN |
| Настройки | UCI `/etc/config/wgkeybot` | плоский `/opt/etc/wgkeybot/wgkeybot.conf` |
| Сервис | procd | Entware `rc.func` + ndm-хук |
| UI | LuCI + встроенная панель | встроенная панель |
| Режимы | gateway, socks | gateway |

## Build

```sh
go build -trimpath -ldflags="-s -w -X main.Version=$(git describe --tags)" -o wgkeybot .   # OpenWrt
go build -tags keenetic -o wgkeybot .                                                      # Keenetic

go vet ./... && go vet -tags keenetic ./...
```

Кросс-билдить **все** арки перед коммитом кода, работающего с числами из сети
или RCI: 32-битный `int` уже ловил переполнения (`0xffffffff`). CI гоняет все
пять арок под обеими метками.

## Directory Structure

```
main.go              единый CLI + супервизор демона (run/import/reload/status/captcha/up/down/netchange)
pkg/proxy/           ЕДИНСТВЕННАЯ копия ядра TURN/VK/WRAP — см. «Синк ядра»
core/                платформенно-нейтральная логика
  platform.go        интерфейсы Platform и Backend — граница с мостами
  paths.go           файловая раскладка, объявляется платформой при старте
  manager.go         поток подключения, watchdog, network-change
  settings.go        Settings (объединение полей обеих платформ) + разбор
  config.go          .conf + #@wgt: парсер, BuildWGUAPIConfig, keyToHex
  api.go             InitFromToken / FetchConfig (key.shadowgate.online)
  store.go           секреты config.conf/state.json (0600)
  control.go         unix-сокет (STATUS/CAPTCHA/SOLVE/RELOAD/RECONNECT/NETCHANGE)
  captcha.go         headless reverse-proxy captcha (LAN-served)
  panel.go           веб-панель на LAN поверх того же Controller
  logging.go         файл+syslog+ротация (включается платформой)
keen/                мост Keenetic
  rci.go             HTTP-клиент RCI; WAN/LAN-детект
  wg.go              провижининг WireguardN (идемпотентно, выбор свободного iface)
  routes.go          host-маршруты /32 до TURN-IP через WAN
  dns.go             SystemDNS() из RCI show ip name-server
  platform.go        core.Platform: пути /opt, плоский conf, RCI
  backend.go         core.Backend: ядерный WG через RCI
owrt/                мост OpenWrt
  routing.go         netlink: policy-routing, LAN-подсети, default GW
  ifconfig.go        netlink: адрес/MTU/up для TUN
  firewall.go        nft masquerade fallback (private inet wgkeybot)
  socks.go           SOCKS5-сервер (stdlib-only)
  dns.go             SystemDNS() из /tmp/resolv.conf.d/*
  uci.go             чтение UCI (uci-команда, фоллбэк на разбор файла)
  platform.go        core.Platform: пути /etc, UCI
  backend.go         core.Backend: wireguard-go + netlink + nft
platform/            селектор моста по build-тегам (два файла, см. ниже)
packaging/           keenetic/ (Entware), openwrt/wgkeybot/ (ipk), luci-app-wgkeybot/
install.sh           единый установщик: детект платформы → детект арки → раскладка
```

## Правило зависимостей

`core` → `pkg/proxy`. Мосты `keen`/`owrt` → `core`. `platform` → все. Цикла нет,
и **`core` никогда не импортирует мосты**: если понадобилось — значит, логика
не нейтральна и ей место в `Backend`.

## Build-теги

Тегируются ровно два файла: `platform/openwrt.go` (`//go:build !keenetic`) и
`platform/keenetic.go` (`//go:build keenetic`). Сами мосты не тегированы, поэтому

- `go vet ./...` **без тегов** проверяет типы обеих платформ;
- в бинарь попадает только выбранный мост — второй не входит в граф импортов и
  выбрасывается линковщиком вместе с зависимостями.

Второе свойство защищено проверкой в CI (шаг «Build tags must drop the unused
bridge»): в keenetic-сборке должно быть ноль символов `vishvananda` и
`router/owrt.`, в openwrt-сборке — ноль символов `router/keen.`. Если проверка
упала, значит `core` или `main` начал тянуть мост напрямую.

## Connection Flow (core.Manager.Connect)

Общий для обеих платформ; различия целиком в трёх методах `Backend`.

1. `plat.SystemDNS()` → `proxy.Config.SystemDNS` (резолв VK/TURN до туннеля).
2. `listenAddr()`: порт из `#@wgt:LocalPort` → `Settings.ProxyPort` → `0` (авто).
3. `backend.Prepare(TurnIP)` — **до** `proxy.NewTunnel`, пока не отправлено ни
   одного пакета. owrt ставит fwmark; keen определяет WAN и открывает обход.
4. `proxy.NewTunnel` + `StartBootstrap()`.
5. `WaitReady`; на `CaptchaRequired` → `handleCaptcha` (LAN reverse-proxy).
6. `backend.Bypass(ActiveTURNAddrs + AuthBypassIPs)` — **до** подъёма WG, иначе
   ре-фетч credentials уйдёт в туннель, которому эти credentials и нужны.
7. `backend.Up(t, listenAddr)`.
8. Коммит; `StartWatchdog` + `netPoller`.

`Prepare`/`Bypass` разделены не косметически: fwmark на OpenWrt покрывает все
сокеты прокси разом, а на Keenetic маршрут нужен на каждый IP, причём часть
адресов известна только после bootstrap.

## Синк ядра (pkg/proxy)

Ядро — не собственный код этого репозитория, а копия. Новая логика TURN/VK/WRAP
появляется первой в Android-клиенте, остальные порты синкаются от него; ссылку
на источник и текущий коммит спрашивайте у мейнтейнера.

Порядок синка:

1. `diff` против коммита, с которого снята текущая копия.
2. Перенести **только новую логику**, сохранив чисто-Go замену cgo. В эталоне
   `import "C"` есть в `turn-client.go`, `dns.go` (там он называется
   `turn-dns-resolver.go`), `vk.go`, `vk_captcha.go`; здесь их роль играют
   `logging.go` (`turnLog`) и `protect_linux.go`/`protect_other.go`
   (`protectControl`, `protectAndDial`).
3. Не забыть переписать import path `srtpwrap` на этот модуль.

Постоянные отличия этой копии от эталона — их **не надо «чинить»** при синке:

| Файл | Отличие |
|---|---|
| `turn-client.go` | без cgo-блока (`wgProtectSocket`, `getNetworkDnsServers`, `AndroidLogger`) |
| `dns.go`, `vk.go`, `vk_captcha.go` | без cgo-блока |
| `pion_log.go` | одна фраза в комментарии: про файл журнала вместо «Android discards stderr» |
| `tunnel.go`, `logging.go`, `captcha_host.go`, `protect_*.go`, `iface_linux.go`, `transient_*.go` | обвязка роутерного порта, в эталоне их нет |

JNI-поверхность Android-приложения не переносится — её роль здесь играют
`pkg/proxy/tunnel.go` и `core/api.go`.

**Логику `pkg/proxy` не менять локально.** Правка нужна — её место в эталоне,
иначе следующий синк её затрёт. Допустимо только добавлять `*_linux.go`/
`*_other.go` шимы.

## Conventions

- Демон требует root (`run` отказывается от non-root: файлы конфигурации, сеть).
- Новая настройка: поле в `core.Settings` + ветка в `applySetting` + граница в
  `normalize`. Ключи платформ не пересекаются, второй парсер не нужен.
- Точки, требующие сверки на железе, помечены `VERIFY-ON-DEVICE` (формат
  `show interface` WG-секции, синтаксис parse-команд провижининга).

## Release

Тег `v*` → `release.yml`:

1. статические бинарники — `wgkeybot-<goarch>` (OpenWrt) и
   `wgkeybot-keenetic-<asset>` (Keenetic). Имена сохранены от прежних
   раздельных репозиториев, чтобы разошедшиеся install.sh продолжали работать;
2. `.ipk` через `openwrt/gh-action-sdk` для aarch64_cortex-a53 / mipsel_24kc /
   x86_64 (только OpenWrt; на Keenetic ставится install.sh);
3. `install.sh` в ассеты.

`PKG_VERSION` в `packaging/openwrt/wgkeybot/Makefile` синхронизируется с тегом в CI.
