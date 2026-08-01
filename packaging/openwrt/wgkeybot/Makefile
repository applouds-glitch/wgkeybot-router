# OpenWrt package Makefile for wgkeybot.
#
# Сборка через OpenWrt SDK: репозиторий подключается как feed, дальше
#   ./scripts/feeds update -a && ./scripts/feeds install wgkeybot
#   make package/wgkeybot/compile V=s
# Ровно это и делает openwrt/gh-action-sdk в release.yml.

include $(TOPDIR)/rules.mk

PKG_NAME:=wgkeybot
PKG_VERSION:=0.1.0
PKG_RELEASE:=1

PKG_LICENSE:=Proprietary
PKG_LICENSE_FILES:=LICENSE
PKG_MAINTAINER:=WgKeyBot

PKG_BUILD_DEPENDS:=golang/host
PKG_BUILD_PARALLEL:=1

# Обязательно для mips: golang-package.mk собирает с CGO_ENABLED=1, а ассемблер
# runtime/cgo (gcc_mipsx.S) написан обычными MIPS32-инструкциями и в режиме
# MIPS16 не собирается — «Error: invalid operands `sw $18,12($29)'».
# Устаревший PKG_USE_MIPS16:=0 тут не работает: этой переменной в package.mk
# больше нет, -mips16 снимается только через PKG_BUILD_FLAGS.
PKG_BUILD_FLAGS:=no-mips16

GO_PKG:=github.com/wgkeybot/router
GO_PKG_LDFLAGS:=-s -w
GO_PKG_LDFLAGS_X:=main.Version=$(PKG_VERSION)

# Обязательно. Без этого golang-build.sh переносит в GOPATH только файлы с
# расширениями .c/.cc/.cpp/.go/.h/.hh/.hpp/.proto/.s, а pkg/proxy/vk_certs.go
# держит //go:embed на certs/*.pem и certs/*.cer — до компилятора они не
# доезжают, и сборка падает на «pattern certs/...: no matching files found».
GO_PKG_INSTALL_ALL:=1

include $(INCLUDE_DIR)/package.mk
include $(TOPDIR)/feeds/packages/lang/golang/golang-package.mk

# Исходники берём из самого репозитория, а не скачиваем по сети.
#
# Раньше здесь стоял PKG_SOURCE_PROTO:=git с PKG_SOURCE_URL на GitHub, и сборка
# падала на каждой арке: репозиторий приватный, а в контейнере SDK нет учётных
# данных, поэтому анонимный clone получал отказ (зеркала OpenWrt, само собой,
# отвечали 404). Скачивание тут и не нужно: код уже лежит рядом — feed
# подключён к корню репозитория, а make заходит в реальный каталог пакета
# (`Entering directory '/feed/packaging/openwrt/wgkeybot'`), так что до корня
# ровно три уровня вверх. Заодно пакет теперь гарантированно собирается из того
# же коммита, что и остальной релиз, а не из того, что удалось скачать.
REPO_ROOT:=$(CURDIR)/../../..

define Build/Prepare
	mkdir -p $(PKG_BUILD_DIR)
	$(CP) $(REPO_ROOT)/go.mod $(REPO_ROOT)/go.sum $(REPO_ROOT)/main.go $(REPO_ROOT)/LICENSE $(PKG_BUILD_DIR)/
	$(CP) $(REPO_ROOT)/core $(REPO_ROOT)/keen $(REPO_ROOT)/owrt $(REPO_ROOT)/platform $(REPO_ROOT)/pkg $(PKG_BUILD_DIR)/
endef

define Package/wgkeybot
  SECTION:=net
  CATEGORY:=Network
  SUBMENU:=VPN
  TITLE:=WgKeyBot WireGuard/TURN VPN client
  URL:=https://github.com/applouds-glitch/wgkeybot-router
  DEPENDS:=$(GO_ARCH_DEPENDS) +kmod-tun
endef

define Package/wgkeybot/description
  Userspace WireGuard VPN client with a TURN/DTLS proxy and VK authentication.
  Headless daemon with UCI config and procd service. Gateway mode routes all LAN
  clients through the tunnel; SOCKS mode exposes a local SOCKS5 proxy without
  touching system routing. Built from the shared wgkeybot-router tree, which
  also targets Keenetic — this package is the default (untagged) build.
endef

define Package/wgkeybot/conffiles
/etc/config/wgkeybot
endef

define Package/wgkeybot/install
	$(call GoPackage/Package/Install/Bin,$(1))

	$(INSTALL_DIR) $(1)/etc/init.d
	$(INSTALL_BIN) ./files/wgkeybot.init $(1)/etc/init.d/wgkeybot

	$(INSTALL_DIR) $(1)/etc/config
	$(INSTALL_CONF) ./files/wgkeybot.config $(1)/etc/config/wgkeybot

	$(INSTALL_DIR) $(1)/etc/uci-defaults
	$(INSTALL_BIN) ./files/wgkeybot.defaults $(1)/etc/uci-defaults/99-wgkeybot
endef

$(eval $(call GoBinPackage,wgkeybot))
$(eval $(call BuildPackage,wgkeybot))
