package core

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// captcha.go — headless-вариант решения VK-captcha для роутера.
//
// На Windows reverse-proxy сопровождался открытием системного браузера/WebView2.
// На OpenWrt браузера нет: страница captcha поднимается на LAN-адресе роутера,
// пользователь открывает её с телефона/ПК (URL отдаётся в лог, status и
// `wgkeybot captcha`), а инжектированный JS перехватывает success_token сам.
//
// Чтобы страница работала при открытии по ЛЮБОМУ адресу роутера (LAN-IP, а не
// только 127.0.0.1), localOrigin в JS берётся из window.location.origin, а
// абсолютные upstream-URL переписываются в корне-относительные ("" вместо
// upstreamOrigin). Это отличие от Windows-порта, где localOrigin был фиксирован.
//
// Captcha запрашивается на этапе bootstrap (до подъёма туннеля), поэтому
// исходящие HTTP reverse-proxy идут по физической сети естественным образом.

// CaptchaServer — запущенный reverse-proxy, перехватывающий success_token.
type CaptchaServer struct {
	ln       net.Listener
	srv      *http.Server
	tokenCh  chan string
	localURL string
}

// StartCaptchaServer поднимает reverse-proxy для upstreamURL на listenAddr.
// displayHost — адрес роутера для показа пользователю (если listen — wildcard).
func StartCaptchaServer(listenAddr, displayHost, upstreamURL string) (*CaptchaServer, error) {
	targetURL, err := url.Parse(upstreamURL)
	if err != nil {
		return nil, fmt.Errorf("parse captcha URL: %w", err)
	}

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("captcha listener %s: %w", listenAddr, err)
	}

	upstreamOrigin := targetURL.Scheme + "://" + targetURL.Host
	tokenCh := make(chan string, 1)
	deliver := func(token string) {
		if token == "" {
			return
		}
		select {
		case tokenCh <- token:
		default:
		}
	}

	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		ForceAttemptHTTP2:   false,
	}

	proxy := &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(req *httputil.ProxyRequest) {
			req.Out.URL.Scheme = targetURL.Scheme
			req.Out.URL.Host = targetURL.Host
			if req.Out.URL.Path == "" {
				req.Out.URL.Path = targetURL.Path
			}
			req.Out.Host = targetURL.Host
			req.Out.Header.Del("Accept-Encoding")
			req.Out.Header.Del("TE")
			// Браузер шлёт Origin/Referer со своим (LAN) адресом — подменяем на upstream.
			for _, h := range []string{"Origin", "Referer"} {
				if req.Out.Header.Get(h) != "" {
					req.Out.Header.Set(h, upstreamOrigin)
				}
			}
		},
		ModifyResponse: func(res *http.Response) error {
			stripSecurityHeaders(res)
			rewriteProxyCookies(res)

			if res.StatusCode >= 300 && res.StatusCode < 400 {
				if loc := res.Header.Get("Location"); loc != "" {
					// upstream → корне-относительный (same-origin при любом хосте роутера).
					res.Header.Set("Location", strings.ReplaceAll(loc, upstreamOrigin, ""))
				}
			}

			contentType := res.Header.Get("Content-Type")
			isCheck := strings.Contains(res.Request.URL.Path, "captchaNotRobot.check")
			if !isHTMLLike(contentType) && !isCheck {
				return nil
			}

			bodyBytes, err := readResponseBody(res)
			if err != nil {
				return err
			}

			if isCheck {
				deliver(extractSuccessToken(bodyBytes))
			}

			if isHTMLLike(contentType) {
				bodyBytes = []byte(rewriteCaptchaHTML(string(bodyBytes), upstreamOrigin))
				res.Header.Del("Content-Encoding")
			}

			res.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			res.ContentLength = int64(len(bodyBytes))
			res.Header.Set("Content-Length", fmt.Sprint(len(bodyBytes)))
			return nil
		},
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/local-captcha-result", func(w http.ResponseWriter, r *http.Request) {
		deliver(r.FormValue("token"))
		w.Header().Set("Access-Control-Allow-Origin", "*")
		fmt.Fprint(w, "ok")
	})

	// Catch-all для сторонних хостов, которые грузит виджет. Так как браузер
	// запрашивает их с роутера, всё остаётся same-origin и CORS не мешает.
	mux.HandleFunc("/generic_proxy", func(w http.ResponseWriter, r *http.Request) {
		parsed, err := url.Parse(r.URL.Query().Get("proxy_url"))
		if err != nil || parsed.Host == "" {
			http.Error(w, "bad proxy_url", http.StatusBadRequest)
			return
		}
		generic := &httputil.ReverseProxy{
			Transport: transport,
			Rewrite: func(req *httputil.ProxyRequest) {
				req.Out.URL.Scheme = parsed.Scheme
				req.Out.URL.Host = parsed.Host
				req.Out.URL.Path = parsed.Path
				req.Out.URL.RawQuery = parsed.RawQuery
				req.Out.Host = parsed.Host
				req.Out.Header.Del("Accept-Encoding")
			},
			ModifyResponse: func(res *http.Response) error {
				stripSecurityHeaders(res)
				if strings.Contains(parsed.Path, "captchaNotRobot.check") {
					body, err := readResponseBody(res)
					if err != nil {
						return err
					}
					deliver(extractSuccessToken(body))
					res.Header.Del("Content-Encoding")
					res.Body = io.NopCloser(bytes.NewReader(body))
					res.ContentLength = int64(len(body))
					res.Header.Set("Content-Length", fmt.Sprint(len(body)))
				}
				return nil
			},
		}
		generic.ServeHTTP(w, r)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" && targetURL.Path != "" && targetURL.Path != "/" && r.URL.RawQuery == "" {
			localPath := targetURL.Path
			if targetURL.RawQuery != "" {
				localPath += "?" + targetURL.RawQuery
			}
			http.Redirect(w, r, localPath, http.StatusTemporaryRedirect)
			return
		}
		proxy.ServeHTTP(w, r)
	})

	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()

	cs := &CaptchaServer{
		ln:       ln,
		srv:      srv,
		tokenCh:  tokenCh,
		localURL: buildLocalURL(ln.Addr().String(), displayHost),
	}
	log.Printf("[Captcha] reverse-proxy %s → %s", cs.localURL, upstreamOrigin)
	return cs, nil
}

