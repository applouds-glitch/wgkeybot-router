//go:build !keenetic

// Package platform выбирает платформенный мост на этапе компиляции.
//
// Тегируются только файлы этого пакета — сами мосты keen/ и owrt/ остаются
// нетегированными, поэтому `go vet ./...` и `go test ./...` без тегов
// проверяют типы обеих платформ сразу. В бинарь при этом попадает только
// выбранный мост: второй не входит в граф импортов, и линковщик выбрасывает
// его вместе с зависимостями (на Keenetic-сборке так уходит netlink).
//
// Сборка по умолчанию — OpenWrt; Keenetic собирается с `-tags keenetic`.
package platform

import (
	"github.com/wgkeybot/router/core"
	"github.com/wgkeybot/router/owrt"
)

// Target — платформа этой сборки, для usage и сообщений об ошибках.
const Target = "OpenWrt"

// New создаёт платформенный мост.
func New() (core.Platform, error) { return owrt.New() }
