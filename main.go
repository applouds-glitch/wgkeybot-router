package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/wgkeybot/router/core"
	"github.com/wgkeybot/router/platform"
)

// Version устанавливается линковщиком (-X main.Version=...).
var Version = "dev"

// plat — платформенный мост этой сборки (см. пакет platform). Создаётся раньше
// любой работы с файлами: он же объявляет ядру раскладку путей.
var plat core.Platform

func main() {
	log.SetFlags(log.LstdFlags)
	core.AppVersion = Version

	p, err := platform.New()
	if err != nil {
		log.Fatalf("platform: %v", err)
	}
	plat = p

	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	args := os.Args[2:]
	switch os.Args[1] {
	case "run":
		cmdRun()
	case "import":
		cmdImport(args)
	case "reload":
		cmdSimple("RELOAD")
	case "status":
		cmdStatus()
	case "status-json":
		cmdStatusJSON()
	case "captcha":
		cmdCaptcha(args)
	case "netchange":
		// служебная: пинок от хука смены сети (не показываем в usage)
		cmdSimple("NETCHANGE")
	case "up":
		cmdInitd("start")
	case "down":
		cmdInitd("stop")
	case "version", "-v", "--version":
		fmt.Printf("wgkeybot %s (%s)\n", Version, platform.Target)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `wgkeybot `+Version+` — WireGuard/TURN VPN client for `+platform.Target+`

Usage:
  wgkeybot run               foreground daemon (запускается init-скриптом)
  wgkeybot import <token>    импорт конфига по токену от @wg_key_bot
  wgkeybot reload            обновить конфиг по сохранённому токену
  wgkeybot status            состояние туннеля
  wgkeybot captcha [<token>] показать URL ожидающей captcha или отправить токен
  wgkeybot up | down         запустить / остановить сервис
  wgkeybot version
`)
}

// ── daemon (run) ─────────────────────────────────────────────────────────────

type daemon struct {
	mu        sync.Mutex
	settings  core.Settings
	mgr       *core.Manager
	lastErr   string
	reconnect chan struct{}
}

func cmdRun() {
	if os.Geteuid() != 0 {
		log.Fatal("wgkeybot run требует root (файлы конфигурации, сеть)")
	}
	plat.InitLogging()

	d := &daemon{reconnect: make(chan struct{}, 1)}
	d.settings = core.LoadSettings(plat)

	ctrl, err := core.StartControlServer(core.ControlSocket(), d)
	if err != nil {
		log.Fatalf("control server: %v", err)
	}
	defer ctrl.Close()

	// Веб-панель живёт независимо от циклов реконнекта.
	if d.settings.PanelPort > 0 {
		if p, err := core.StartPanel(d.settings.PanelPort, plat, d); err != nil {
			log.Printf("[run] warn: panel: %v", err)
		} else {
			defer p.Close()
			log.Printf("[run] panel at %s", p.URL())
		}
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)

	log.Printf("[run] wgkeybot %s started (%s)", Version, plat.Name())

	for {
		plat.RotateLogs()
		settings := core.LoadSettings(plat)
		cfg, err := core.LoadConfig()
		if err != nil {
			d.update(nil, settings, "нет конфига — выполните: wgkeybot import <token>")
			log.Printf("[run] %s", d.snapErr())
			if d.waitIdle(sigCh) == evStop {
				return
			}
			continue
		}

		state := core.LoadState()
		mgr := core.NewManager(plat, cfg, settings, state)
		ctx, cancel := context.WithCancel(context.Background())
		d.update(mgr, settings, "")

		if err := mgr.Connect(ctx); err != nil {
			d.setErr(err.Error())
			log.Printf("[run] connect failed: %v", err)
			cancel()
			mgr.Disconnect()
			if d.waitRetry(sigCh, 15*time.Second) == evStop {
				return
			}
			continue
		}
		d.setErr("")

		// Запоминаем выбранный интерфейс, чтобы после рестарта демона
		// управлять тем же подключением, а не занимать новый номер.
		if iface := mgr.WgIface(); iface != "" && iface != state.WgIface {
			state.WgIface = iface
			if err := core.SaveState(state); err != nil {
				log.Printf("[run] warn: save state: %v", err)
			}
		}

		mgr.StartWatchdog(ctx, func() { d.triggerReconnect() })
		go d.netPoller(ctx, mgr)

		ev := d.waitConnected(sigCh)
		cancel()
		mgr.Disconnect()
		if ev == evStop {
			return
		}
		// evReconnect / SIGHUP → следующая итерация перечитает конфиг/настройки.
	}
}

