package core

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// StatusInfo — снимок состояния демона, отдаётся по команде STATUS.
type StatusInfo struct {
	Connected     bool   `json:"connected"`
	Mode          string `json:"mode"`
	WgIface       string `json:"wg_iface,omitempty"`
	RxBytes       uint64 `json:"rx_bytes"`
	TxBytes       uint64 `json:"tx_bytes"`
	LastHandshake int64  `json:"last_handshake"`
	CaptchaURL    string `json:"captcha_url,omitempty"`
	Error         string `json:"error,omitempty"`
}

// Controller — то, что control-сервер открывает наружу (реализует демон).
type Controller interface {
	Status() StatusInfo
	CaptchaURL() string
	SolveCaptcha(token string)
	Reload() error    // re-fetch конфига по токену + reconnect
	Reconnect() error // переподнять туннель с текущего сохранённого конфига
	// NetworkChange — мягкий пинок извне (ndm-хук ifstatechanged на Keenetic,
	// hotplug на OpenWrt): проверить смену WAN и при необходимости
	// переустановить маршруты/TURN-стримы, без полного реконнекта.
	NetworkChange()
}

// ControlServer — unix-сокет сервер управления.
type ControlServer struct {
	ln   net.Listener
	ctrl Controller
	path string
}

// StartControlServer открывает unix-сокет и начинает обслуживать команды.
func StartControlServer(path string, ctrl Controller) (*ControlServer, error) {
	// Каталог сокета живёт в tmpfs и после перезагрузки может отсутствовать.
	os.MkdirAll(filepath.Dir(path), 0755)
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("control socket %s: %w", path, err)
	}
	os.Chmod(path, 0600)
	s := &ControlServer{ln: ln, ctrl: ctrl, path: path}
	go s.serve()
	return s, nil
}

func (s *ControlServer) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(c)
	}
}

func (s *ControlServer) handle(c net.Conn) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))

	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil && line == "" {
		return
	}
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) == 0 {
		return
	}
	switch strings.ToUpper(fields[0]) {
	case "STATUS":
		json.NewEncoder(c).Encode(s.ctrl.Status())
	case "CAPTCHA":
		fmt.Fprintln(c, s.ctrl.CaptchaURL())
	case "SOLVE":
		if len(fields) >= 2 {
			s.ctrl.SolveCaptcha(fields[1])
			fmt.Fprintln(c, "OK")
		} else {
			fmt.Fprintln(c, "ERR no token")
		}
	case "RELOAD":
		if err := s.ctrl.Reload(); err != nil {
			fmt.Fprintln(c, "ERR "+err.Error())
		} else {
			fmt.Fprintln(c, "OK")
		}
	case "RECONNECT":
		if err := s.ctrl.Reconnect(); err != nil {
			fmt.Fprintln(c, "ERR "+err.Error())
		} else {
			fmt.Fprintln(c, "OK")
		}
	case "NETCHANGE":
		s.ctrl.NetworkChange()
		fmt.Fprintln(c, "OK")
	default:
		fmt.Fprintln(c, "ERR unknown command")
	}
}

// Close закрывает сокет и удаляет файл.
func (s *ControlServer) Close() {
	s.ln.Close()
	os.Remove(s.path)
}

// ControlRequest шлёт команду демону через сокет и возвращает ответ (для CLI).
func ControlRequest(path, cmd string) (string, error) {
	c, err := net.DialTimeout("unix", path, 3*time.Second)
	if err != nil {
		return "", err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(120 * time.Second))
	if _, err := fmt.Fprintln(c, cmd); err != nil {
		return "", err
	}
	data, _ := io.ReadAll(c)
	return strings.TrimRight(string(data), "\n"), nil
}