// LocalURL — адрес, который должен открыть пользователь.
func (c *CaptchaServer) LocalURL() string { return c.localURL }

// Wait блокирует до перехвата success_token или отмены ctx.
func (c *CaptchaServer) Wait(ctx context.Context) (string, error) {
	select {
	case token := <-c.tokenCh:
		return token, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Submit вручную доставляет токен (например введённый через `wgkeybot captcha
// <token>`), как если бы его перехватил JS-хук. No-op для пустого токена.
func (c *CaptchaServer) Submit(token string) {
	if token == "" {
		return
	}
	select {
	case c.tokenCh <- token:
	default:
	}
}

// Close останавливает captcha-сервер.
func (c *CaptchaServer) Close() {
	if c.srv != nil {
		c.srv.Close()
	}
	if c.ln != nil {
		c.ln.Close()
	}
}

// buildLocalURL формирует URL для пользователя. Если listen — wildcard
// (0.0.0.0 / ::), подставляет displayHost (LAN-IP роутера) либо 127.0.0.1.
func buildLocalURL(listenAddr, displayHost string) string {
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return "http://" + listenAddr + "/"
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		if displayHost != "" {
			host = displayHost
		} else {
			host = "127.0.0.1"
		}
	}
	return fmt.Sprintf("http://%s/", net.JoinHostPort(host, port))
}

// ── Reverse-proxy helpers (порт из winbridge/captcha.go) ─────────────────────

func stripSecurityHeaders(res *http.Response) {
	for _, h := range []string{
		"Content-Security-Policy", "Content-Security-Policy-Report-Only",
		"X-Content-Security-Policy", "X-WebKit-CSP",
		"Cross-Origin-Opener-Policy", "Cross-Origin-Embedder-Policy",
		"Cross-Origin-Resource-Policy", "X-Frame-Options",
		"Strict-Transport-Security", "Alt-Svc",
	} {
		res.Header.Del(h)
	}
}

func rewriteProxyCookies(res *http.Response) {
	cookies := res.Cookies()
	if len(cookies) == 0 {
		return
	}
	res.Header.Del("Set-Cookie")
	for _, c := range cookies {
		c.Domain = ""
		c.Secure = false
		c.Partitioned = false
		if c.SameSite == http.SameSiteNoneMode || c.SameSite == http.SameSiteStrictMode {
			c.SameSite = http.SameSiteLaxMode
		}
		res.Header.Add("Set-Cookie", c.String())
	}
}

func readResponseBody(res *http.Response) ([]byte, error) {
	reader := res.Body
	if res.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(res.Body)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		reader = gz
	}
	body, err := io.ReadAll(reader)
	res.Body.Close()
	return body, err
}

func isHTMLLike(contentType string) bool {
	return strings.Contains(contentType, "text/html") ||
		strings.Contains(contentType, "application/xhtml+xml")
}