type event int

const (
	evStop event = iota
	evReconnect
)

// waitIdle блокирует, когда нет конфига: ждёт reconnect/SIGHUP или остановки.
func (d *daemon) waitIdle(sigCh <-chan os.Signal) event {
	select {
	case s := <-sigCh:
		if isStop(s) {
			return evStop
		}
		return evReconnect
	case <-d.reconnect:
		return evReconnect
	}
}

// waitRetry ждёт reconnect/SIGHUP/остановку или истечение backoff.
func (d *daemon) waitRetry(sigCh <-chan os.Signal, backoff time.Duration) event {
	t := time.NewTimer(backoff)
	defer t.Stop()
	select {
	case s := <-sigCh:
		if isStop(s) {
			return evStop
		}
		return evReconnect
	case <-d.reconnect:
		return evReconnect
	case <-t.C:
		return evReconnect
	}
}

// waitConnected ждёт событие при поднятом туннеле.
func (d *daemon) waitConnected(sigCh <-chan os.Signal) event {
	select {
	case s := <-sigCh:
		if isStop(s) {
			return evStop
		}
		return evReconnect
	case <-d.reconnect:
		return evReconnect
	}
}

// netPoller — пассивный поллинг смены аплинка. Внешние хуки (ndm
// ifstatechanged на Keenetic, hotplug на OpenWrt) шлют NETCHANGE в
// control-сокет; поллер — страховка, если хук не установлен.
func (d *daemon) netPoller(ctx context.Context, mgr *core.Manager) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			mgr.CheckNetworkChange()
		}
	}
}

func (d *daemon) triggerReconnect() {
	select {
	case d.reconnect <- struct{}{}:
	default:
	}
}

func (d *daemon) update(mgr *core.Manager, settings core.Settings, errMsg string) {
	d.mu.Lock()
	d.mgr = mgr
	d.settings = settings
	d.lastErr = errMsg
	d.mu.Unlock()
}

func (d *daemon) setErr(msg string) {
	d.mu.Lock()
	d.lastErr = msg
	d.mu.Unlock()
}

func (d *daemon) snapErr() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastErr
}

// ── Controller (control socket + панель + LuCI) ─────────────────────────────

func (d *daemon) Status() core.StatusInfo {
	d.mu.Lock()
	mgr := d.mgr
	lastErr := d.lastErr
	mode := d.settings.Mode
	d.mu.Unlock()

	si := core.StatusInfo{Mode: string(mode), Error: lastErr}
	if mgr != nil {
		stats := mgr.Stats()
		si.Connected = stats.Connected
		si.WgIface = stats.WgIface
		si.RxBytes = stats.RxBytes
		si.TxBytes = stats.TxBytes
		if !stats.LastHandshake.IsZero() {
			si.LastHandshake = stats.LastHandshake.Unix()
		}
		si.CaptchaURL = mgr.PendingCaptchaURL()
	}
	return si
}

func (d *daemon) CaptchaURL() string {
	d.mu.Lock()
	mgr := d.mgr
	d.mu.Unlock()
	if mgr != nil {
		return mgr.PendingCaptchaURL()
	}
	return ""
}

func (d *daemon) SolveCaptcha(token string) {
	d.mu.Lock()
	mgr := d.mgr
	d.mu.Unlock()
	if mgr != nil {
		mgr.SolveCaptcha(token)
	}
}

func (d *daemon) Reconnect() error {
	d.triggerReconnect()
	return nil
}

// NetworkChange — мягкий пинок от внешнего хука: проверить смену аплинка и
// переустановить обход/TURN-стримы без полного реконнекта.
func (d *daemon) NetworkChange() {
	d.mu.Lock()
	mgr := d.mgr
	d.mu.Unlock()
	if mgr != nil {
		mgr.CheckNetworkChange()
	}
}

func (d *daemon) Reload() error {
	st := core.LoadState()
	if st.AccessToken == "" {
		return fmt.Errorf("нет access_token — выполните: wgkeybot import <token>")
	}
	cfgData, err := core.FetchConfig(st.AccessToken)
	if err != nil {
		return err
	}
	if err := core.SaveConfig(cfgData); err != nil {
		return err
	}
	d.triggerReconnect()
	return nil
}

