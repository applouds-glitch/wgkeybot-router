package core

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
)

// panel.go — веб-панель на LAN: статус, импорт конфига, Reconnect/Reload,
// ссылка на captcha. Тонкая обёртка над тем же Controller, что и control-сокет
// — одна реализация на CLI, панель и (на OpenWrt) LuCI.
// Без аутентификации: слушает только LAN-IP роутера; порт/выключение — в
// настройках (panel_port = 0).

// Panel — запущенная веб-панель.
type Panel struct {
	srv *http.Server
	ln  net.Listener
	url string
}

// StartPanel поднимает панель на LAN-IP роутера (fallback — все адреса, если
// LAN-IP определить не удалось; со стороны WAN порт закрыт firewall'ом роутера).
func StartPanel(port int, p Platform, ctrl Controller) (*Panel, error) {
	host := p.LANIP()
	addr := fmt.Sprintf("%s:%d", host, port)
	ln, err := net.Listen("tcp", addr)
	if err != nil && host != "" {
		log.Printf("[Panel] warn: listen %s: %v — слушаю на всех адресах", addr, err)
		host = ""
		ln, err = net.Listen("tcp", fmt.Sprintf(":%d", port))
	}
	if err != nil {
		return nil, err
	}
	if host == "" {
		host = "<router-ip>"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, panelHTML)
	})
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ctrl.Status())
	})
	mux.HandleFunc("POST /api/reconnect", func(w http.ResponseWriter, r *http.Request) {
		apiResult(w, ctrl.Reconnect())
	})
	mux.HandleFunc("POST /api/reload", func(w http.ResponseWriter, r *http.Request) {
		apiResult(w, ctrl.Reload())
	})
	mux.HandleFunc("POST /api/solve", func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimSpace(r.FormValue("token"))
		if token == "" {
			apiResult(w, fmt.Errorf("пустой токен"))
			return
		}
		ctrl.SolveCaptcha(token)
		apiResult(w, nil)
	})
	mux.HandleFunc("POST /api/import", func(w http.ResponseWriter, r *http.Request) {
		apiResult(w, panelImport(r.FormValue("data"), ctrl))
	})

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()

	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	return &Panel{srv: srv, ln: ln, url: fmt.Sprintf("http://%s:%s/", host, portStr)}, nil
}

// URL — адрес панели для показа пользователю.
func (p *Panel) URL() string { return p.url }

// Close останавливает панель.
func (p *Panel) Close() {
	if p.srv != nil {
		p.srv.Close()
	}
}

func apiResult(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"result": "ok"})
}

// panelImport принимает либо токен от @wg_key_bot, либо текст .conf целиком
// (различаются по секции [Interface]), сохраняет конфиг и переподнимает туннель.
func panelImport(data string, ctrl Controller) error {
	data = strings.TrimSpace(data)
	if data == "" {
		return fmt.Errorf("вставьте токен или содержимое .conf")
	}
	if strings.Contains(data, "[Interface]") {
		if _, err := ParseConfBytes([]byte(data), "wgkeybot"); err != nil {
			return fmt.Errorf("некорректный .conf: %w", err)
		}
		if err := SaveConfig([]byte(data)); err != nil {
			return err
		}
	} else {
		cfgData, accessToken, err := InitFromToken(data)
		if err != nil {
			return err
		}
		if err := SaveConfig(cfgData); err != nil {
			return err
		}
		st := LoadState()
		st.AccessToken = accessToken
		if err := SaveState(st); err != nil {
			return err
		}
	}
	return ctrl.Reconnect()
}

