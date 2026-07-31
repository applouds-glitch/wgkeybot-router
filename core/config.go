package core

import (
	"bufio"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/wgkeybot/router/pkg/proxy"
)

// TunnelConfig — полная конфигурация тоннеля, прочитанная из .conf-файла.
type TunnelConfig struct {
	// Стандартный WireGuard [Interface]
	PrivateKey string
	Address    []string // CIDR-адреса интерфейса
	DNS        []string
	MTU        int

	// Стандартный WireGuard [Peer]
	PublicKey           string
	PresharedKey        string
	Endpoint            string // оригинальный endpoint из конфига
	AllowedIPs          []string
	PersistentKeepalive int

	// TURN-настройки (из #@wgt: комментариев)
	TURN proxy.Config

	// Имя тоннеля (имя файла без расширения)
	Name string
}

// ParseConfBytes парсит WireGuard конфиг из байтов.
// name используется как имя тоннеля (если пусто — "wgkeybot").
func ParseConfBytes(data []byte, name string) (*TunnelConfig, error) {
	cfg := &TunnelConfig{MTU: 1280}
	if name == "" {
		name = "wgkeybot"
	}
	cfg.Name = name

	var section string
	var wgtLines []string

	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#@wgt:") {
			wgtLines = append(wgtLines, line)
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			section = strings.ToLower(strings.Trim(line, "[]"))
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)

		switch section {
		case "interface":
			switch strings.ToLower(k) {
			case "privatekey":
				cfg.PrivateKey = v
			case "address":
				cfg.Address = append(cfg.Address, splitCSV(v)...)
			case "dns":
				cfg.DNS = append(cfg.DNS, splitCSV(v)...)
			case "mtu":
				if n, err := strconv.Atoi(v); err == nil {
					cfg.MTU = n
				}
			}
		case "peer":
			switch strings.ToLower(k) {
			case "publickey":
				cfg.PublicKey = v
			case "presharedkey":
				cfg.PresharedKey = v
			case "endpoint":
				cfg.Endpoint = v
			case "allowedips":
				cfg.AllowedIPs = append(cfg.AllowedIPs, splitCSV(v)...)
			case "persistentkeepalive":
				if n, err := strconv.Atoi(v); err == nil {
					cfg.PersistentKeepalive = n
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	parseTURNSettings(wgtLines, &cfg.TURN)
	return cfg, nil
}

// parseTURNSettings заполняет proxy.Config из списка #@wgt: строк.
func parseTURNSettings(lines []string, t *proxy.Config) {
	for _, line := range lines {
		if !strings.HasPrefix(line, "#@wgt:") {
			continue
		}
		k, v, ok := strings.Cut(line[6:], "=")
		if !ok {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		switch k {
		case "enableturn":
			// Флаг включения — наличие @wgt: секции уже подразумевает включение.
		case "vklink":
			t.VKLink = v
		case "useudp":
			t.UseUDP = strings.EqualFold(v, "true")
		case "streamnum":
			if n, err := strconv.Atoi(v); err == nil {
				t.StreamsTotal = clamp(n, 1, 128)
			}
		case "localport":
			if n, err := strconv.Atoi(v); err == nil {
				t.ListenAddr = fmt.Sprintf("127.0.0.1:%d", clamp(n, 1, 65535))
			}
		case "ipport":
			t.PeerAddr = v
		case "turnip":
			t.TurnIP = v
		case "turnport":
			if n, err := strconv.Atoi(v); err == nil {
				t.TurnPort = clamp(n, 1, 65535)
			}
		case "peertype":
			t.PeerType = v
		case "streamspercred":
			if n, err := strconv.Atoi(v); err == nil {
				t.StreamsPerCred = clamp(n, 1, 16)
			}
		case "watchdogtimeout":
			if n, err := strconv.Atoi(v); err == nil && n >= 5 {
				t.WatchdogSecs = n
			}
		case "wrapkey":
			t.WrapKey = v
		}
	}
}

// BuildWGUAPIConfig строит строку конфигурации в формате WireGuard UAPI.
// listenAddr — адрес TURN-прокси (127.0.0.1:PORT), заменяет оригинальный
// Endpoint. Нужен только userspace-бэкенду (wireguard-go): ядерному WG
// прошивки UAPI не отдаётся, он настраивается своими средствами.
func (c *TunnelConfig) BuildWGUAPIConfig(listenAddr string) string {
	var sb strings.Builder

	// [Interface]
	sb.WriteString("private_key=" + keyToHex(c.PrivateKey) + "\n")

	// [Peer]
	sb.WriteString("public_key=" + keyToHex(c.PublicKey) + "\n")
	if c.PresharedKey != "" {
		sb.WriteString("preshared_key=" + keyToHex(c.PresharedKey) + "\n")
	}

	endpoint := listenAddr
	if endpoint == "" {
		endpoint = c.Endpoint
	}
	sb.WriteString("endpoint=" + endpoint + "\n")

	for _, a := range c.AllowedIPs {
		sb.WriteString("allowed_ip=" + strings.TrimSpace(a) + "\n")
	}
	if c.PersistentKeepalive > 0 {
		sb.WriteString(fmt.Sprintf("persistent_keepalive_interval=%d\n", c.PersistentKeepalive))
	}

	return sb.String()
}

// keyToHex конвертирует base64-ключ WireGuard в lowercase hex.
// wireguard-go IpcSet принимает только hex для private_key/public_key/preshared_key.
func keyToHex(b64key string) string {
	b64key = strings.TrimSpace(b64key)
	if b64key == "" {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(b64key)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(b64key)
		if err != nil {
			return b64key // уже hex или неизвестный формат — пропускаем как есть
		}
	}
	return hex.EncodeToString(raw)
}

// splitCSV разбивает строку через запятую, убирая пробелы.
func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ValidateEndpoint проверяет, что строка является корректным host:port.
func ValidateEndpoint(ep string) error {
	host, port, err := net.SplitHostPort(ep)
	if err != nil {
		return err
	}
	if host == "" || port == "" {
		return fmt.Errorf("empty host or port in endpoint %q", ep)
	}
	return nil
}