func isStop(s os.Signal) bool {
	return s == syscall.SIGTERM || s == syscall.SIGINT
}

// ── CLI subcommands ──────────────────────────────────────────────────────────

func cmdImport(args []string) {
	if len(args) < 1 || strings.TrimSpace(args[0]) == "" {
		fmt.Fprintln(os.Stderr, "usage: wgkeybot import <token>")
		os.Exit(2)
	}
	token := strings.TrimSpace(args[0])
	fmt.Println("Импорт конфига...")
	cfgData, accessToken, err := core.InitFromToken(token)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		os.Exit(1)
	}
	if err := core.SaveConfig(cfgData); err != nil {
		fmt.Fprintln(os.Stderr, "сохранение конфига:", err)
		os.Exit(1)
	}
	st := core.LoadState()
	st.AccessToken = accessToken
	if err := core.SaveState(st); err != nil {
		fmt.Fprintln(os.Stderr, "сохранение токена:", err)
		os.Exit(1)
	}
	fmt.Printf("Конфиг сохранён в %s\n", core.ConfigDir())

	// Применить сразу, если сервис запущен.
	if _, err := core.ControlRequest(core.ControlSocket(), "RECONNECT"); err == nil {
		fmt.Println("Сервис уведомлён — туннель переподнимается.")
	} else {
		fmt.Println("Запустите сервис:", core.InitScript(), "start")
	}
}

func cmdStatus() {
	resp, err := core.ControlRequest(core.ControlSocket(), "STATUS")
	if err != nil {
		fmt.Println("сервис не запущен (нет ответа на", core.ControlSocket()+")")
		os.Exit(1)
	}
	var si core.StatusInfo
	if err := json.Unmarshal([]byte(resp), &si); err != nil {
		fmt.Println(resp)
		return
	}
	state := "отключён"
	if si.Connected {
		state = "подключён"
	}
	fmt.Printf("Состояние:  %s (режим: %s)\n", state, si.Mode)
	if si.Connected {
		if si.WgIface != "" {
			fmt.Printf("Интерфейс:  %s\n", si.WgIface)
		}
		fmt.Printf("Трафик:     ↓ %s  ↑ %s\n", humanBytes(si.RxBytes), humanBytes(si.TxBytes))
		if si.LastHandshake > 0 {
			ago := time.Since(time.Unix(si.LastHandshake, 0)).Round(time.Second)
			fmt.Printf("Handshake:  %s назад\n", ago)
		} else {
			fmt.Println("Handshake:  ещё не было")
		}
	}
	if si.CaptchaURL != "" {
		fmt.Printf("CAPTCHA:    откройте %s в браузере, чтобы решить\n", si.CaptchaURL)
	}
	if si.Error != "" {
		fmt.Printf("Ошибка:     %s\n", si.Error)
	}
}

// cmdStatusJSON печатает сырой STATUS-JSON (для панели, LuCI и скриптов).
// Без демона — валидный JSON с ошибкой.
func cmdStatusJSON() {
	resp, err := core.ControlRequest(core.ControlSocket(), "STATUS")
	if err != nil {
		fmt.Println(`{"connected":false,"error":"service not running"}`)
		return
	}
	fmt.Println(strings.TrimSpace(resp))
}

func cmdCaptcha(args []string) {
	if len(args) >= 1 && strings.TrimSpace(args[0]) != "" {
		resp, err := core.ControlRequest(core.ControlSocket(), "SOLVE "+strings.TrimSpace(args[0]))
		if err != nil {
			fmt.Println("сервис не запущен")
			os.Exit(1)
		}
		fmt.Println(resp)
		return
	}
	resp, err := core.ControlRequest(core.ControlSocket(), "CAPTCHA")
	if err != nil {
		fmt.Println("сервис не запущен")
		os.Exit(1)
	}
	if strings.TrimSpace(resp) == "" {
		fmt.Println("нет ожидающей captcha")
		return
	}
	fmt.Printf("Откройте в браузере (с устройства в LAN): %s\n", resp)
}

func cmdSimple(cmd string) {
	resp, err := core.ControlRequest(core.ControlSocket(), cmd)
	if err != nil {
		fmt.Println("сервис не запущен")
		os.Exit(1)
	}
	fmt.Println(resp)
}

func cmdInitd(action string) {
	c := exec.Command(core.InitScript(), action)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		os.Exit(1)
	}
}

func humanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