func extractSuccessToken(body []byte) string {
	var payload struct {
		Response struct {
			SuccessToken string `json:"success_token"`
		} `json:"response"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	return payload.Response.SuccessToken
}

// rewriteCaptchaHTML переписывает абсолютные upstream-URL в корне-относительные
// и инжектирует скрипт, который во время выполнения маршрутизирует все URL (XHR,
// fetch, window.open, динамические href/src/action) через тот же origin, что и
// страница (window.location.origin). За счёт этого страница работает при открытии
// по любому адресу роутера.
func rewriteCaptchaHTML(html, upstreamOrigin string) string {
	html = strings.ReplaceAll(html, upstreamOrigin, "")

	script := fmt.Sprintf(`
<script>
(function() {
    var localOrigin = window.location.origin;
    var upstreamOrigin = %q;

    function rewriteUrl(urlStr) {
        if (!urlStr || typeof urlStr !== 'string') return urlStr;
        if (urlStr.indexOf(localOrigin) === 0) return urlStr;
        if (urlStr.indexOf(upstreamOrigin) === 0) return localOrigin + urlStr.slice(upstreamOrigin.length);
        if (urlStr.indexOf('//') === 0) {
            return '/generic_proxy?proxy_url=' + encodeURIComponent(window.location.protocol + urlStr);
        }
        if (urlStr.indexOf('http://') === 0 || urlStr.indexOf('https://') === 0) {
            return '/generic_proxy?proxy_url=' + encodeURIComponent(urlStr);
        }
        return urlStr;
    }

    function rewriteElementAttr(el, attr) {
        if (!el || !el.getAttribute) return;
        var value = el.getAttribute(attr);
        if (!value) return;
        var rewritten = rewriteUrl(value);
        if (rewritten !== value) el.setAttribute(attr, rewritten);
    }

    function rewriteDocument(root) {
        if (!root || !root.querySelectorAll) return;
        root.querySelectorAll('[href]').forEach(function(el) { rewriteElementAttr(el, 'href'); });
        root.querySelectorAll('[src]').forEach(function(el) { rewriteElementAttr(el, 'src'); });
        root.querySelectorAll('form[action]').forEach(function(el) { rewriteElementAttr(el, 'action'); });
    }

    function handleSuccessToken(token) {
        if (!token) return;
        fetch('/local-captcha-result', {
            method: 'POST',
            headers: {'Content-Type': 'application/x-www-form-urlencoded'},
            body: 'token=' + encodeURIComponent(token)
        }).catch(function() {});
    }

    var origOpen = XMLHttpRequest.prototype.open;
    XMLHttpRequest.prototype.open = function() {
        if (arguments[1] && typeof arguments[1] === 'string') {
            this._origUrl = arguments[1];
            arguments[1] = rewriteUrl(arguments[1]);
        }
        return origOpen.apply(this, arguments);
    };
    var origSend = XMLHttpRequest.prototype.send;
    XMLHttpRequest.prototype.send = function() {
        var xhr = this;
        if (this._origUrl && this._origUrl.indexOf('captchaNotRobot.check') !== -1) {
            xhr.addEventListener('load', function() {
                try {
                    var data = JSON.parse(xhr.responseText);
                    if (data.response && data.response.success_token) handleSuccessToken(data.response.success_token);
                } catch (e) {}
            });
        }
        return origSend.apply(this, arguments);
    };

    var origFetch = window.fetch;
    if (origFetch) {
        window.fetch = function() {
            var url = arguments[0];
            var urlStr = (typeof url === 'object' && url && url.url) ? url.url : url;
            var origUrlStr = urlStr;
            if (typeof urlStr === 'string') {
                urlStr = rewriteUrl(urlStr);
                arguments[0] = urlStr;
            }
            var p = origFetch.apply(this, arguments);
            if (typeof origUrlStr === 'string' && origUrlStr.indexOf('captchaNotRobot.check') !== -1) {
                p.then(function(r) { return r.clone().json(); }).then(function(data) {
                    if (data.response && data.response.success_token) handleSuccessToken(data.response.success_token);
                }).catch(function() {});
            }
            return p;
        };
    }

    var origWindowOpen = window.open;
    if (origWindowOpen) {
        window.open = function(url) {
            if (typeof url === 'string') arguments[0] = rewriteUrl(url);
            return origWindowOpen.apply(this, arguments);
        };
    }

    rewriteDocument(document);
    if (document.documentElement && window.MutationObserver) {
        new MutationObserver(function(mutations) {
            mutations.forEach(function(mutation) {
                if (mutation.type === 'attributes' && mutation.target) {
                    rewriteElementAttr(mutation.target, mutation.attributeName);
                    return;
                }
                mutation.addedNodes.forEach(function(node) {
                    if (node.nodeType === 1) rewriteDocument(node);
                });
            });
        }).observe(document.documentElement, {
            subtree: true, childList: true, attributes: true,
            attributeFilter: ['href', 'src', 'action']
        });
    }
})();
</script>
`, upstreamOrigin)

	if idx := strings.Index(html, "</head>"); idx >= 0 {
		return html[:idx] + script + html[idx:]
	}
	if idx := strings.Index(html, "</body>"); idx >= 0 {
		return html[:idx] + script + html[idx:]
	}
	return html + script
}
