package core

import (
	"context"
	"fmt"
	"log"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/wgkeybot/router/pkg/proxy"
)

// Manager управляет жизненным циклом одного WireGuard/TURN-туннеля. Поток
// подключения общий для обеих платформ; всё, что у них различается, живёт за
// интерфейсом Backend (см. platform.go).
type Manager struct {
	plat     Platform
	cfg      *TunnelConfig
	settings Settings
	state    State

	mu        sync.Mutex
	tunnel    *proxy.Tunnel
	backend   Backend
	connected bool

	// Captcha под отдельным мьютексом: статус/решение читаются, пока Connect
	// заблокирован в ожидании токена.
	capMu   sync.Mutex
	captcha *CaptchaServer
}

// NewManager создаёт Manager для конфига, настроек и сохранённого состояния.
func NewManager(plat Platform, cfg *TunnelConfig, s Settings, st State) *Manager {
	return &Manager{plat: plat, cfg: cfg, settings: s, state: st}
}

// listenAddr собирает адрес TURN-прокси. Порт: #@wgt:LocalPort из конфига
// (приоритет) → ProxyPort из настроек → 0, то есть «любой свободный».
//
// Фиксированный порт нужен там, где endpoint пира прописывается до старта
// прокси (Keenetic: ядро WG настраивается через RCI). Там, где endpoint
// отдаётся уже после готовности прокси (OpenWrt: UAPI для wireguard-go), порт
// можно не фиксировать — ProxyPort там 0.
func (m *Manager) listenAddr() string {
	port := strconv.Itoa(m.settings.ProxyPort)
	if m.cfg.TURN.ListenAddr != "" {
		if _, p, err := net.SplitHostPort(m.cfg.TURN.ListenAddr); err == nil && p != "" && p != "0" {
			port = p
		}
	}
	return net.JoinHostPort(m.settings.ListenAddr, port)
}

// Connect поднимает туннель: подготовка обхода → TURN-прокси → WireGuard.
// ctx отменяет ожидание (captcha, timeout).
func (m *Manager) Connect(ctx context.Context) error {
	m.mu.Lock()
	already := m.connected
	m.mu.Unlock()
	if already {
		return fmt.Errorf("tunnel already connected")
	}

	cfg := m.cfg
	if cfg.TURN.VKLink == "" {
		return fmt.Errorf("конфиг не содержит TURN-настроек (#@wgt:VKLink).\n" +
			"Получите конфиг через @wg_key_bot и выполните: wgkeybot import <token>")
	}

	sysDNS := m.plat.SystemDNS()
	log.Printf("[Manager] system DNS: %v", sysDNS)

	proxyCfg := cfg.TURN
	proxyCfg.SystemDNS = sysDNS
	proxyCfg.ListenAddr = m.listenAddr()

	backend, err := m.plat.NewBackend(cfg, m.settings, m.state)
	if err != nil {
		return fmt.Errorf("backend: %w", err)
	}

	// Обход для собственного трафика прокси ставится до NewTunnel, пока не
	// отправлено ни одного пакета: при реконнекте прошлый туннель может быть
	// ещё поднят, и без обхода трафик прокси зациклился бы в него.
	if err := backend.Prepare(cfg.TURN.TurnIP); err != nil {
		backend.Down()
		return fmt.Errorf("prepare: %w", err)
	}

	t, err := proxy.NewTunnel(proxyCfg)
	if err != nil {
		backend.Down()
		return fmt.Errorf("new tunnel: %w", err)
	}

	// Очистка частичного состояния при ошибке/отмене.
	var committed bool
	defer func() {
		if committed {
			return
		}
		backend.Down()
		t.Stop()
	}()

	t.StartBootstrap()

	// Ожидание готовности прокси с поэтапной обработкой captcha. Благодаря
	// captcha-free потоку VK Calls captcha нужна только как legacy-fallback.
	log.Printf("[Manager] waiting for TURN proxy...")
	for {
		status := t.WaitReady(90 * time.Second)
		if status == proxy.ReadyStatusOK {
			break
		}
		switch status {
		case proxy.ReadyStatusCaptchaRequired:
			if err := m.handleCaptcha(ctx, t); err != nil {
				return err
			}
		case proxy.ReadyStatusAuthRequired:
			return fmt.Errorf("VK авторизация устарела (CALL_REQUIRES_AUTH) — обновите конфиг: wgkeybot reload")
		default:
			return fmt.Errorf("TURN proxy failed (status %d)", status)
		}
	}

	listenAddr := t.ListenAddr()
	log.Printf("[Manager] TURN proxy listening at %s", listenAddr)

	// Обход для динамических TURN-серверов и VK/OK auth-хостов — до подъёма
	// WireGuard, иначе ре-фетч credentials уйдёт в туннель и повиснет: туннелю
	// нужны creds, а creds нужен туннель.
	var bypass []string
	for _, addr := range t.ActiveTURNAddrs() {
		if host, _, err := net.SplitHostPort(addr); err == nil && net.ParseIP(host) != nil {
			bypass = append(bypass, host)
		}
	}
	bypass = append(bypass, t.AuthBypassIPs()...)
	if len(bypass) > 0 {
		log.Printf("[Manager] adding bypass routes: %v", bypass)
		backend.Bypass(bypass)
	}

	if err := backend.Up(t, listenAddr); err != nil {
		return err
	}

	// Коммит состояния.
	m.mu.Lock()
	m.tunnel = t
	m.backend = backend
	m.connected = true
	m.mu.Unlock()
	committed = true

	log.Printf("[Manager] tunnel connected (%s, mode=%s)", m.plat.Name(), m.settings.Mode)
	return nil
}

