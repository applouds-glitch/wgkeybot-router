# LuCI app for wgkeybot.
#
# Сборка через OpenWrt SDK с установленным luci-feed:
#   ./scripts/feeds install luci-base
#   make package/luci-app-wgkeybot/compile V=s
#
# Поместите каталог как package/luci-app-wgkeybot/ (или в свой feed).

include $(TOPDIR)/rules.mk

LUCI_TITLE:=LuCI support for WgKeyBot
LUCI_DEPENDS:=+wgkeybot
LUCI_PKGARCH:=all

PKG_LICENSE:=Proprietary
PKG_MAINTAINER:=WgKeyBot

include $(TOPDIR)/feeds/luci/luci.mk

# call BuildPackage - OpenWrt buildroot signature