const panelHTML = `<!DOCTYPE html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>WgKeyBot</title>
<style>
  body { font-family: system-ui, sans-serif; max-width: 640px; margin: 2rem auto; padding: 0 1rem; background: #f5f6f8; color: #222; }
  h1 { font-size: 1.4rem; }
  .card { background: #fff; border-radius: 8px; padding: 1rem 1.25rem; margin-bottom: 1rem; box-shadow: 0 1px 3px rgba(0,0,0,.08); }
  .row { display: flex; justify-content: space-between; padding: .25rem 0; }
  .muted { color: #777; }
  .ok { color: #188038; font-weight: 600; }
  .bad { color: #c5221f; font-weight: 600; }
  .warn { background: #fef7e0; border: 1px solid #f9c74f; border-radius: 6px; padding: .6rem .8rem; margin-top: .6rem; }
  .err { background: #fce8e6; border: 1px solid #f28b82; border-radius: 6px; padding: .6rem .8rem; margin-top: .6rem; white-space: pre-wrap; }
  button { background: #1a73e8; color: #fff; border: 0; border-radius: 6px; padding: .5rem 1rem; margin-right: .5rem; cursor: pointer; font-size: .95rem; }
  button.secondary { background: #5f6368; }
  textarea { width: 100%; box-sizing: border-box; min-height: 7rem; font-family: monospace; font-size: .85rem; }
  #msg { margin-top: .5rem; }
</style>
</head>
<body>
<h1>WgKeyBot</h1>

<div class="card">
  <div class="row"><span class="muted">Состояние</span><span id="state">…</span></div>
  <div class="row"><span class="muted">Интерфейс</span><span id="iface">—</span></div>
  <div class="row"><span class="muted">Трафик</span><span id="traffic">—</span></div>
  <div class="row"><span class="muted">Handshake</span><span id="hs">—</span></div>
  <div id="captcha" class="warn" style="display:none"></div>
  <div id="error" class="err" style="display:none"></div>
  <div style="margin-top: .8rem">
    <button onclick="act('reconnect')">Переподключить</button>
    <button class="secondary" onclick="act('reload')">Обновить конфиг</button>
  </div>
</div>

<div class="card">
  <p class="muted" style="margin-top:0">Вставьте токен от <b>@wg_key_bot</b> или содержимое .conf-файла:</p>
  <textarea id="import-data" placeholder="токен или [Interface]…"></textarea>
  <div style="margin-top: .5rem"><button onclick="doImport()">Импортировать</button></div>
  <div id="msg"></div>
</div>

<script>
function fmtBytes(b) {
  if (!b) return '0 B';
  var u = ['B','KiB','MiB','GiB','TiB'], i = 0;
  while (b >= 1024 && i < u.length - 1) { b /= 1024; i++; }
  return b.toFixed(i ? 1 : 0) + ' ' + u[i];
}
function refresh() {
  fetch('/api/status').then(function(r){ return r.json(); }).then(function(s) {
    var st = document.getElementById('state');
    st.textContent = s.connected ? 'подключён' : 'отключён';
    st.className = s.connected ? 'ok' : 'bad';
    document.getElementById('iface').textContent = s.wg_iface || '—';
    document.getElementById('traffic').textContent = '↓ ' + fmtBytes(s.rx_bytes) + '  ↑ ' + fmtBytes(s.tx_bytes);
    var hs = document.getElementById('hs');
    if (s.last_handshake > 0) {
      var ago = Math.max(0, Math.round(Date.now()/1000 - s.last_handshake));
      hs.textContent = ago + ' с назад';
    } else { hs.textContent = s.connected ? 'ещё не было' : '—'; }
    var cap = document.getElementById('captcha');
    if (s.captcha_url) {
      cap.style.display = '';
      cap.innerHTML = '⚠ Требуется captcha: <a href="' + s.captcha_url + '" target="_blank">решить</a>';
    } else { cap.style.display = 'none'; }
    var err = document.getElementById('error');
    if (s.error) { err.style.display = ''; err.textContent = s.error; }
    else { err.style.display = 'none'; }
  }).catch(function(){});
}
function act(name) {
  fetch('/api/' + name, {method: 'POST'}).then(function(r){ return r.json(); }).then(function(j) {
    showMsg(j.error || 'OK');
    setTimeout(refresh, 500);
  });
}
function doImport() {
  var data = document.getElementById('import-data').value;
  var body = new URLSearchParams();
  body.set('data', data);
  showMsg('Импорт…');
  fetch('/api/import', {method: 'POST', body: body}).then(function(r){ return r.json(); }).then(function(j) {
    showMsg(j.error || 'Конфиг сохранён, туннель переподнимается.');
    setTimeout(refresh, 1000);
  });
}
function showMsg(t) { document.getElementById('msg').textContent = t; }
refresh();
setInterval(refresh, 3000);
</script>
</body>
</html>
`
