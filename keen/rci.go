package keen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wgkeybot/router/core"
)

// rci.go — тонкий клиент локального REST API прошивки KeeneticOS (NDM).
// Из Entware доступен на http://localhost:79/rci без авторизации.
//
// Мутации выполняются только parse-формой CLI-команд (POST [{"parse":"…"}]):
// она стабильна между версиями прошивки, в отличие от структурного JSON-API.
// Форматы ответов записаны по документации/сообществу для 4.x; помечены
// VERIFY-ON-DEVICE там, где нужна сверка с живым устройством.

// RCI — HTTP-клиент RCI.
type RCI struct {
	base string
	hc   *http.Client
}

// NewRCI создаёт клиент. base — например "http://localhost:79/rci" (без
// хвостового слеша); пустая строка — дефолт.
func NewRCI(base string) *RCI {
	if base == "" {
		base = core.DefaultRCIURL
	}
	return &RCI{
		base: strings.TrimRight(base, "/"),
		hc:   &http.Client{Timeout: 10 * time.Second},
	}
}

// Get выполняет GET-запрос пути вида "show/interface/Wireguard0" и
// декодирует JSON-ответ в out.
func (r *RCI) Get(path string, out any) error {
	url := r.base + "/" + strings.TrimLeft(path, "/")
	resp, err := r.hc.Get(url)
	if err != nil {
		return fmt.Errorf("rci get %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("rci get %s: %w", path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("rci get %s: HTTP %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("rci get %s: parse: %w", path, err)
	}
	return nil
}

// Parse выполняет CLI-команды через POST [{"parse":"<cmd>"},…].
// Ошибка возвращается при транспортном сбое или если NDM ответил
// status=="error" хотя бы на одну команду.
func (r *RCI) Parse(cmds ...string) error {
	if len(cmds) == 0 {
		return nil
	}
	reqBody := make([]map[string]string, len(cmds))
	for i, c := range cmds {
		reqBody[i] = map[string]string{"parse": c}
	}
	data, _ := json.Marshal(reqBody)

	resp, err := r.hc.Post(r.base+"/", "application/json", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("rci parse: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("rci parse: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("rci parse: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var decoded any
	if err := json.Unmarshal(body, &decoded); err != nil {
		// Пустой или не-JSON ответ на успешную команду допустим.
		return nil
	}
	if msg := findRCIError(decoded); msg != "" {
		return fmt.Errorf("rci parse: %s (commands: %s)", msg, strings.Join(cmds, "; "))
	}
	return nil
}

// Save персистит текущую конфигурацию NDM (переживает перезагрузку).
func (r *RCI) Save() error {
	return r.Parse("system configuration save")
}

// findRCIError рекурсивно ищет в ответе NDM объект статуса с status=="error"
// и возвращает его message. Формы ответов parse отличаются между командами,
// поэтому обходим всё дерево, а не фиксированную структуру.
func findRCIError(v any) string {
	switch t := v.(type) {
	case map[string]any:
		if s, _ := t["status"].(string); s == "error" {
			if m, _ := t["message"].(string); m != "" {
				return m
			}
			return "unknown error"
		}
		for _, child := range t {
			if msg := findRCIError(child); msg != "" {
				return msg
			}
		}
	case []any:
		for _, child := range t {
			if msg := findRCIError(child); msg != "" {
				return msg
			}
		}
	}
	return ""
}

// ── show interface ───────────────────────────────────────────────────────────

// IfaceInfo — подмножество полей `show interface`, нужное демону.
type IfaceInfo struct {
	ID            string   `json:"id"`
	InterfaceName string   `json:"interface-name"`
	Type          string   `json:"type"`
	Description   string   `json:"description"`
	Link          string   `json:"link"`
	Connected     string   `json:"connected"`
	State         string   `json:"state"`
	Address       string   `json:"address"`
	Mask          string   `json:"mask"`
	Global        flexBool `json:"global"`
	DefaultGW     flexBool `json:"defaultgw"`
	Priority      flexInt  `json:"priority"`
	Wireguard     *WgInfo  `json:"wireguard,omitempty"`
}

// Up сообщает, поднят ли интерфейс.
func (i IfaceInfo) Up() bool {
	return i.State == "up" || i.Link == "up" || i.Connected == "yes"
}

// WgInfo — секция wireguard из `show interface WireguardN`.
// VERIFY-ON-DEVICE: имена полей записаны по выводу 4.x из сообщества.
type WgInfo struct {
	PublicKey  string   `json:"public-key"`
	ListenPort flexInt  `json:"listen-port"`
	Peers      []WgPeer `json:"peer"`
}

// WgPeer — состояние пира WG.
type WgPeer struct {
	PublicKey     string   `json:"public-key"`
	Remote        string   `json:"remote"`
	RxBytes       flexInt  `json:"rxbytes"`
	TxBytes       flexInt  `json:"txbytes"`
	LastHandshake flexInt  `json:"last-handshake"` // секунд с последнего handshake
	Online        flexBool `json:"online"`
}

// Interfaces возвращает все интерфейсы (`show interface`), map по имени.
func (r *RCI) Interfaces() (map[string]IfaceInfo, error) {
	var m map[string]IfaceInfo
	if err := r.Get("show/interface", &m); err != nil {
		return nil, err
	}
	return m, nil
}

// Interface возвращает один интерфейс (`show interface <name>`).
func (r *RCI) Interface(name string) (IfaceInfo, error) {
	var i IfaceInfo
	err := r.Get("show/interface/"+name, &i)
	return i, err
}

// WANInterface определяет текущий WAN: интерфейс с defaultgw, из поднятых —
// с наибольшим priority (порядок «Приоритетов подключений» NDM).
func (r *RCI) WANInterface() (IfaceInfo, error) {
	m, err := r.Interfaces()
	if err != nil {
		return IfaceInfo{}, err
	}
	var cands []IfaceInfo
	for _, i := range m {
		if bool(i.DefaultGW) {
			cands = append(cands, i)
		}
	}
	if len(cands) == 0 {
		// Fallback: любой поднятый global-интерфейс.
		for _, i := range m {
			if bool(i.Global) && i.Up() {
				cands = append(cands, i)
			}
		}
	}
	if len(cands) == 0 {
		return IfaceInfo{}, fmt.Errorf("rci: WAN interface not found (no defaultgw/global up)")
	}
	sort.Slice(cands, func(a, b int) bool {
		if cands[a].Up() != cands[b].Up() {
			return cands[a].Up()
		}
		return int(cands[a].Priority) > int(cands[b].Priority)
	})
	return cands[0], nil
}

// LANIP возвращает IP-адрес LAN-сегмента роутера (Bridge0/"Home") — адрес, по
// которому пользователю показываются панель и captcha.
func (r *RCI) LANIP() string {
	m, err := r.Interfaces()
	if err != nil {
		return ""
	}
	// Приоритет: Bridge0 (Home) → любой bridge с адресом → любой не-WAN с адресом.
	if i, ok := m["Bridge0"]; ok && i.Address != "" {
		return i.Address
	}
	var bridge, other string
	for _, i := range m {
		if i.Address == "" || bool(i.Global) || bool(i.DefaultGW) {
			continue
		}
		if strings.EqualFold(i.Type, "bridge") && bridge == "" {
			bridge = i.Address
		} else if other == "" {
			other = i.Address
		}
	}
	if bridge != "" {
		return bridge
	}
	return other
}

// Version возвращает строку версии прошивки (`show version`, поле release).
func (r *RCI) Version() (string, error) {
	var v struct {
		Release string `json:"release"`
		Title   string `json:"title"`
	}
	if err := r.Get("show/version", &v); err != nil {
		return "", err
	}
	if v.Release != "" {
		return v.Release, nil
	}
	return v.Title, nil
}

// ── толерантные типы ─────────────────────────────────────────────────────────
// Прошивка местами отдаёт числа строками и булевы как "yes"/"no" — не полагаемся
// на конкретный вариант.

type flexBool bool

func (b *flexBool) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), `"`)
	switch strings.ToLower(s) {
	case "true", "yes", "1", "up":
		*b = true
	default:
		*b = false
	}
	return nil
}

type flexInt int64

func (n *flexInt) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		// Число с плавающей точкой (uptime бывает "123.45").
		f, ferr := strconv.ParseFloat(s, 64)
		if ferr != nil {
			*n = 0
			return nil
		}
		v = int64(f)
	}
	*n = flexInt(v)
	return nil
}
