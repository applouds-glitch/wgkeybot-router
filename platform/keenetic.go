//go:build keenetic

package platform

import (
	"github.com/wgkeybot/router/core"
	"github.com/wgkeybot/router/keen"
)

// Target — платформа этой сборки, для usage и сообщений об ошибках.
const Target = "Keenetic (Entware)"

// New создаёт платформенный мост.
func New() (core.Platform, error) { return keen.New() }
