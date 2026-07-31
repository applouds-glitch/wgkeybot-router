package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/wgkeybot/router/pkg/proxy"
)

const apiBaseURL = "https://key.shadowgate.online"

// AppVersion отправляется в заголовке X-App-Version каждого запроса к API.
// Устанавливается main при старте (ldflags -X). Это идентификация сборки для
// серверной статистики: клиент версию сообщает, но сам по ней ничего не решает
// — версионного гейтинга на стороне клиента нет (см. apiGet).
var AppVersion = "dev"

// ErrUnauthorized возвращается когда сервер отвечает 401 — нужно заново
// выполнить импорт токена.
var ErrUnauthorized = errors.New("сессия устарела, выполните импорт токена заново (wgkeybot import <token>)")

// apiResolver резолвит хосты control-plane (key.shadowgate.online) через
// закалённый резолвер прокси (UDP→DoH→DoT-фолбэк) вместо системного DNS.
// Это снимает зависимость от роутерного DNS: при его таймауте запрос всё равно
// уходит на Yandex/Google (в т.ч. поверх DoH/DoT, если UDP/53 режут).
// Кэш отдельный от bootstrap-кэша прокси — лукапы API не попадают в
// ResolvedHostIPs() и не порождают лишних bypass-маршрутов.
var apiResolver = proxy.NewDnsCache()

// apiClient — общий HTTP-клиент для запросов к API. DialContext подменяет
// резолвинг на apiResolver; TLS (SNI/проверка серта) при этом по-прежнему идёт
// по имени хоста из URL, а не по IP, потому что http.Transport накладывает TLS
// поверх уже установленного TCP-соединения.
var apiClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			// IP-литералы пропускаем как есть — резолвить нечего.
			if net.ParseIP(host) == nil {
				ip, rerr := apiResolver.Resolve(ctx, host)
				if rerr != nil {
					return nil, fmt.Errorf("resolve %s: %w", host, rerr)
				}
				host = ip
			}
			return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(host, port))
		},
	},
}

// apiGet выполняет GET-запрос к API с заголовками X-App-Version и Authorization.
// Транспортные сбои (DNS/dial/TLS/timeout) — транзиентные, поэтому запрос
// повторяется с коротким backoff. Любой HTTP-ответ (включая 401 и прочие
// 4xx/5xx) детерминирован и возвращается без ретрая.
//
// Отдельной ветки «требуется обновление» нет: версия уходит на сервер как
// идентификация сборки, но клиент по ней ничего не решает. Если сервер ответит
// 426, это обычная ошибка с его текстом.
func apiGet(urlStr, accessToken string) ([]byte, error) {
	const maxAttempts = 3
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequest(http.MethodGet, urlStr, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-App-Version", strings.SplitN(AppVersion, "-", 2)[0])
		if accessToken != "" {
			req.Header.Set("Authorization", "Bearer "+accessToken)
		}

		resp, err := apiClient.Do(req)
		if err != nil {
			// Транспортный сбой — сервер не ответил. Повторяем (0.5s, 1s).
			lastErr = err
			if attempt < maxAttempts {
				time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
			}
			continue
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, ErrUnauthorized
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("сервер вернул %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return body, nil
	}
	return nil, lastErr
}

// InitFromToken выполняет первичный импорт по одноразовому токену.
// Возвращает конфиг и access_token для последующих вызовов FetchConfig.
func InitFromToken(token string) (config []byte, accessToken string, err error) {
	body, err := apiGet(apiBaseURL+"/api/v1/init/"+strings.TrimSpace(token), "")
	if err != nil {
		return nil, "", err
	}
	var result struct {
		AccessToken string `json:"access_token"`
		Config      string `json:"config"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, "", fmt.Errorf("parse response: %w", err)
	}
	if result.AccessToken == "" {
		return nil, "", fmt.Errorf("сервер вернул пустой access_token")
	}
	if result.Config == "" {
		return nil, "", fmt.Errorf("сервер вернул пустой config")
	}
	return []byte(strings.TrimSpace(result.Config)), result.AccessToken, nil
}

// FetchConfig обновляет конфиг по сохранённому access_token.
func FetchConfig(accessToken string) ([]byte, error) {
	body, err := apiGet(apiBaseURL+"/api/v1/config", accessToken)
	if err != nil {
		return nil, err
	}
	var result struct {
		Config string `json:"config"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	if result.Config == "" {
		return nil, fmt.Errorf("сервер вернул пустой config")
	}
	return []byte(strings.TrimSpace(result.Config)), nil
}
