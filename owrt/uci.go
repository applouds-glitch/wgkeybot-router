package owrt

import (
	"bufio"
	"os"
	"os/exec"
	"strings"
)

// uci.go — чтение конфигурации OpenWrt. Только источник пар ключ-значение:
// разбор и валидацию значений делает core (см. core/settings.go), поэтому
// добавление новой опции не требует правок здесь.

// SettingsPath — UCI-файл настроек демона.
const SettingsPath = "/etc/config/wgkeybot"

// readUCI возвращает опции секции wgkeybot.main. Основной источник — команда
// `uci` (канонический парсер OpenWrt, учитывает overrides); при её отсутствии
// (или вне OpenWrt) — прямой разбор файла.
func readUCI() map[string]string {
	if kv, ok := readUCICommand(); ok {
		return kv
	}
	return readUCIFile(SettingsPath)
}

// uciGet возвращает значение произвольного UCI-ключа (например
// "network.lan.device") через `uci -q get`, либо "" если uci недоступен/пусто.
func uciGet(key string) string {
	if _, err := exec.LookPath("uci"); err != nil {
		return ""
	}
	out, err := exec.Command("uci", "-q", "get", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// readUCICommand читает опции через `uci -q show wgkeybot.main`.
// Формат строк: wgkeybot.main.<option>='<value>'.
func readUCICommand() (map[string]string, bool) {
	if _, err := exec.LookPath("uci"); err != nil {
		return nil, false
	}
	out, err := exec.Command("uci", "-q", "show", "wgkeybot.main").Output()
	if err != nil {
		return nil, false
	}
	kv := make(map[string]string)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		// key = wgkeybot.main.<option>
		parts := strings.Split(key, ".")
		if len(parts) != 3 {
			continue
		}
		kv[parts[2]] = strings.Trim(val, "'")
	}
	return kv, true
}

// readUCIFile — запасной разбор UCI-файла без бинарника uci. Поддерживает
// минимальный синтаксис: `option <name> '<value>'` внутри `config wgkeybot 'main'`.
func readUCIFile(path string) map[string]string {
	kv := make(map[string]string)
	f, err := os.Open(path)
	if err != nil {
		return kv
	}
	defer f.Close()

	inMain := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "config ") {
			inMain = strings.Contains(line, "wgkeybot") || strings.Contains(line, "'main'") || strings.Contains(line, "\"main\"")
			continue
		}
		if !inMain || !strings.HasPrefix(line, "option ") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, "option "))
		name, val, ok := strings.Cut(rest, " ")
		if !ok {
			continue
		}
		kv[name] = strings.Trim(strings.TrimSpace(val), "'\"")
	}
	return kv
}
