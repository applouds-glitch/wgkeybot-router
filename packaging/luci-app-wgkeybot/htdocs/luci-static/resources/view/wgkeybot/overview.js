'use strict';
'require view';
'require form';
'require fs';
'require ui';
'require poll';
'require dom';

var BIN = '/usr/bin/wgkeybot';

function callStatus() {
	return fs.exec(BIN, ['status-json']).then(function(res) {
		try { return JSON.parse((res.stdout || '{}').trim()); }
		catch (e) { return { connected: false, error: 'parse error' }; }
	}).catch(function() {
		return { connected: false, error: 'service not running' };
	});
}

function fmtBytes(n) {
	n = n || 0;
	var u = ['B', 'KiB', 'MiB', 'GiB', 'TiB'], i = 0;
	while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
	return (i === 0 ? n : n.toFixed(1)) + ' ' + u[i];
}

function fmtAgo(ts) {
	var d = Math.max(0, Math.floor(Date.now() / 1000) - ts);
	if (d < 60) return d + _(' сек назад');
	if (d < 3600) return Math.floor(d / 60) + _(' мин назад');
	return Math.floor(d / 3600) + _(' ч назад');
}

function statRow(k, v) {
	return E('tr', { 'class': 'tr' }, [
		E('td', { 'class': 'td left', 'width': '33%' }, E('strong', {}, k)),
		E('td', { 'class': 'td left' }, v)
	]);
}

function renderStatus(st) {
	var rows = [];
	var state = st.connected
		? E('span', { 'style': 'color:#2e7d32;font-weight:bold' }, _('подключён'))
		: E('span', { 'style': 'color:#999' }, _('отключён'));
	rows.push(statRow(_('Состояние'), state));
	rows.push(statRow(_('Режим'), st.mode || '—'));
	if (st.connected) {
		rows.push(statRow(_('Принято'), fmtBytes(st.rx_bytes)));
		rows.push(statRow(_('Отправлено'), fmtBytes(st.tx_bytes)));
		rows.push(statRow(_('Рукопожатие'),
			st.last_handshake ? fmtAgo(st.last_handshake) : _('ещё не было')));
	}
	if (st.captcha_url) {
		rows.push(statRow(_('CAPTCHA'), E('span', {}, [
			E('a', { 'href': st.captcha_url, 'target': '_blank', 'rel': 'noreferrer' }, st.captcha_url),
			E('div', { 'style': 'color:#b26a00' }, _('откройте ссылку с устройства в LAN, чтобы решить'))
		])));
	}
	if (st.error)
		rows.push(statRow(_('Сообщение'), E('span', { 'style': 'color:#b71c1c' }, st.error)));
	return E('table', { 'class': 'table' }, rows);
}

