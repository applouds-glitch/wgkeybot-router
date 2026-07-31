package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Каталог секретов задаётся платформой (см. paths.go): на Keenetic корень
// прошивки read-only и всё стороннее живёт в /opt (Entware), на OpenWrt это
// обычный /etc. Шифрование не используется: файлы защищены правами 0600, а
// доступ к root на роутере уже означает полный контроль.
const (
	configFile = "config.conf"
	stateFile  = "state.json"
)

func configPath() string { return filepath.Join(ConfigDir(), configFile) }
func statePath() string  { return filepath.Join(ConfigDir(), stateFile) }

func ensureDir() error { return os.MkdirAll(ConfigDir(), 0700) }

// State — постоянное состояние демона (секреты + выбор интерфейса).
type State struct {
	// AccessToken — Bearer-токен для /api/v1/config, полученный при init.
	AccessToken string `json:"access_token,omitempty"`
	// WgIface — имя WG-интерфейса, выбранное при первом провижининге.
	// Запоминается, чтобы демон после рестарта управлял тем же подключением,
	// а не занимал новый номер. Используется только Keenetic-бэкендом: на
	// OpenWrt имя интерфейса задано настройкой и не выбирается динамически.
	WgIface string `json:"wg_iface,omitempty"`
}

// SaveConfig сохраняет конфиг тоннеля (plaintext) в <ConfigDir>/config.conf (0600).
func SaveConfig(data []byte) error {
	if err := ensureDir(); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	path := configPath()
	if _, err := os.Stat(path); err == nil {
		os.Rename(path, path+".bak")
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// LoadConfig читает и парсит сохранённый конфиг тоннеля.
func LoadConfig() (*TunnelConfig, error) {
	data, err := os.ReadFile(configPath())
	if err != nil {
		return nil, err
	}
	return ParseConfBytes(data, "wgkeybot")
}

// HasConfig сообщает, существует ли сохранённый конфиг.
func HasConfig() bool {
	_, err := os.Stat(configPath())
	return err == nil
}

// LoadState читает состояние; при отсутствии файла возвращает пустое State.
func LoadState() State {
	var s State
	data, err := os.ReadFile(statePath())
	if err != nil {
		return s
	}
	json.Unmarshal(data, &s)
	return s
}

// SaveState сохраняет состояние в <ConfigDir>/state.json (0600).
func SaveState(s State) error {
	if err := ensureDir(); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(statePath(), data, 0600)
}