// handleCaptcha поднимает headless captcha-страницу на LAN и ждёт токен.
func (m *Manager) handleCaptcha(ctx context.Context, t *proxy.Tunnel) error {
	upstream := t.PendingCaptchaURL()
	cs, err := StartCaptchaServer(m.settings.CaptchaListen, m.plat.LANIP(), upstream)
	if err != nil {
		return fmt.Errorf("captcha server: %w", err)
	}
	m.capMu.Lock()
	m.captcha = cs
	m.capMu.Unlock()
	defer func() {
		m.capMu.Lock()
		m.captcha = nil
		m.capMu.Unlock()
		cs.Close()
	}()

	log.Printf("[Manager] ⚠ CAPTCHA REQUIRED — откройте %s с устройства в LAN, чтобы решить", cs.LocalURL())
	token, err := cs.Wait(ctx)
	if err != nil {
		return fmt.Errorf("captcha: %w", err)
	}
	t.SolveCaptcha(token)
	// Цикл WaitReady может запросить captcha повторно (следующий solve-mode).
	return nil
}

// WgIface возвращает имя интерфейса, выбранное при Connect (для state.json).
func (m *Manager) WgIface() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.backend == nil {
		return ""
	}
	return m.backend.WgIface()
}

// Disconnect опускает интерфейс, снимает маршруты и останавливает прокси.
func (m *Manager) Disconnect() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.connected {
		return
	}
	if m.backend != nil {
		m.backend.Down()
		m.backend = nil
	}
	if m.tunnel != nil {
		m.tunnel.Stop()
		m.tunnel = nil
	}
	m.connected = false
	log.Printf("[Manager] tunnel disconnected")
}

// Stats возвращает статистику туннеля.
func (m *Manager) Stats() TunnelStats {
	m.mu.Lock()
	connected := m.connected
	backend := m.backend
	m.mu.Unlock()

	if !connected || backend == nil {
		return TunnelStats{}
	}
	return backend.Stats()
}

// IsConnected возвращает true если туннель поднят.
func (m *Manager) IsConnected() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.connected
}

// PendingCaptchaURL возвращает LAN-URL ожидающей captcha, либо "".
func (m *Manager) PendingCaptchaURL() string {
	m.capMu.Lock()
	defer m.capMu.Unlock()
	if m.captcha != nil {
		return m.captcha.LocalURL()
	}
	return ""
}

// SolveCaptcha доставляет токен ожидающей captcha-странице (ручной ввод).
func (m *Manager) SolveCaptcha(token string) {
	m.capMu.Lock()
	cs := m.captcha
	m.capMu.Unlock()
	if cs != nil {
		cs.Submit(token)
	}
}

// CheckNetworkChange проверяет смену физического аплинка; при смене
// переустанавливает привязанное к нему и уведомляет туннель (сброс DNS/HTTP-кешей,
// реконнект TURN-стримов). Вызывается поллером и внешним хуком.
func (m *Manager) CheckNetworkChange() bool {
	m.mu.Lock()
	t := m.tunnel
	backend := m.backend
	m.mu.Unlock()

	if t == nil || backend == nil {
		return false
	}
	if !backend.NetworkChanged() {
		return false
	}
	t.OnNetworkChange()
	return true
}

// StartWatchdog следит за свежестью WG-handshake. Если handshake устарел (>3 мин)
// при растущем TX — соединение считается мёртвым и вызывается onDead. Горутина
// завершается при отмене ctx или отключении туннеля.
func (m *Manager) StartWatchdog(ctx context.Context, onDead func()) {
	const (
		pollInterval  = 30 * time.Second
		staleAfter    = 3 * time.Minute
		neverAfter    = 150 * time.Second
		deadChecksMax = 2
	)
	go func() {
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		upSince := time.Now()
		var prevTx uint64
		prevTxSet := false
		deadChecks := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			stats := m.Stats()
			if !stats.Connected {
				return
			}
			// Пока висит captcha, прокси заблокирован в ожидании ответа, и
			// handshake закономерно устаревает — это не мёртвый туннель.
			if m.PendingCaptchaURL() != "" {
				upSince = time.Now()
				prevTxSet = false
				deadChecks = 0
				continue
			}
			now := time.Now()
			isDead := false
			if stats.LastHandshake.IsZero() {
				isDead = now.Sub(upSince) > neverAfter
			} else if now.Sub(stats.LastHandshake) > staleAfter {
				isDead = prevTxSet && stats.TxBytes > prevTx
			}
			prevTx = stats.TxBytes
			prevTxSet = true
			if !isDead {
				deadChecks = 0
				continue
			}
			deadChecks++
			if deadChecks >= deadChecksMax {
				log.Printf("[Manager] watchdog: WG handshake stale — reconnect")
				onDead()
				return
			}
		}
	}()
}