return view.extend({
	load: function() {
		return callStatus();
	},

	handleImport: function() {
		var input = E('input', {
			'type': 'text',
			'class': 'cbi-input-text',
			'style': 'width:100%',
			'placeholder': _('Токен от @wg_key_bot')
		});
		ui.showModal(_('Импорт токена'), [
			E('p', {}, _('Вставьте токен, полученный у Telegram-бота @wg_key_bot.')),
			input,
			E('div', { 'class': 'right', 'style': 'margin-top:1em' }, [
				E('button', { 'class': 'btn', 'click': ui.hideModal }, _('Отмена')),
				' ',
				E('button', {
					'class': 'btn cbi-button-positive',
					'click': ui.createHandlerFn(this, function() {
						var token = (input.value || '').trim();
						if (!token) { input.focus(); return; }
						ui.showModal(_('Импорт…'), [
							E('p', { 'class': 'spinning' }, _('Получение конфига с сервера'))
						]);
						return fs.exec(BIN, ['import', token]).then(function(res) {
							ui.hideModal();
							var msg = ((res.stdout || '') + (res.stderr || '')).trim();
							ui.addNotification(null, E('p', {}, msg || _('Готово')),
								res.code === 0 ? 'info' : 'error');
						}).catch(function(e) {
							ui.hideModal();
							ui.addNotification(null, E('p', {}, '' + e), 'error');
						});
					})
				}, _('Импортировать'))
			])
		]);
	},

	handleReload: function() {
		ui.showModal(_('Обновление…'), [
			E('p', { 'class': 'spinning' }, _('Перезапрос конфига по токену'))
		]);
		return fs.exec(BIN, ['reload']).then(function(res) {
			ui.hideModal();
			var msg = ((res.stdout || '') + (res.stderr || '')).trim();
			ui.addNotification(null, E('p', {}, msg || _('Готово')),
				res.code === 0 ? 'info' : 'warning');
		}).catch(function(e) {
			ui.hideModal();
			ui.addNotification(null, E('p', {}, '' + e), 'error');
		});
	},

	renderConfig: function() {
		var m, s, o;
		m = new form.Map('wgkeybot', null,
			_('WireGuard VPN-клиент с TURN/DTLS прокси и авторизацией через ВКонтакте.'));

		s = m.section(form.NamedSection, 'main', 'wgkeybot', _('Настройки'));
		s.anonymous = true;

		o = s.option(form.Flag, 'enabled', _('Включить'),
			_('Запускать туннель (procd-сервис).'));
		o.rmempty = false;

		o = s.option(form.ListValue, 'mode', _('Режим'));
		o.value('gateway', _('Gateway — весь трафик LAN через туннель'));
		o.value('socks', _('SOCKS5 — локальный прокси без смены маршрутов'));
		o.default = 'gateway';

		o = s.option(form.Value, 'ifname', _('TUN-интерфейс'));
		o.default = 'wgkb0';
		o.datatype = 'string';

		o = s.option(form.Value, 'mtu', _('MTU'));
		o.datatype = 'range(1280,9000)';
		o.default = '1280';

		o = s.option(form.Value, 'socks_port', _('SOCKS5 порт'));
		o.datatype = 'port';
		o.default = '1080';
		o.depends('mode', 'socks');

		o = s.option(form.Value, 'lan', _('LAN-интерфейс'),
			_('Network-интерфейс, чьи клиенты заворачиваются в туннель.'));
		o.default = 'lan';
		o.depends('mode', 'gateway');

		o = s.option(form.Value, 'captcha_listen', _('Адрес страницы captcha'),
			_('Открывается с устройства в LAN при необходимости решить captcha.'));
		o.default = '0.0.0.0:8089';

		o = s.option(form.Value, 'fwmark', _('fwmark'),
			_('Метка сокетов прокси (их трафик идёт мимо туннеля).'));
		o.default = '0x4b8';
		o.optional = true;
		o.depends('mode', 'gateway');

		o = s.option(form.Value, 'table', _('Таблица маршрутизации'));
		o.datatype = 'uinteger';
		o.default = '51820';
		o.optional = true;
		o.depends('mode', 'gateway');

		o = s.option(form.Flag, 'nat', _('nft NAT (fallback)'),
			_('Ставить masquerade демоном в дополнение к firewall-зоне.'));
		o.default = '1';
		o.optional = true;
		o.depends('mode', 'gateway');

		return m.render();
	},

	render: function(st) {
		var self = this;

		var statusBox = E('div', { 'id': 'wgkb-status' }, renderStatus(st));

		poll.add(function() {
			return callStatus().then(function(s) {
				dom.content(document.getElementById('wgkb-status'), renderStatus(s));
			});
		}, 5);

		var actions = E('div', { 'class': 'cbi-section' }, [
			E('button', {
				'class': 'btn cbi-button cbi-button-action important',
				'click': ui.createHandlerFn(self, 'handleImport')
			}, _('Импорт токена')),
			' ',
			E('button', {
				'class': 'btn cbi-button cbi-button-action',
				'click': ui.createHandlerFn(self, 'handleReload')
			}, _('Обновить конфиг'))
		]);

		var top = E('div', {}, [
			E('h2', {}, _('WgKeyBot')),
			E('div', { 'class': 'cbi-section' }, [
				E('h3', {}, _('Состояние')),
				statusBox
			]),
			actions
		]);

		return this.renderConfig().then(function(mapEl) {
			top.appendChild(mapEl);
			return top;
		});
	}
});
