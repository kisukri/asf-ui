'use strict';
(() => {
  const $ = (sel, root = document) => root.querySelector(sel);
  const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));
  const ESC = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };
  const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ESC[c]);
  const S = { me: null, csrf: null, prefs: null, features: {}, page: null, modal: 0 };

  const LANGS = [['zh', '中文', 'zh-CN'], ['ja', '日本語', 'ja'], ['en', 'English', 'en']];
  let LANG = 'zh';
  let DICT = {};
  function t(s, ...a) {
    let out = s;
    if (LANG !== 'zh') {
      const e = DICT[s];
      if (e && e[LANG === 'ja' ? 0 : 1]) out = e[LANG === 'ja' ? 0 : 1];
    }
    if (!a.length) return out;
    let i = 0;
    return out.replace(/%(?:\[(\d+)\])?[sd]/g, (_, n) => String((n ? a[n - 1] : a[i++]) ?? ''));
  }
  const sep = () => (LANG === 'en' ? ', ' : '、');
  const locale = () => LANGS.find((l) => l[0] === LANG)[2];
  function pickLang() {
    try { const v = localStorage.getItem('lang'); if (LANGS.some((l) => l[0] === v)) return v; } catch (e) { /* ignore */ }
    for (const l of navigator.languages || [navigator.language || '']) {
      const p = String(l).toLowerCase().slice(0, 2);
      if (p === 'ja' || p === 'zh' || p === 'en') return p;
    }
    return 'en';
  }
  function applyLang() {
    document.documentElement.lang = locale();
    document.title = t('ASF 挂机面板');
  }
  function setLang(l) {
    LANG = l;
    try { localStorage.setItem('lang', l); } catch (e) { /* ignore */ }
    applyLang();
    route();
  }
  const langSwitch = () => `<div class="lang" role="group" aria-label="Language">${LANGS.map(([k, n]) =>
    `<button type="button" data-lang="${k}" class="${k === LANG ? 'on' : ''}">${n}</button>`).join('')}</div>`;
  const copyright = () => `<div class="copy">© ${new Date().getFullYear()} kisukri</div>`;

  async function api(method, path, body) {
    const opt = { method, credentials: 'same-origin', headers: { Accept: 'application/json', 'X-Lang': LANG } };
    if (body !== undefined) {
      opt.headers['Content-Type'] = 'application/json';
      opt.body = JSON.stringify(body);
    }
    if (method !== 'GET' && S.csrf) opt.headers['X-CSRF-Token'] = S.csrf;
    let res;
    try {
      res = await fetch(path, opt);
    } catch (e) {
      throw { message: t('网络连接失败，请检查网络后重试。') };
    }
    let data = null;
    try { data = await res.json(); } catch (e) { /* not json */ }
    if (!res.ok) {
      const err = (data && data.error) || { message: t('请求失败（%d）', res.status) };
      err.status = res.status;
      if (res.status === 401 && !path.startsWith('/api/auth/')) {
        S.me = null;
        S.csrf = null;
        location.hash = '#/login';
      }
      throw err;
    }
    return data;
  }

  function fmtDur(sec) {
    sec = Math.max(0, Math.round(sec || 0));
    const d = Math.floor(sec / 86400), h = Math.floor((sec % 86400) / 3600), m = Math.floor((sec % 3600) / 60);
    if (d) return t('%d 天 %d 小时', d, h);
    if (h) return t('%d 小时 %d 分', h, m);
    if (m) return t('%d 分钟', m);
    return sec ? t('不到 1 分钟') : t('%d 分钟', 0);
  }
  function fmtTime(ts) {
    if (!ts) return '—';
    const diff = Date.now() / 1000 - ts;
    if (diff < 60) return t('刚刚');
    if (diff < 3600) return t('%d 分钟前', Math.floor(diff / 60));
    if (diff < 86400) return t('%d 小时前', Math.floor(diff / 3600));
    return fmtDate(ts);
  }
  function fmtDate(ts) {
    return ts ? new Date(ts * 1000).toLocaleString(locale(), { hour12: false }) : '—';
  }
  function gameHtml(appId, name) {
    return name ? `${esc(name)} <span class="muted small">App ${esc(appId)}</span>` : `App ID ${esc(appId)}`;
  }
  function newKey() {
    const a = new Uint8Array(16);
    crypto.getRandomValues(a);
    return Array.from(a, (b) => b.toString(16).padStart(2, '0')).join('');
  }

  const LINK_RES = [
    /store\.steampowered\.com\/(?:agecheck\/)?app\/(\d+)/i,
    /steamcommunity\.com\/app\/(\d+)/i,
    /steamdb\.info\/app\/(\d+)/i,
    /s\.team\/a\/(\d+)/i,
    /steam:\/\/(?:run|rungameid|store)\/(\d+)/i,
  ];
  function parseAppId(text) {
    text = String(text || '').trim();
    let v = null;
    if (/^\d{1,10}$/.test(text)) v = Number(text);
    else for (const re of LINK_RES) { const m = text.match(re); if (m) { v = Number(m[1]); break; } }
    return v && v > 0 && v <= 4294967295 ? v : null;
  }
  function parseMany(text) {
    const ids = [], bad = [];
    for (const part of String(text || '').split(/[\s,，;；、]+/)) {
      if (!part) continue;
      const id = parseAppId(part);
      if (id) { if (!ids.includes(id)) ids.push(id); } else bad.push(part);
    }
    return { ids, bad };
  }

  const TASK_KIND = { running: 'ok', queued: 'info', completed: 'done', stopped: 'muted', failed: 'err' };
  const FARM_KIND = { idling: 'ok', farming: 'ok', stopped: 'muted', idle: 'done', waiting: 'info', offline: 'err',
    needs_input: 'warn', occupied: 'warn', playtime: 'info' };
  const CONN_KIND = { ok: 'ok', untested: 'muted', disabled: 'muted' };
  function noTargetsHtml(what) {
    return S.features.user_connections
      ? `<div class="panel empty"><h2>${t('还没有 ASF 连接')}</h2><p>${what}</p><a class="btn primary" href="#/connections">${t('添加 ASF 连接')}</a></div>`
      : `<div class="panel empty"><h2>${t('还没有可用的 Bot')}</h2><p>${t('管理员还没有为你分配 Bot。如果管理员给了你新建额度，可以点“现在在挂”旁边的“新建 Bot”。')}</p></div>`;
  }
  const badge = (kind, text) => `<span class="badge b-${esc(kind)}">${esc(text)}</span>`;
  const assignedTag = (name) => (name ? ` <span class="tag owner" title="${esc(t('已分配给 %s', name))}">→ ${esc(name)}</span>` : '');

  function toast(msg, kind = 'info', ms = 4500) {
    const el = document.createElement('div');
    el.className = `toast ${kind}`;
    el.textContent = msg;
    $('#toasts').appendChild(el);
    setTimeout(() => el.remove(), ms);
  }
  const errText = (e) => `${e.message || t('操作失败')}${e.hint ? ' ' + e.hint : ''}`;
  const showError = (e) => toast(errText(e), 'error', 7000);

  function openModal({ title, body = '', wide = false, actions = [{ label: t('关闭'), value: null }] }) {
    S.modal++;
    const back = document.createElement('div');
    back.className = 'backdrop';
    back.innerHTML = `<div class="modal ${wide ? 'wide' : ''}" role="dialog" aria-modal="true" aria-labelledby="mtitle">
      <div class="modal-head"><h2 id="mtitle">${esc(title)}</h2><button class="btn ghost icon" data-close aria-label="${t('关闭')}">✕</button></div>
      <div class="modal-body">${body}</div>
      <div class="modal-foot">${actions.map((a, i) =>
        `<button class="btn ${a.kind || ''}" data-idx="${i}">${esc(a.label)}</button>`).join('')}</div></div>`;
    document.body.appendChild(back);
    let resolve;
    const done = new Promise((r) => { resolve = r; });
    const close = (v) => {
      if (!back.isConnected) return;
      back.remove();
      S.modal--;
      document.removeEventListener('keydown', onKey);
      resolve(v);
    };
    const onKey = (e) => { if (e.key === 'Escape') close(null); };
    document.addEventListener('keydown', onKey);
    back.addEventListener('click', (e) => {
      if (e.target === back || e.target.closest('[data-close]')) return close(null);
      const b = e.target.closest('.modal-foot [data-idx]');
      if (b) {
        const a = actions[Number(b.dataset.idx)];
        if (a.onClick) a.onClick(b, close); else close(a.value);
      }
    });
    const first = $('.modal-foot .primary', back) || $('.modal-foot .btn', back);
    if (first) first.focus();
    return { el: back, body: $('.modal-body', back), close, done };
  }

  async function confirmBox(title, html, { confirm = t('确认'), danger = false, onConfirm = null } = {}) {
    const m = openModal({
      title, body: html,
      actions: [{ label: t('取消'), value: false }, {
        label: confirm, kind: danger ? 'danger solid' : 'primary',
        onClick: (_, close) => { if (onConfirm) onConfirm(m.el); close(true); },
      }],
    });
    return (await m.done) === true;
  }

  async function busy(btn, fn) {
    if (!btn || btn.disabled) return;
    const old = btn.textContent;
    btn.disabled = true;
    btn.classList.add('busy');
    btn.textContent = t('处理中…');
    try { return await fn(); } finally {
      if (btn.isConnected) { btn.disabled = false; btn.classList.remove('busy'); btn.textContent = old; }
    }
  }

  function resultsHtml(results) {
    return `<ul class="plain">${results.map((r) => `<li>${r.ok ? badge('ok', t('成功')) : badge('err', t('失败'))}
      <strong>${esc(r.label || r.bot || r.id)}</strong> ${esc(r.message || '')}${r.hint ? ` <span class="muted">${esc(r.hint)}</span>` : ''}</li>`).join('')}</ul>`;
  }

  const NAV = [['', '游戏时长'], ['overview', '总览'], ['farming', '卡牌挂机'], ['connections', 'ASF 连接'], ['settings', '设置']];

  function renderShell(path) {
    const items = NAV.filter(([p]) => p !== 'connections' || S.features.user_connections)
      .concat(S.me.is_admin ? [['admin', '后台管理']] : []);
    $('#app').innerHTML = `<div class="layout">
      <aside class="sidebar">
        <a class="brand" href="#/"><span class="mark">ASF</span><span class="word">Panel</span></a>
        <nav class="nav">${items.map(([p, label]) => `<a href="#/${p}" class="${p === path ? 'active' : ''}">${t(label)}</a>`).join('')}</nav>
        <div class="spacer"></div>
        <div class="userbox">
          <div class="who"><span class="avatar">${esc(S.me.username.slice(0, 1).toUpperCase())}</span>
            <span><span class="name">${esc(S.me.username)}</span>${S.me.is_admin ? `<span class="role">${t('管理员')}</span>` : ''}</span></div>
          ${langSwitch()}
          <button class="btn ghost sm" id="logout">${t('退出登录')}</button>
          ${copyright()}
        </div>
      </aside>
      <main class="main"><div id="main"></div><footer class="foot">${copyright()}</footer></main></div>`;
    $('#logout').addEventListener('click', async () => {
      try { await api('POST', '/api/auth/logout'); } catch (e) { /* ignore */ }
      S.me = null; S.csrf = null;
      location.hash = '#/login';
    });
    return $('#main');
  }

  async function route() {
    const path = (location.hash || '#/').replace(/^#\/?/, '').split('?')[0];
    const pub = path === 'login' || path === 'register';
    if (!S.me && !pub) { location.hash = '#/login'; return; }
    if (S.me && pub) { location.hash = '#/'; return; }
    const page = PAGES[path] || PAGES[''];
    if (page.admin && !S.me.is_admin) { location.hash = '#/'; return; }
    if (page === Connections && !S.features.user_connections) { location.hash = '#/'; return; }
    S.page = page;
    while (S.modal > 0) { const b = $('.backdrop'); if (b) b.remove(); S.modal--; }
    const root = pub ? $('#app') : renderShell(path);
    try { await page.render(root); } catch (e) { root.innerHTML = `<div class="note err">${esc(errText(e))}</div>`; }
  }

  function autoRefresh() {
    const p = S.page;
    if (!p || !p.refresh || S.modal || document.visibilityState !== 'visible') return;
    const a = document.activeElement;
    if (a && /^(INPUT|SELECT|TEXTAREA)$/.test(a.tagName)) return;
    p.refresh(true).catch(() => {});
  }

  const head = (title, lead = '', extra = '') => `<div class="page-head"><div><h1>${title}</h1>${lead ? `<p class="lead">${lead}</p>` : ''}</div>${extra}</div>`;

  function authShell(inner) {
    return `<div class="auth"><div class="auth-top"><span class="brand"><span class="mark">ASF</span><span class="word">Panel</span></span>${langSwitch()}</div>
      ${inner}<div class="auth-foot">${copyright()}</div></div>`;
  }

  const Login = {
    async render(root) {
      root.innerHTML = authShell(`<form class="card form" id="f" novalidate>
        <div><h1>${t('登录')}</h1><p class="sub">${t('卡牌挂机与游戏时长，一处管理。')}</p></div>
        <div class="field"><label for="u">${t('用户名')}</label><input id="u" type="text" autocomplete="username" required maxlength="64"></div>
        <div class="field"><label for="p">${t('密码')}</label><input id="p" type="password" autocomplete="current-password" required maxlength="128"></div>
        <div id="msg"></div>
        <button class="btn primary block" type="submit">${t('登录')}</button>
        <div class="alt">${t('有邀请码？')} <a href="#/register">${t('注册账户')}</a></div></form>`);
      $('#f').addEventListener('submit', async (e) => {
        e.preventDefault();
        const btn = $('button[type=submit]', e.target);
        await busy(btn, async () => {
          try {
            await api('POST', '/api/auth/login', { username: $('#u').value.trim(), password: $('#p').value });
            await loadMe();
            location.hash = '#/';
          } catch (err) {
            $('#msg').innerHTML = `<div class="note err">${esc(errText(err))}</div>`;
          }
        });
      });
      $('#u').focus();
    },
  };

  const Register = {
    async render(root) {
      root.innerHTML = authShell(`<form class="card form" id="f" novalidate>
        <div><h1>${t('注册账户')}</h1><p class="sub">${t('需要管理员发放的邀请码')}</p></div>
        <div class="field"><label for="c">${t('邀请码')}</label><input id="c" type="text" autocomplete="off" maxlength="64" required>
          <div class="help">${t('邀请码只用于获得注册资格，不是登录密码，也不会关联你的 Steam 账号。')}</div></div>
        <div class="field"><label for="u">${t('用户名')}</label><input id="u" type="text" autocomplete="username" maxlength="32" required>
          <div class="help">${t('3–32 位，可用字母、数字、下划线、短横线或中文。')}</div></div>
        <div class="field"><label for="p">${t('密码')}</label><input id="p" type="password" autocomplete="new-password" maxlength="128" required>
          <div class="help">${t('至少 10 个字符。不要使用你的 Steam 密码。')}</div></div>
        <div class="field"><label for="p2">${t('确认密码')}</label><input id="p2" type="password" autocomplete="new-password" maxlength="128" required></div>
        <div id="msg"></div>
        <button class="btn primary block" type="submit">${t('注册并登录')}</button>
        <div class="alt">${t('已有账户？')} <a href="#/login">${t('登录')}</a></div></form>`);
      $('#f').addEventListener('submit', async (e) => {
        e.preventDefault();
        const msg = $('#msg');
        if ($('#p').value !== $('#p2').value) { msg.innerHTML = `<div class="note err">${t('两次输入的密码不一致。')}</div>`; return; }
        await busy($('button[type=submit]', e.target), async () => {
          try {
            await api('POST', '/api/auth/register', { invite_code: $('#c').value, username: $('#u').value.trim(), password: $('#p').value });
            await loadMe();
            toast(t('注册成功，欢迎！'), 'ok');
            location.hash = S.features.user_connections ? '#/connections' : '#/';
          } catch (err) {
            msg.innerHTML = `<div class="note err">${esc(errText(err))}</div>`;
          }
        });
      });
    },
  };

  const STATUS_TABS = [['running', '运行中'], ['queued', '排队中'], ['completed', '已完成'], ['stopped', '已停止'], ['failed', '失败']];

  const Overview = {
    async render(root) {
      this.root = root;
      root.innerHTML = `<div class="boot">${t('正在读取…')}</div>`;
      await this.refresh();
    },
    async refresh() {
      let d, np;
      try { [d, np] = await Promise.all([api('GET', '/api/overview'), api('GET', '/api/now-playing')]); } catch (e) {
        this.root.innerHTML = `<div class="note err">${esc(errText(e))}</div>`; return;
      }
      this.root.innerHTML = this.view(d, np.bots);
    },
    view(d, playing) {
      const top = head(t('总览'), t('卡牌挂机和游戏时长一览，每 15 秒自动刷新。'));
      if (!d.targets.length) {
        return top + noTargetsHtml(t('先添加你自己运行的 ASF（ArchiSteamFarm）的 IPC 地址。连接成功后，就能在这里管理卡牌挂机和游戏时长。'));
      }
      const active = playing.filter((b) => b.active);
      const c = d.task_counts;
      const bots = d.farming.flatMap((x) => x.bots);
      const farmingNow = bots.filter((b) => b.state === 'farming').length;
      const stats = `<div class="stats">
        ${STATUS_TABS.map(([k, label]) => `<div class="stat"><div class="num">${c[k] || 0}</div><div class="lbl">${t(label)}</div></div>`).join('')}
        <div class="stat"><div class="num">${farmingNow}<span class="of"> / ${bots.length}</span></div><div class="lbl">${t('挂卡中的 Bot')}</div></div></div>`;
      const nowPanel = `<section class="panel"><div class="panel-head"><h2>${t('现在在挂')}</h2><a class="btn sm" href="#/playtime">${t('新建时长任务')}</a></div>
        ${active.length ? nowPlayingHtml(active, '') : `<p class="muted">${t('现在没有在挂任何游戏。')}</p>`}</section>`;

      const farmRows = d.farming.flatMap((x) => {
        if (x.error) {
          return [`<div class="row no-check"><div><div class="title">${esc(x.connection.name)}</div><div class="sub"><span class="tag farm">${t('卡牌挂机')}</span></div></div>
            <div>${badge('err', t('无法读取'))}</div><div class="sub">${esc(x.error.message)} ${esc(x.error.hint || '')}</div><div></div></div>`];
        }
        return x.bots.map((b) => {
          const f = b.farming;
          const cur = f.current_games.length ? f.current_games.map((g) => gameHtml(g.app_id, g.name)).join(sep()) : '—';
          const prog = f.games_to_farm_count
            ? `${t('待挂 %d 个游戏', f.games_to_farm_count)}${f.cards_remaining_total != null ? t('，剩余 %d 张卡', f.cards_remaining_total) : ''}${f.time_remaining_seconds != null ? `<br><span class="muted">${t('ASF 估算剩余 %s', fmtDur(f.time_remaining_seconds))}</span>` : ''}`
            : `<span class="muted">${t('暂无待挂卡游戏')}</span>`;
          const skip = b.skip.count == null ? t('跳过列表未读取') : t('跳过 %d 个游戏', b.skip.count);
          return `<div class="row no-check">
            <div><div class="title">${cur}</div><div class="sub"><span class="tag farm">${t('卡牌挂机')}</span> ${esc(x.connection.name)} · ${esc(b.name)}</div></div>
            <div>${badge(FARM_KIND[b.state] || 'muted', b.state_text)}</div>
            <div class="sub">${prog}<br>${esc(skip)}</div>
            <div></div></div>`;
        });
      });
      const list = d.tasks.map((x) => taskRow(x, false)).concat(farmRows);
      const tasksPanel = `<section class="panel"><div class="panel-head"><h2>${t('任务与挂机状态')}</h2>
        <div class="btn-row"><a class="btn sm" href="#/farming">${t('卡牌挂机')}</a><a class="btn sm" href="#/playtime">${t('新建时长任务')}</a></div></div>
        ${list.length ? `<div class="rows">${list.join('')}</div>` : `<div class="empty"><p>${t('还没有任务。')}</p>
          <a class="btn primary" href="#/playtime">${t('创建第一个游戏时长任务')}</a></div>`}</section>`;
      return top + stats + nowPanel + tasksPanel;
    },
  };

  const targetText = (sec) => (sec ? fmtDur(sec) : t('一直挂（手动停止）'));

  function matches(q, ...fields) {
    if (!q) return true;
    return fields.some((f) => String(f ?? '').toLowerCase().includes(q));
  }

  const openLists = new Set();

  function nowPlayingHtml(list, q) {
    const shown = list.filter((b) => matches(q, b.bot, b.connection.name,
      ...b.playtime.flatMap((x) => [x.app_name, x.app_id, x.note]), ...b.farming_games.flatMap((g) => [g.name, g.app_id]),
      ...(b.config_games || []).flatMap((g) => [g.name, g.app_id])));
    if (!shown.length) return `<p class="muted">${q ? t('没有匹配的结果。') : t('还没有可用的 Bot。')}</p>`;
    return `<div class="bot-grid">${shown.map((b) => {
      const key = `${b.connection.id}|${b.bot}`;
      const games = b.playtime.map((x) => ({ name: x.app_name, id: x.app_id,
        extra: `${x.status === 'queued' ? t('排队中') : `${t('已挂 %s', fmtDur(x.elapsed_seconds))}${x.target_seconds ? ` / ${fmtDur(x.target_seconds)}` : ''}`}${x.timing !== 'accruing' && x.timing_text ? ` · ${x.timing_text}` : ''}${x.owner ? ` · ${t('%s 的任务', x.owner)}` : ''}` }))
        .concat((b.config_games || []).map((g) => ({ name: g.name, id: g.app_id, extra: '' })));
      const preview = games.slice(0, 2).map((g) => esc(g.name || `App ${g.id}`)).join(sep()) + (games.length > 2 ? ' …' : '');
      const body = games.length
        ? `<details class="games" data-key="${esc(key)}" ${openLists.has(key) ? 'open' : ''}><summary><span class="count">${games.length}</span>${preview}</summary>
            <ul class="plain">${games.map((g) => `<li>${gameHtml(g.id, g.name)}${g.extra ? `<div class="muted small">${esc(g.extra)}</div>` : ''}</li>`).join('')}</ul></details>`
        : `<p class="muted small">${esc(b.error || t('没有挂时长的游戏'))}</p>`;
      const farm = b.farming_games.length
        ? `<div class="small"><span class="tag farm">${t('挂卡')}</span> ${b.farming_games.map((g) => `${gameHtml(g.app_id, g.name)}${g.cards_remaining != null ? ` <span class="muted">${t('剩 %d 张卡', g.cards_remaining)}</span>` : ''}`).join(sep())}</div>` : '';
      return `<article class="bot"><header><span><span class="name">${esc(b.bot)}${assignedTag(b.assigned_to)}</span><span class="muted small conn">${esc(b.connection.name)}</span></span>
        ${badge(FARM_KIND[b.state] || 'muted', b.state_text)}</header>${body}${farm}
        <footer><button class="btn sm" data-act="idleedit" data-cid="${b.connection.id}" data-bot="${esc(b.bot)}">${t('管理')}</button></footer></article>`;
    }).join('')}</div>`;
  }

  const ONLINE_STATUS = ['离线', '在线', '忙碌', '离开', '打盹', '想交易', '想玩游戏', '隐身'];
  const TASK_HINT = '删掉带“任务”标记的游戏，会同时停止它的时长任务。';

  async function openBotSettings(connectionId, botName) {
    let d;
    try { d = await api('GET', `/api/bots/settings?connection_id=${connectionId}&bot=${encodeURIComponent(botName)}`); } catch (err) { showError(err); return false; }
    let games = d.games.map((g) => ({ ...g }));
    const m = openModal({
      title: t('管理：%s', botName),
      wide: true,
      body: `<div class="form">
        <div id="spn"></div>
        <div class="field"><label>${t('挂时长的游戏')} <span class="muted small" id="sgc"></span></label><div class="chips" id="sg"></div>
          <div class="inline"><input id="sa" type="text" autocomplete="off" placeholder="${t('App ID 或商店链接，可以一次填多个')}"><button class="btn sm" data-add>${t('添加')}</button></div>
          <div class="help" id="sah">${t(TASK_HINT)}</div></div>
        <div class="form-grid">
          <div class="field"><label for="so">${t('在线状态')}</label><select id="so">${ONLINE_STATUS.map((x, i) => `<option value="${i}">${t(x)}</option>`).join('')}</select>
            <div class="help">${t('“离线”时游戏时长照样计算，但 ASF 收不到 Steam 聊天命令。“隐身”适合小号。')}</div></div>
          <div class="field"><label for="si">${t('挂时长时显示的文字')}</label><input id="si" type="text" maxlength="64" placeholder="${t('留空则不显示')}">
            <div class="help">${t('好友会看到“正在玩 非 Steam 游戏：这段文字”。设置后最多挂 31 个游戏。')}</div></div>
          <div class="field"><label for="sf">${t('挂卡时显示的文字')}</label><input id="sf" type="text" maxlength="64" placeholder="${t('留空则不显示')}">
            <div class="help">${t('可以用 {0} 表示 App ID，{1} 表示游戏名。')}</div></div>
        </div>
        <p class="muted small">${t('保存后会写入 ASF 配置，这个 Bot 会重连几秒；没有改动时不会写入。')}</p></div>`,
      actions: [{ label: t('取消'), value: null }, { label: t('保存'), kind: 'primary', onClick: (btn, close) => busy(btn, () => save(close)) }],
    });
    const fill = () => {
      $('#so', m.el).value = String(d.online_status);
      $('#si', m.el).value = d.custom_idle;
      $('#sf', m.el).value = d.custom_farming;
      $('#spn', m.el).innerHTML = d.pending ? `<div class="note warn">${t('上一次保存还在确认中，系统会自动补完。')}</div>` : '';
    };
    const draw = () => {
      const limit = $('#si', m.el).value.trim() ? 31 : 32;
      const orig = new Set(d.games.map((g) => g.app_id));
      $('#sgc', m.el).textContent = `${games.length} / ${limit}`;
      $('#sg', m.el).innerHTML = games.length ? games.map((g) => `<span class="chip">${gameHtml(g.app_id, g.name)}
          ${g.task ? `<span class="tag play">${g.task.unlimited ? t('任务') : `${t('任务')} ${fmtDur(g.task.target_seconds)}`}</span>` : ''}${orig.has(g.app_id) ? '' : `<span class="tag">${t('新')}</span>`}
          <button class="x" data-rm="${g.app_id}" aria-label="${t('移除')}" title="${t('移除')}">×</button></span>`).join('')
        : `<p class="muted small">${t('还没有游戏。')}</p>`;
    };
    const diff = () => ({
      add: games.filter((g) => !d.games.some((x) => x.app_id === g.app_id)),
      remove: d.games.filter((g) => !games.some((x) => x.app_id === g.app_id)),
    });
    const build = (confirmIds) => {
      const { add, remove } = diff();
      const body = { connection_id: connectionId, bot: botName, add_games: add.map((g) => String(g.app_id)),
        remove_games: remove.map((g) => g.app_id), confirm_stop_task_ids: confirmIds };
      const st = Number($('#so', m.el).value);
      if (st !== d.online_status) body.online_status = { value: st, base: d.online_status };
      const ci = $('#si', m.el).value.trim();
      if (ci !== d.custom_idle) body.custom_idle = { value: ci, base: d.custom_idle };
      const cf = $('#sf', m.el).value.trim();
      if (cf !== d.custom_farming) body.custom_farming = { value: cf, base: d.custom_farming };
      return body;
    };
    const taskList = (tasks) => `<ul>${tasks.map((x) => `<li>${gameHtml(x.app_id, x.app_name || x.name)}</li>`).join('')}</ul>`;
    async function save(close) {
      let body = build([]);
      if (!body.add_games.length && !body.remove_games.length && !body.online_status && !body.custom_idle && !body.custom_farming) {
        toast(t('没有需要保存的修改。'));
        close(false);
        return;
      }
      const local = diff().remove.filter((g) => g.task);
      if (local.length) {
        if (!await confirmBox(t('同时停止时长任务'), `<p>${t('这些游戏有进行中的时长任务，删除后任务会停止：')}</p>${taskList(local)}`, { confirm: t('删除并停止'), danger: true })) return;
        body = build(local.map((g) => g.task.id));
      }
      let r;
      for (let attempt = 0; attempt < 2 && !r; attempt++) {
        try {
          r = await api('POST', '/api/bots/settings', body);
        } catch (err) {
          if (err.code === 'confirm_required' && attempt === 0) {
            if (!await confirmBox(t('需要确认'), `<p>${esc(err.message)}</p>${taskList(err.tasks || [])}`, { confirm: t('删除并停止'), danger: true })) return;
            body = build((err.tasks || []).map((x) => x.id));
            continue;
          }
          if (err.code === 'settings_conflict' && err.settings) {
            const { add, remove } = diff();
            d = err.settings;
            const removedIds = new Set(remove.map((g) => g.app_id));
            games = d.games.filter((g) => !removedIds.has(g.app_id)).map((g) => ({ ...g }));
            for (const g of add) if (!games.some((x) => x.app_id === g.app_id)) games.push(g);
            fill();
            draw();
          }
          showError(err);
          return;
        }
      }
      if (!r) return;
      if (r.pending) toast(r.message, 'info', 10000);
      else if (!r.changed) toast(r.message || t('没有需要保存的修改。'));
      else toast(r.refresh_error || r.message || t('已保存到 ASF 配置。'), 'ok', 6000);
      close(true);
    }
    m.el.addEventListener('click', async (e) => {
      const rm = e.target.closest('[data-rm]');
      if (rm) { games = games.filter((g) => g.app_id !== Number(rm.dataset.rm)); draw(); return; }
      if (!e.target.closest('[data-add]')) return;
      const { ids, bad } = parseMany($('#sa', m.el).value);
      const h = $('#sah', m.el);
      if (bad.length) { h.className = 'help err'; h.textContent = t('无法识别：%s', bad.slice(0, 3).join(sep())); return; }
      const fresh = ids.filter((id) => !games.some((g) => g.app_id === id));
      for (const id of fresh) games.push({ app_id: id, name: null, task: null });
      $('#sa', m.el).value = '';
      h.className = 'help';
      h.textContent = t(TASK_HINT);
      draw();
      for (const id of fresh.slice(0, 10)) {
        try {
          const r = await api('GET', `/api/apps/resolve?q=${id}`);
          const g = games.find((x) => x.app_id === id);
          if (g && r.name) { g.name = r.name; draw(); }
        } catch (err) { /* name is optional */ }
      }
    });
    $('#si', m.el).addEventListener('input', draw);
    fill();
    draw();
    return (await m.done) === true;
  }

  async function openCodeDialog(connectionId, botName, requiredInput) {
    const twoFactor = requiredInput === 'two_factor';
    const m = openModal({
      title: t('输入验证码：%s', botName),
      body: `<div class="form">
        <div class="field"><label for="ck">${t('验证码类型')}</label><select id="ck">
          <option value="two_factor" ${twoFactor ? 'selected' : ''}>${t('手机令牌（Steam App 里的 5 位码）')}</option>
          <option value="steam_guard" ${twoFactor ? '' : 'selected'}>${t('邮箱验证码（Steam 发到邮箱的 5 位码）')}</option></select></div>
        <div class="field"><label for="cv">${t('验证码')}</label><input id="cv" type="text" maxlength="5" autocomplete="one-time-code" placeholder="${t('5 位字母或数字')}"></div>
        <p class="muted small">${t('提交后 ASF 会用这个码重新登录。令牌码约 30 秒变一次，请输入最新的。')}</p></div>`,
      actions: [{ label: t('取消'), value: null }, {
        label: t('提交'), kind: 'primary', onClick: (btn, close) => busy(btn, async () => {
          try {
            const r = await api('POST', '/api/bots/code', { connection_id: connectionId, bot: botName, kind: $('#ck').value, code: $('#cv').value });
            toast(r.message, 'ok', 6000);
            close(true);
          } catch (err) { showError(err); }
        }),
      }],
    });
    setTimeout(() => { const i = $('#cv'); if (i) i.focus(); }, 0);
    return m.done;
  }

  async function openQrDialog(connectionId, botName, start) {
    const m = openModal({
      title: t('扫码登录：%s', botName),
      body: `<div id="qrbox" class="center muted">${t('正在生成二维码…')}</div>
        <p class="muted small">${t('打开手机 Steam App → 右下角“Steam 令牌” → “扫描二维码”。二维码会定期更新，页面会自动刷新。')}</p>`,
      actions: [{ label: t('关闭'), value: null }],
    });
    if (start) {
      try { await api('POST', '/api/bots/qr-start', { connection_id: connectionId, bot: botName }); } catch (err) {
        $('#qrbox', m.el).innerHTML = `<div class="note err">${esc(errText(err))}</div>`; return m.done;
      }
    }
    const began = Date.now();
    const poll = async () => {
      if (!m.el.isConnected) return;
      try {
        const r = await api('GET', `/api/bots/qr?connection_id=${connectionId}&bot=${encodeURIComponent(botName)}`);
        const box = $('#qrbox', m.el);
        if (!box) return;
        if (r.online) { m.close(true); toast(t('「%s」登录成功。', botName), 'ok'); return; }
        if (r.qr) box.innerHTML = `<img class="qr" src="${r.qr}" alt="QR">`;
        else if (Date.now() - began > 30000) {
          box.innerHTML = `<div class="note warn">${t('ASF 没有给出二维码。可能是 ASF 开启了 Headless，或这个账号需要密码 / 验证码登录。可以关闭后改用“输入验证码”。')}</div>`;
        }
      } catch (err) { /* retry */ }
      if (Date.now() - began < 5 * 60000) setTimeout(poll, 3000);
    };
    setTimeout(poll, 1500);
    return m.done;
  }

  async function openNewBotDialog(assignUsers) {
    let info;
    try { info = await api('GET', '/api/bots/mine'); } catch (err) { showError(err); return null; }
    if (!info.hosts.length) { toast(S.me.is_admin ? t('请先在“ASF 连接”中添加并启用你的 ASF。') : t('管理员还没有开放新建 Bot。'), 'error'); return null; }
    const left = info.quota < 0 ? null : info.quota - info.used;
    if (left !== null && left <= 0) { toast(t('你的新建额度（%d 个）已经用完。', info.quota), 'error'); return null; }
    const m = openModal({
      title: t('新建 Bot'),
      body: `<div class="form">
        ${info.hosts.length > 1 ? `<div class="field"><label for="nc">${t('添加到')}</label><select id="nc">${info.hosts.map((h) => `<option value="${h.connection_id}">${esc(h.name)}</option>`).join('')}</select></div>`
          : `<input type="hidden" id="nc" value="${info.hosts[0].connection_id}">`}
        <div class="field"><label for="nn">${t('Bot 名称')}</label><input id="nn" type="text" maxlength="32" autocomplete="off" placeholder="mybot">
          <div class="help">${t('字母、数字、下划线或短横线。')}</div></div>
        <div class="field"><label>${t('登录方式')}</label><div class="seg">
          <label class="check"><input type="radio" name="nm" value="qr"> ${t('扫码（推荐，不用填密码）')}</label>
          <label class="check"><input type="radio" name="nm" value="password"> ${t('账号密码')}</label></div>
          <div class="help" id="nmh"></div></div>
        <div class="field"><label for="nl">${t('Steam 账号名')}</label><input id="nl" type="text" maxlength="64" autocomplete="off">
          <div class="help" id="nlh"></div></div>
        <div class="field" id="npf"><label for="np">${t('Steam 密码')}</label><input id="np" type="password" maxlength="256" autocomplete="new-password">
          <div class="help">${t('会保存在 ASF 的 Bot 配置里（ASF 管理员能看到），本面板不保存。')}</div></div>
        ${assignUsers ? `<div class="field"><label for="na">${t('分配给')}</label><select id="na"><option value="">${t('不分配（我自己管理）')}</option>
          ${assignUsers.filter((u) => u.is_active).map((u) => `<option value="${u.id}">${esc(u.username)}</option>`).join('')}</select></div>` : ''}
        ${left !== null ? `<p class="muted small">${t('你还可以新建 %d 个 Bot。', left)}</p>` : ''}</div>`,
      actions: [{ label: t('取消'), value: null }, {
        label: t('创建并登录'), kind: 'primary', onClick: (btn, close) => busy(btn, async () => {
          const method = ($('input[name=nm]:checked', m.el) || {}).value;
          const body = { connection_id: Number($('#nc', m.el).value), name: $('#nn', m.el).value.trim(), method,
            steam_login: $('#nl', m.el).value.trim(), steam_password: method === 'password' ? $('#np', m.el).value : '',
            assign_to: $('#na', m.el) && $('#na', m.el).value ? Number($('#na', m.el).value) : null };
          if (!body.name) { toast(t('请填写 Bot 名称。'), 'error'); return; }
          if (method === 'password' && (!body.steam_login || !body.steam_password)) { toast(t('请填写 Steam 账号名和密码。'), 'error'); return; }
          try { close(await api('POST', '/api/bots', body)); } catch (err) { showError(err); }
        }),
      }],
    });
    const hostOf = () => info.hosts.find((h) => h.connection_id === Number($('#nc', m.el).value)) || info.hosts[0];
    const sync = () => {
      const qrOk = hostOf().qr_supported;
      const qrRadio = $('input[value=qr]', m.el);
      qrRadio.disabled = !qrOk;
      if (!qrOk && qrRadio.checked) $('input[value=password]', m.el).checked = true;
      if (!$('input[name=nm]:checked', m.el)) $(`input[value=${qrOk ? 'qr' : 'password'}]`, m.el).checked = true;
      const method = $('input[name=nm]:checked', m.el).value;
      $('#nmh', m.el).textContent = qrOk ? '' : t('这个 ASF 开启了 Headless，不支持扫码，只能用账号密码。');
      $('#npf', m.el).hidden = method !== 'password';
      $('#nlh', m.el).textContent = method === 'qr' ? t('可不填；填了的话，扫码的账号必须是它。') : t('登录 Steam 用的账号名，不是昵称或邮箱。');
    };
    m.el.addEventListener('change', sync);
    sync();
    const r = await m.done;
    if (!r) return null;
    if (!r.loaded) {
      openModal({ title: t('已写入 ASF'), body: `<div class="note warn">${t('Bot「%s」的配置已写入 ASF，但 ASF 没有自动加载它。可能是 ASF 以 --no-config-watch 启动，请让管理员重启 ASF。', esc(r.bot))}</div>` });
    } else if (r.method === 'qr') {
      await openQrDialog(r.connection_id, r.bot, false);
    } else if (r.needs_input) {
      toast(t('Bot「%s」已创建，Steam 需要验证码。', r.bot), 'ok', 6000);
      await openCodeDialog(r.connection_id, r.bot, r.required_input);
    } else {
      toast(`${r.online ? t('Bot「%s」已创建并已登录。', r.bot) : t('Bot「%s」已创建，正在登录。', r.bot)} ${(r.warnings || []).join(' ')}`, 'ok', 6000);
    }
    return r;
  }

  const helpBox = (items) => `<details class="note help-box"><summary>${t('这些按钮是做什么的？')}</summary><dl class="kv">${items.map(([k, v]) =>
    `<dt>${t(k)}</dt><dd>${t(v)}</dd>`).join('')}</dl></details>`;
  const HELP = {
    farming: [
      ['启动挂卡', '让 ASF 自动挂还有卡牌可掉的游戏，掉完自动换下一个。'],
      ['停止挂卡', '停止自动挂卡，直到你再点“启动挂卡”。ASF 重启后会按 Bot 自己的配置重新开始。'],
      ['暂停一段时间', '暂停若干分钟，到时 ASF 自动继续挂卡。'],
      ['跳过列表', '列表里的游戏不参与卡牌挂机；不影响游戏时长任务。'],
      ['启动 Bot / 停止 Bot', '让这个 Steam 账号在 ASF 里上线 / 下线。下线后什么都不会挂。'],
      ['输入验证码', 'Steam 要求邮箱验证码或手机令牌时，在这里填写。'],
      ['扫码登录', 'Bot 掉线或要重新登录时，用手机 Steam App 扫码（ASF 开启 Headless 时不可用）。'],
      ['删除 Bot', '从 ASF 中彻底删除这个 Bot（只有创建者或管理员能删）。'],
      ['挂卡和挂时长的顺序', 'ASF 先挂卡，卡挂完后才挂时长游戏。想先挂时长，点“停止挂卡”。'],
      ['勾选框', '勾选多个 Bot 后，可以一起启动或停止挂卡。'],
    ],
    playtime: [
      ['检查并开始', '先显示确认信息，确认后立即开始挂。游戏可以一次填多个；时长两项都填 0 表示一直挂，直到你手动停止。'],
      ['管理', '直接增删这个 Bot 挂时长的游戏，设置在线状态和好友看到的自定义文字（都保存在 ASF 配置里）。'],
      ['新建 Bot', '在 ASF 里添加一个 Steam 账号，可以扫码或用账号密码登录（需要管理员给你额度）。'],
      ['停止', '结束这个任务。停止后不能继续，需要的话重新建一个。'],
      ['停止所选', '勾选多个任务后一次停止。'],
      ['搜索', '按游戏名、App ID、Bot 名或备注筛选“现在在挂”和任务列表。'],
    ],
    connections: [
      ['重新测试', '重新连接 ASF 并刷新 Bot 列表。在 ASF 里新增或删除了 Bot 后点它。'],
      ['编辑', '修改名称、地址或 IPC 密码（留空的项不修改）。'],
      ['断开', '暂时停用这个连接。进行中的时长任务会停止，之后可以“重新启用”。'],
      ['删除', '永久删除这个连接和它的任务记录，不影响 ASF 本身。'],
    ],
  };

  function taskRow(x, withCheck, selected) {
    const active = x.status === 'running' || x.status === 'queued';
    const pct = Math.min(1, x.progress || 0);
    const timing = x.timing_text ? `<div class="sub">${esc(x.timing_text)}</div>` : '';
    const end = x.stop_reason_text ? `<div class="sub">${esc(x.stop_reason_text)}${x.finished_at ? ' · ' + fmtTime(x.finished_at) : ''}</div>` : '';
    const err = x.last_error ? `<div class="err-line ${active ? 'warn' : ''}">${esc(x.last_error)}</div>` : '';
    const note = x.note ? `<div class="sub">${t('备注：%s', esc(x.note))}</div>` : '';
    const check = withCheck
      ? `<div>${active ? `<input type="checkbox" aria-label="${t('选择任务')}" data-sel="${x.id}" ${selected ? 'checked' : ''}>` : ''}</div>` : '';
    return `<div class="row ${withCheck ? '' : 'no-check'}">${check}
      <div><div class="title">${gameHtml(x.app_id, x.app_name)}</div>
        <div class="sub"><span class="tag play">${t('游戏时长')}</span> ${esc(x.connection.name)} · ${esc(x.bot)}${x.owner ? ` · <span class="tag owner">${esc(t('%s 的任务', x.owner))}</span>` : ''}</div>${note}</div>
      <div>${badge(TASK_KIND[x.status] || 'muted', x.status_text)}${timing}${end}</div>
      <div>${x.unlimited
        ? `<div class="sub"><strong>${t('已挂 %s', fmtDur(x.elapsed_seconds))}</strong> · ${t('一直挂，直到手动停止')}</div>`
        : `<progress max="1" value="${pct}" class="${x.status === 'completed' ? 'done' : ''}"></progress>
        <div class="sub">${t('已挂 %s / 目标 %s', fmtDur(x.elapsed_seconds), fmtDur(x.target_seconds))}${active ? ` · ${t('剩余 %s', fmtDur(x.remaining_seconds))}` : ''}</div>`}
        <div class="sub">${t('更新于 %s', fmtTime(x.updated_at))}</div></div>
      <div>${withCheck && active ? `<button class="btn sm danger" data-act="stop" data-id="${x.id}">${t('停止')}</button>` : ''}</div>${err}</div>`;
  }

  const Farming = {
    sel: new Set(),
    data: null,
    async render(root) {
      this.root = root;
      root.innerHTML = `${head(t('卡牌挂机'), t('ASF 会自动“玩”还有集换式卡牌可掉落的游戏，直到卡牌掉完。'), `<button class="btn" id="reload">${t('刷新')}</button>`)}
        ${helpBox(HELP.farming)}
        <div id="list"><div class="boot">${t('正在从 ASF 读取状态…')}</div></div><div id="batch"></div>`;
      $('#reload').addEventListener('click', (e) => busy(e.target, () => this.refresh(false, true)));
      root.addEventListener('click', (e) => this.onClick(e));
      root.addEventListener('change', (e) => {
        const cb = e.target.closest('[data-sel]');
        if (!cb) return;
        if (cb.checked) this.sel.add(cb.dataset.sel); else this.sel.delete(cb.dataset.sel);
        this.renderBatch();
      });
      await this.refresh();
    },
    async refresh(auto, force) {
      let d;
      try { d = await api('GET', `/api/farming${force ? '?refresh=1' : ''}`); } catch (e) {
        $('#list', this.root).innerHTML = `<div class="note err">${esc(errText(e))}</div>`; return;
      }
      this.data = d.connections;
      const valid = new Set(this.data.flatMap((c) => c.bots.map((b) => `${c.connection.id}|${b.name}`)));
      for (const k of [...this.sel]) if (!valid.has(k)) this.sel.delete(k);
      $('#list', this.root).innerHTML = this.view();
      this.renderBatch();
    },
    find(key) {
      const [cid, name] = [Number(key.split('|')[0]), key.slice(key.indexOf('|') + 1)];
      const c = this.data.find((x) => x.connection.id === cid);
      return c ? { conn: c.connection, bot: c.bots.find((b) => b.name === name) } : {};
    },
    view() {
      if (!this.data.length) return noTargetsHtml(t('添加连接后，这里会显示该 ASF 下的 Bot。'));
      return this.data.map((c) => {
        const h = `<div class="panel-head"><h2>${esc(c.connection.name)}</h2>
          ${c.bots.length > 1 ? `<button class="btn sm" data-act="skip" data-cid="${c.connection.id}">${t('这里全部 Bot 的跳过列表')}</button>` : ''}</div>`;
        if (c.error) return `<section class="panel">${h}<div class="note err">${esc(c.error.message)} ${esc(c.error.hint || '')}</div></section>`;
        if (!c.bots.length) return `<section class="panel">${h}<div class="note">${c.connection.owned
          ? t('这个连接下还没有可管理的 Bot（已分配给其他用户的 Bot 不在这里显示）。若刚在 ASF 中添加了 Bot，请到“ASF 连接”重新测试。')
          : t('暂时没有可用的 Bot。')}</div></section>`;
        const missing = c.missing_bots && c.missing_bots.length
          ? `<div class="note warn">${t('ASF 未返回这些 Bot：%s。', esc(c.missing_bots.join(sep())))}${c.connection.owned ? t('请到“ASF 连接”重新测试以刷新列表。') : t('请联系管理员。')}</div>` : '';
        return `<section class="panel">${h}${missing}<div class="bot-grid">${c.bots.map((b) => this.botCard(c.connection, b)).join('')}</div></section>`;
      }).join('');
    },
    botCard(conn, b) {
      const key = `${conn.id}|${b.name}`;
      const f = b.farming;
      const k = esc(key);
      const cur = f.current_games.length
        ? f.current_games.map((g) => `${gameHtml(g.app_id, g.name)}${g.cards_remaining != null ? ` · ${t('剩 %d 张卡', g.cards_remaining)}` : ''}`).join('<br>') : '—';
      const btns = [];
      if (b.playtime.managed) {
        btns.push(b.playtime.resume_after
          ? `<button class="btn sm" data-act="farm_stop" data-key="${k}">${t('改为：结束后不挂卡')}</button>`
          : `<button class="btn sm" data-act="farm_start" data-key="${k}">${t('改为：结束后继续挂卡')}</button>`);
      } else if (b.online) {
        if (f.paused) btns.push(`<button class="btn sm primary" data-act="farm_start" data-key="${k}">${t('启动挂卡')}</button>`);
        else {
          btns.push(`<button class="btn sm" data-act="farm_stop" data-key="${k}">${t('停止挂卡')}</button>`);
          btns.push(`<button class="btn sm" data-act="farm_pause_temp" data-key="${k}">${t('暂停一段时间')}</button>`);
        }
      }
      btns.push(`<button class="btn sm" data-act="skip" data-cid="${conn.id}" data-bot="${esc(b.name)}">${t('跳过列表')}${b.skip.count != null ? ` · ${b.skip.count}` : ''}</button>`);
      if (b.online || b.keep_running) btns.push(`<button class="btn sm" data-act="idleedit" data-cid="${conn.id}" data-bot="${esc(b.name)}">${t('管理挂时长')}</button>`);
      if (b.needs_input || !b.online) {
        btns.push(`<button class="btn sm${b.needs_input ? ' primary' : ''}" data-act="code" data-key="${k}">${t('输入验证码')}</button>`);
        btns.push(`<button class="btn sm" data-act="qr" data-key="${k}">${t('扫码登录')}</button>`);
      }
      if (!b.online && !b.keep_running) btns.push(`<button class="btn sm" data-act="bot_start" data-key="${k}">${t('启动 Bot')}</button>`);
      if ((b.online || b.keep_running) && !b.playtime.managed) btns.push(`<button class="btn sm ghost danger" data-act="bot_stop" data-key="${k}">${t('停止 Bot')}</button>`);
      if (b.deletable) btns.push(`<button class="btn sm ghost danger" data-act="delbot" data-key="${k}">${t('删除 Bot')}</button>`);
      const pt = b.playtime.managed
        ? `<div class="note">${t('有 %d 个游戏时长任务正在使用这个 Bot，挂卡已暂停。', b.playtime.playtime_tasks)}${b.playtime.resume_after ? t('任务全部结束后会继续挂卡。') : t('任务全部结束后不会挂卡。')}</div>` : '';
      return `<article class="bot">
        <header><label class="check"><input type="checkbox" data-sel="${k}" ${this.sel.has(key) ? 'checked' : ''} aria-label="${esc(b.name)}">
          <span><span class="name">${esc(b.name)}${assignedTag(b.assigned_to)}</span>${b.nickname ? `<span class="muted small conn">${esc(b.nickname)}</span>` : ''}</span></label>
          ${badge(FARM_KIND[b.state] || 'muted', b.state_text)}</header>
        <dl>
          <dt>${t('在线状态')}</dt><dd>${b.online ? t('在线') : (b.keep_running ? t('正在连接') : t('离线'))}</dd>
          <dt>${t('当前挂卡')}</dt><dd>${cur}</dd>
          <dt>${t('待挂卡')}</dt><dd>${f.games_to_farm_count ? `${t('%d 个游戏', f.games_to_farm_count)}${f.cards_remaining_total != null ? ` · ${t('共剩 %d 张卡', f.cards_remaining_total)}` : ''}` : t('无')}</dd>
          ${f.time_remaining_seconds != null ? `<dt>${t('预计剩余')}</dt><dd>${fmtDur(f.time_remaining_seconds)} <span class="muted small">${t('（ASF 估算）')}</span></dd>` : ''}
          ${(b.idle_games || []).length ? `<dt>${t('挂时长')}</dt><dd>${b.idle_games.map((g) => gameHtml(g.app_id, g.name)).join(sep())}</dd>` : ''}
          <dt>${t('跳过列表')}</dt><dd>${b.skip.count == null ? `<span class="muted">${t('未读取')}</span>` : `${t('%d 个游戏', b.skip.count)} <span class="muted small">${fmtTime(b.skip.checked_at)}</span>`}</dd>
        </dl>${b.needs_input ? `<div class="note warn">${t('Steam 需要验证码才能登录，请点下面的“输入验证码”。')}</div>` : ''}${pt}
        <footer>${btns.join('')}</footer></article>`;
    },
    renderBatch() {
      const el = $('#batch', this.root);
      if (!this.sel.size) { el.innerHTML = ''; return; }
      el.innerHTML = `<div class="batchbar"><span class="grow">${t('已选择 %d 个 Bot', this.sel.size)}</span>
        <button class="btn sm" data-act="batch" data-op="farm_start">${t('批量启动挂卡')}</button>
        <button class="btn sm" data-act="batch" data-op="farm_stop">${t('批量停止挂卡')}</button>
        <button class="btn sm" data-act="clear">${t('取消选择')}</button></div>`;
    },
    async onClick(e) {
      const b = e.target.closest('[data-act]');
      if (!b) return;
      const act = b.dataset.act;
      if (act === 'idleedit') return;
      if (act === 'clear') { this.sel.clear(); $$('[data-sel]', this.root).forEach((x) => { x.checked = false; }); this.renderBatch(); return; }
      if (act === 'skip') { await openSkip(Number(b.dataset.cid), b.dataset.bot || null); this.refresh(); return; }
      if (act === 'batch') { await this.batch(b, b.dataset.op); return; }
      const { conn, bot } = this.find(b.dataset.key);
      if (!bot) return;
      if (act === 'code') { await openCodeDialog(conn.id, bot.name, bot.required_input); await this.refresh(false, true); return; }
      if (act === 'qr') { await openQrDialog(conn.id, bot.name, true); await this.refresh(false, true); return; }
      if (act === 'delbot') {
        if (!await confirmBox(t('删除 Bot'), `<p>${t('从 ASF 中彻底删除 Bot「%s」？', esc(bot.name))}</p><ul><li>${t('ASF 里它的配置会被删除，Steam 账号会退出 ASF。')}</li>
          <li>${t('它上面进行中的游戏时长任务会停止。')}</li><li>${t('删除后可以重新新建。')}</li></ul>`, { confirm: t('删除'), danger: true })) return;
        await busy(b, async () => {
          try { await api('POST', '/api/bots/delete', { connection_id: conn.id, bot: bot.name }); toast(t('已删除。'), 'ok'); } catch (err) { showError(err); }
        });
        await this.refresh(false, true);
        return;
      }
      await this.single(b, act, conn, bot);
    },
    async single(btn, act, conn, bot) {
      const who = `<strong>${esc(conn.name)} / ${esc(bot.name)}</strong>`;
      let ok = false, minutes = null;
      if (act === 'farm_start') {
        if (bot.playtime.managed) ok = true;
        else {
          let skipText = '';
          try {
            const s = await api('GET', `/api/farming/skip?connection_id=${conn.id}&bot=${encodeURIComponent(bot.name)}`);
            const apps = s.bots[0] ? s.bots[0].apps : [];
            skipText = apps.length
              ? t('跳过 %d 个游戏：%s', apps.length, apps.slice(0, 8).map((a) => gameHtml(a.app_id, a.name)).join(sep()) + (apps.length > 8 ? ' …' : ''))
              : t('不跳过任何游戏');
          } catch (err) { skipText = t('（未能读取跳过列表）'); }
          ok = await confirmBox(t('启动挂卡'), `<p>${t('为 %s 启动经典卡牌挂机。', who)}</p><p class="muted">${skipText}</p>
            <p class="muted">${t('ASF 会依次挂有剩余卡牌的游戏，并自动处理掉卡。')}</p>`, { confirm: t('启动挂卡') });
        }
      } else if (act === 'farm_stop') {
        ok = bot.playtime.managed || await confirmBox(t('停止挂卡'), `<p>${t('停止 %s 的经典卡牌挂机？', who)}</p>
          <p class="muted">${t('在 ASF 下次重启前不会自动恢复，你可以随时在这里重新启动。')}</p>`, { confirm: t('停止挂卡'), danger: true });
      } else if (act === 'farm_pause_temp') {
        const m = openModal({
          title: t('暂停一段时间'),
          body: `<p>${t('暂停 %s 的挂卡，到时由 ASF 自动继续。', who)}</p>
            <div class="field"><label for="pm">${t('暂停分钟数')}</label><input id="pm" type="number" min="1" max="1092" value="60">
            <div class="help">${t('1–1092 分钟。注意：Bot 断线重连或有新的游戏事件时，ASF 也可能提前继续挂卡。')}</div></div>`,
          actions: [{ label: t('取消'), value: null }, { label: t('暂停'), kind: 'primary', onClick: (_, close) => close(Number($('#pm').value)) }],
        });
        minutes = await m.done;
        ok = !!minutes;
      } else if (act === 'bot_start') {
        ok = await confirmBox(t('启动 Bot'), `<p>${t('让 ASF 启动 %s 并登录 Steam？', who)}</p>
          <p class="muted">${t('如果 Steam 要求验证码，启动后点这个 Bot 的“输入验证码”。')}</p>`, { confirm: t('启动 Bot') });
      } else if (act === 'bot_stop') {
        ok = await confirmBox(t('停止 Bot'), `<p>${t('停止 %s？', who)}</p><p class="muted">${t('该 Steam 账号会从 ASF 登出，挂卡也会停止。')}</p>`,
          { confirm: t('停止 Bot'), danger: true });
      }
      if (!ok) return;
      await busy(btn, async () => {
        try {
          const r = await api('POST', '/api/farming/actions', { action: act, minutes, items: [{ connection_id: conn.id, bot: bot.name }] });
          const x = r.results[0];
          toast(x.message + (x.hint ? ' ' + x.hint : ''), x.ok ? 'ok' : 'error', 6000);
        } catch (err) { showError(err); }
      });
      await this.refresh(false, true);
    },
    async batch(btn, op) {
      const items = [...this.sel].map((k) => this.find(k)).filter((x) => x.bot);
      const verb = op === 'farm_start' ? t('启动挂卡') : t('停止挂卡');
      const list = items.map(({ conn, bot }) => `<li>${esc(conn.name)} / <strong>${esc(bot.name)}</strong>
        <span class="muted">— ${esc(bot.state_text)}${op === 'farm_start' ? ` · ${bot.skip.count == null ? t('跳过列表未读取') : t('跳过 %d 个游戏', bot.skip.count)}` : ''}</span></li>`).join('');
      const ok = await confirmBox(`${verb} · ${items.length}`, `<ul>${list}</ul>
        <p class="muted">${t('正在执行游戏时长任务的 Bot 不会立即改变挂卡状态，只记录任务结束后的去向。')}</p>`,
        { confirm: `${verb}（${items.length}）`, danger: op === 'farm_stop' });
      if (!ok) return;
      await busy(btn, async () => {
        try {
          const r = await api('POST', '/api/farming/actions', { action: op, items: items.map(({ conn, bot }) => ({ connection_id: conn.id, bot: bot.name })) });
          openModal({ title: verb, body: resultsHtml(r.results) });
          this.sel.clear();
        } catch (err) { showError(err); }
      });
      await this.refresh(false, true);
    },
  };

  async function openSkip(connectionId, bot) {
    const m = openModal({ title: t('跳过列表'), wide: true, body: `<div class="boot">${t('正在从 ASF 读取…')}</div>` });
    const body = m.body;
    let data;
    const load = async () => {
      try { data = await api('GET', `/api/farming/skip?connection_id=${connectionId}${bot ? `&bot=${encodeURIComponent(bot)}` : ''}`); draw(); } catch (e) {
        body.innerHTML = `<div class="note err">${esc(errText(e))}</div>`;
      }
    };
    const union = () => {
      const map = new Map();
      for (const b of data.bots) for (const a of b.apps) {
        if (!map.has(a.app_id)) map.set(a.app_id, { ...a, bots: [] });
        map.get(a.app_id).bots.push(b.bot);
      }
      return [...map.values()].sort((x, y) => x.app_id - y.app_id);
    };
    const draw = () => {
      const n = data.bots.length;
      const scope = data.scope === 'bot'
        ? t('作用范围：%s 中的 Bot %s。', esc(data.connection.name), esc(bot))
        : t('作用范围：%s 中你可以操作的全部 %d 个 Bot。ASF 的跳过列表按 Bot 保存，这里会对每个 Bot 分别执行；之后新加入的 Bot 不会自动继承。', esc(data.connection.name), n);
      const errs = data.bots.filter((b) => b.error).map((b) => `<li>${esc(b.bot)}: ${esc(b.error)}</li>`).join('');
      const apps = union();
      body.innerHTML = `<div class="note">${scope} ${t('跳过的游戏不会参与经典卡牌挂机；仍然可以为它们创建游戏时长任务。')}</div>
        ${errs ? `<div class="note err">${t('部分 Bot 读取失败：')}<ul>${errs}</ul></div>` : ''}
        <h3>${t('当前跳过 %d 个游戏', apps.length)}</h3>
        ${apps.length ? `<div class="chips">${apps.map((a) => `<span class="chip">${gameHtml(a.app_id, a.name)}
          ${data.scope === 'connection' ? `<span class="muted small">${a.bots.length}/${n}</span>` : ''}
          <button class="x" data-rm="${a.app_id}" aria-label="${t('移除')}" title="${t('移除')}">×</button></span>`).join('')}</div>`
          : `<p class="muted">${t('目前没有跳过任何游戏。')}</p>`}
        <hr class="sep">
        <div class="field"><label for="sk">${t('添加要跳过的游戏')}</label>
          <textarea id="sk" placeholder="${t('App ID 或 Steam 商店链接，用逗号或换行分隔')}"></textarea>
          <div class="help" id="skh">${t('一次最多 50 个。')}</div></div>
        <div class="btn-row"><button class="btn primary" id="skadd">${t('添加到跳过列表')}</button></div>`;
      const ta = $('#sk', body);
      ta.addEventListener('input', () => {
        const { ids, bad } = parseMany(ta.value);
        const h = $('#skh', body);
        h.className = 'help' + (bad.length ? ' err' : ids.length ? ' ok' : '');
        h.textContent = bad.length ? t('无法识别：%s', bad.slice(0, 3).join(sep())) : ids.length ? t('识别到 %d 个 App ID：%s', ids.length, ids.slice(0, 10).join(', ')) : t('一次最多 50 个。');
      });
      $('#skadd', body).addEventListener('click', (e) => modify(e.target, 'add', parseMany(ta.value)));
      $$('[data-rm]', body).forEach((x) => x.addEventListener('click', (e) => modify(e.target, 'remove', { ids: [Number(x.dataset.rm)], bad: [] })));
    };
    const modify = async (btn, op, { ids, bad }) => {
      if (bad.length) { toast(t('无法识别：%s', bad.slice(0, 3).join(sep())), 'error'); return; }
      if (!ids.length) { toast(t('请先输入 App ID 或商店链接。'), 'error'); return; }
      if (ids.length > 50) { toast(t('一次最多 50 个。'), 'error'); return; }
      const target = data.scope === 'bot' ? `<strong>${esc(bot)}</strong>` : t('%s 中你可以操作的全部 %d 个 Bot', `<strong>${esc(data.connection.name)}</strong>`, data.bots.length);
      const before = union().length;
      const ok = await confirmBox(op === 'add' ? t('添加跳过游戏') : t('移除跳过游戏'),
        `<p>${op === 'add' ? t('将为 %s 跳过：%s', target, `<strong>${ids.join(', ')}</strong>`) : t('将为 %s 不再跳过：%s', target, `<strong>${ids.join(', ')}</strong>`)}</p>
         <p class="muted">${op === 'add' ? t('完成后挂卡时将跳过约 %d 个游戏。如果正好在挂其中某个游戏，ASF 会换下一个。', before + ids.length) : t('这些游戏如还有卡牌，之后会重新参与挂卡。')}</p>`,
        { confirm: op === 'add' ? t('添加') : t('移除'), danger: op === 'remove' });
      if (!ok) return;
      await busy(btn, async () => {
        try {
          const r = await api('POST', '/api/farming/skip', { connection_id: connectionId, bot, op, games: ids.map(String) });
          const fails = r.results.filter((x) => !x.ok);
          if (fails.length) openModal({ title: t('部分 Bot 未成功'), body: resultsHtml(r.results.map((x) => ({ ...x, message: x.ok ? t('已更新') : x.message }))) });
          else toast(op === 'add' ? t('已添加到跳过列表。') : t('已从跳过列表移除。'), 'ok');
        } catch (err) { showError(err); }
      });
      await load();
    };
    await load();
    await m.done;
  }

  const GAMES_HINT = '每个游戏会建一个任务，使用相同的时长和备注。';

  const Playtime = {
    sel: new Set(),
    filter: '',
    async render(root) {
      this.root = root;
      this.key = newKey();
      let conns = [];
      try {
        conns = (await api('GET', '/api/targets')).targets.filter((x) => x.enabled && x.bots.length)
          .map((x) => ({ id: x.connection_id, name: x.name, status: x.status, status_text: x.status_text, bots: x.bots, assigned: x.assigned || {} }));
      } catch (e) { showError(e); }
      this.conns = conns;
      const defMin = S.prefs && S.prefs.default_minutes != null ? S.prefs.default_minutes : 0;
      root.innerHTML = `${head(t('游戏时长'), t('让 ASF 以“正在游戏”状态挂指定游戏。可以一直挂，也可以设定时长，到时自动停止。'))}
        <details class="note help-box"><summary>${t('工作方式')}</summary><ul>
          <li>${t('游戏会写入这个 Bot 的 ASF 配置（GamesPlayedWhileIdle），ASF 重启或面板停机后照常挂。')}</li>
          <li>${t('ASF 会先挂卡，卡挂完后才挂这些游戏。想马上挂时长，可以在添加时选择“先暂停挂卡”。')}</li>
          <li>${t('每次添加或停止，ASF 会重新加载这个 Bot，Bot 会重连几秒。')}</li>
          <li>${t('任务结束时，面板只从配置里删掉它自己加的游戏，你在 ASF 里自己配的游戏不会动。')}</li>
          <li>${t('“已挂时长”是面板估算的时间，不是 Steam 显示的时长。')}</li>
          <li>${t('单个 Bot 最多 32 个游戏（Steam 限制），多出的任务会排队。账号必须拥有这个游戏。')}</li></ul></details>
        ${helpBox(HELP.playtime)}
        <div class="search"><input id="q" type="search" placeholder="${t('搜索游戏名、App ID、Bot 或备注')}" aria-label="${t('搜索')}"></div>
        <section class="panel"><div class="panel-head"><h2>${t('现在在挂')}</h2><div id="newbotbtn"></div></div><div id="nowp"><div class="boot">${t('正在读取…')}</div></div></section>
        <section class="panel" id="formp"></section>
        <section class="panel"><div class="panel-head"><h2>${t('我的任务')}</h2><div class="btn-row" id="bar"></div></div>
          <div class="tabs" id="tabs"></div><div id="tasks"><div class="boot">${t('正在读取…')}</div></div></section>`;
      this.renderForm(defMin);
      this.renderNewBotButton();
      this.query = '';
      $('#q', root).addEventListener('input', (e) => { this.query = e.target.value.trim().toLowerCase(); this.renderLists(); });
      root.addEventListener('click', (e) => this.onClick(e));
      root.addEventListener('change', (e) => {
        const cb = e.target.closest('#tasks [data-sel]');
        if (!cb) return;
        if (cb.checked) this.sel.add(Number(cb.dataset.sel)); else this.sel.delete(Number(cb.dataset.sel));
        this.renderBar();
      });
      await this.refresh();
    },
    async renderNewBotButton() {
      try {
        const info = await api('GET', '/api/bots/mine');
        const left = info.quota < 0 ? null : info.quota - info.used;
        if (!info.hosts.length || (left !== null && left <= 0)) return;
        $('#newbotbtn', this.root).innerHTML = `<button class="btn sm primary" data-act="newbot">${t('新建 Bot')}${left !== null ? ` · ${left}` : ''}</button>`;
      } catch (err) { /* no permission */ }
    },
    renderForm(defMin) {
      const p = $('#formp', this.root);
      if (!this.conns.length) {
        p.innerHTML = noTargetsHtml(t('游戏时长任务要指定在哪个 ASF 的哪个 Bot 上运行。'));
        return;
      }
      const defConn = (S.prefs && this.conns.find((c) => c.id === S.prefs.default_connection_id)) || this.conns[0];
      p.innerHTML = `<div class="panel-head"><h2>${t('新建任务')}</h2></div>
        <form class="form" id="tf" novalidate>
          <div class="form-grid">
            <div class="field"><label for="tc">${t('ASF 连接')}</label><select id="tc">${this.conns.map((c) =>
              `<option value="${c.id}" ${c.id === defConn.id ? 'selected' : ''}>${esc(c.name)}${c.status !== 'ok' ? `（${esc(c.status_text)}）` : ''}</option>`).join('')}</select></div>
            <div class="field"><label for="tb">Bot</label><select id="tb"></select><div class="help" id="tbh"></div></div>
          </div>
          <div class="field"><label for="tg">${t('游戏')}</label><textarea id="tg" rows="2" maxlength="4000" autocomplete="off"
              placeholder="${t('App ID 或 Steam 商店链接，可以一次填多个，例如：730, 440, 570')}"></textarea>
            <div class="help" id="tgh">${t(GAMES_HINT)}</div></div>
          <div class="form-grid">
            <div class="field"><label>${t('目标时长')}</label><div class="inline">
              <input id="th" type="number" min="0" max="99999" value="${Math.floor(defMin / 60)}" aria-label="${t('小时')}"> ${t('小时')}
              <input id="tm" type="number" min="0" max="59" value="${defMin % 60}" aria-label="${t('分钟')}"> ${t('分钟')}</div>
              <div class="help">${t('两项都填 0 表示一直挂，直到手动停止。')}</div></div>
            <div class="field"><label for="tn">${t('备注（可选）')}</label><input id="tn" type="text" maxlength="200"></div>
          </div>
          <div class="btn-row"><button class="btn primary" type="submit">${t('检查并开始')}</button></div>
        </form>`;
      const fillBots = () => {
        const c = this.conns.find((x) => x.id === Number($('#tc').value));
        const bots = c ? c.bots : [];
        $('#tb').innerHTML = bots.length ? bots.map((b) => `<option value="${esc(b)}">${esc(b)}${c.assigned[b] ? ` → ${esc(c.assigned[b])}` : ''}</option>`).join('') : `<option value="">${t('（没有 Bot）')}</option>`;
        $('#tbh').textContent = c && c.status !== 'ok' ? t('该连接当前状态：%s', c.status_text) : '';
      };
      $('#tc').addEventListener('change', fillBots);
      fillBots();
      let timer = null, seq = 0;
      $('#tg').addEventListener('input', () => {
        clearTimeout(timer);
        const h = $('#tgh'), v = $('#tg').value.trim();
        if (!v) { h.className = 'help'; h.textContent = t(GAMES_HINT); return; }
        const { ids, bad } = parseMany(v);
        if (bad.length) { h.className = 'help err'; h.textContent = t('无法识别：%s', bad.slice(0, 3).join(sep())); return; }
        if (ids.length > 31) { h.className = 'help err'; h.textContent = t('一次最多 31 个，现在有 %d 个。', ids.length); return; }
        h.className = 'help ok';
        if (ids.length !== 1) { h.textContent = t('识别到 %d 个游戏：%s', ids.length, ids.join(', ')); return; }
        const id = ids[0];
        h.textContent = `App ID ${id}`;
        const my = ++seq;
        timer = setTimeout(async () => {
          try {
            const r = await api('GET', `/api/apps/resolve?q=${encodeURIComponent(String(id))}`);
            if (my === seq) h.textContent = r.name ? `App ID ${id} · ${r.name}` : t('App ID %d（未查到名称，以 App ID 为准）', id);
          } catch (e) { /* optional */ }
        }, 450);
      });
      $('#tf').addEventListener('submit', (e) => { e.preventDefault(); this.submit($('button[type=submit]', e.target)); });
    },
    async submit(btn) {
      const body = {
        connection_id: Number($('#tc').value), bot: $('#tb').value, games: [$('#tg').value.trim()],
        hours: Number($('#th').value || 0), minutes: Number($('#tm').value || 0),
      };
      if (!body.bot) { toast(t('请选择 Bot。'), 'error'); return; }
      const parsed = parseMany(body.games[0]);
      if (!parsed.ids.length || parsed.bad.length) { toast(t('请填写有效的 App ID 或商店链接。'), 'error'); return; }
      if (parsed.ids.length > 31) { toast(t('一次最多添加 31 个游戏。'), 'error'); return; }
      if (!Number.isInteger(body.hours) || !Number.isInteger(body.minutes) || body.hours < 0 || body.minutes < 0 || body.minutes > 59) {
        toast(t('时长格式不正确：分钟需在 0–59 之间。'), 'error'); return;
      }
      let pv;
      this.pauseFarming = false;
      await busy(btn, async () => { try { pv = await api('POST', '/api/playtime/preview', body); } catch (e) { showError(e); } });
      if (!pv) return;
      if (!pv.new_count) { toast(t('这些游戏在这个 Bot 上都已经在挂了。'), 'error'); return; }
      const note = $('#tn').value.trim();
      const effects = [t('游戏会写入这个 Bot 的 ASF 配置，写入时 Bot 会重连几秒。')];
      if (pv.other_running) effects.push(t('这个 Bot 已有 %d 个时长任务在运行，会一起挂。', pv.other_running));
      if (pv.queued_count) effects.push(t('单个 Bot 最多同时挂 31 个游戏，有 %d 个会排队，有空位后自动开始。', pv.queued_count));
      effects.push(t('账号需要拥有这个游戏，否则 Steam 不会记录挂机。'));
      const ok = await confirmBox(t('确认开始游戏时长任务'), `<dl class="kv">
          <dt>${t('ASF 连接')}</dt><dd>${esc(pv.connection.name)}</dd><dt>Bot</dt><dd>${esc(pv.bot)}</dd>
          <dt>${t('游戏')} · ${pv.new_count}</dt><dd><ul class="plain">${pv.items.map((i) => `<li>${gameHtml(i.app_id, i.app_name)}${i.duplicate ? ` <span class="tag">${t('已在挂，跳过')}</span>` : ''}</li>`).join('')}</ul></dd>
          <dt>${t('目标时长')}</dt><dd>${targetText(pv.target_seconds)}</dd>
          ${note ? `<dt>${t('备注')}</dt><dd>${esc(note)}</dd>` : ''}</dl>
        ${pv.cards_pending ? `<div class="note warn"><strong>${t('这个 Bot 还有卡牌没挂完。')}</strong> ${t('ASF 默认先挂卡，挂完后才开始挂这些游戏。')}
          <label class="check"><input type="checkbox" id="pf"> ${t('先暂停挂卡，现在就开始挂时长（ASF 重启后会恢复挂卡）')}</label></div>` : ''}
        <div class="note"><ul>${effects.map((x) => `<li>${x}</li>`).join('')}</ul></div>`, { confirm: t('开始任务'), onConfirm: (el) => { const c = $('#pf', el); this.pauseFarming = !!(c && c.checked); } });
      if (!ok) return;
      await busy(btn, async () => {
        try {
          const r = await api('POST', '/api/playtime/tasks', { ...body, note: note || null, request_key: this.key, pause_farming: this.pauseFarming === true });
          this.key = newKey();
          $('#tg').value = ''; $('#tn').value = ''; $('#tgh').className = 'help'; $('#tgh').textContent = t(GAMES_HINT);
          toast(r.skipped.length ? t('已开始 %d 个任务，跳过 %d 个已在挂的游戏。', r.tasks.length, r.skipped.length) : t('已开始 %d 个任务。', r.tasks.length), 'ok');
        } catch (e) { showError(e); }
      });
      await this.refresh();
    },
    async refresh() {
      let d, np;
      try {
        [d, np] = await Promise.all([api('GET', `/api/playtime/tasks${this.filter ? `?status=${this.filter}` : ''}`), api('GET', '/api/now-playing')]);
      } catch (e) {
        $('#tasks', this.root).innerHTML = `<div class="note err">${esc(errText(e))}</div>`; return;
      }
      this.tasks = d.tasks;
      this.playing = np.bots;
      const c = d.counts;
      const tabs = [['', '全部', Object.values(c).reduce((a, b) => a + b, 0)]].concat(STATUS_TABS.map(([k, label]) => [k, label, c[k]]));
      $('#tabs', this.root).innerHTML = tabs.map(([k, label, n]) => `<button data-act="filter" data-f="${k}" class="${k === this.filter ? 'active' : ''}">${t(label)}<span class="n">${n || 0}</span></button>`).join('');
      const activeIds = new Set(d.tasks.filter((x) => x.status === 'running' || x.status === 'queued').map((x) => x.id));
      for (const id of [...this.sel]) if (!activeIds.has(id)) this.sel.delete(id);
      this.renderLists();
      this.renderBar();
    },
    renderLists() {
      const q = this.query;
      $('#nowp', this.root).innerHTML = nowPlayingHtml(this.playing || [], q);
      const tasks = (this.tasks || []).filter((x) => matches(q, x.app_name, x.app_id, x.bot, x.note, x.connection.name));
      $('#tasks', this.root).innerHTML = tasks.length ? `<div class="rows">${tasks.map((x) => taskRow(x, true, this.sel.has(x.id))).join('')}</div>`
        : `<div class="empty"><p>${q ? t('没有匹配的任务。') : t('这里还没有任务。')}</p></div>`;
    },
    renderBar() {
      $('#bar', this.root).innerHTML = this.sel.size
        ? `<span class="muted">${t('已选 %d 个', this.sel.size)}</span><button class="btn sm danger" data-act="stopsel">${t('停止所选')}</button><button class="btn sm" data-act="clear">${t('取消选择')}</button>` : '';
    },
    async onClick(e) {
      const b = e.target.closest('[data-act]');
      if (!b) return;
      const act = b.dataset.act;
      if (act === 'filter') { this.filter = b.dataset.f; this.sel.clear(); await this.refresh(); return; }
      if (act === 'newbot') { if (await openNewBotDialog(null)) route(); return; }
      if (act === 'clear') { this.sel.clear(); await this.refresh(); return; }
      if (act === 'stop' || act === 'stopsel') {
        const ids = act === 'stop' ? [Number(b.dataset.id)] : [...this.sel];
        const ts = this.tasks.filter((x) => ids.includes(x.id));
        if (!ts.length) return;
        const ok = await confirmBox(ts.length > 1 ? t('停止 %d 个任务', ts.length) : t('停止任务'), `<ul>${ts.map((x) =>
          `<li>${gameHtml(x.app_id, x.app_name)} — ${esc(x.connection.name)} / ${esc(x.bot)} · ${t('已挂 %s', fmtDur(x.elapsed_seconds))} / ${targetText(x.target_seconds)}</li>`).join('')}</ul>
          <p class="muted">${t('停止后不能继续，只能新建任务。如果这是该 Bot 的最后一个时长任务，Bot 会恢复到任务开始前的挂卡状态。')}</p>`,
        { confirm: t('停止'), danger: true });
        if (!ok) return;
        await busy(b, async () => {
          try {
            const r = await api('POST', '/api/playtime/tasks/stop', { ids: ts.map((x) => x.id) });
            const bad = r.results.filter((x) => !x.ok);
            if (bad.length) openModal({ title: t('停止结果'), body: resultsHtml(r.results) }); else toast(t('已停止。'), 'ok');
            this.sel.clear();
          } catch (err) { showError(err); }
        });
        await this.refresh();
      }
    },
  };

  const Connections = {
    async render(root) {
      this.root = root;
      root.innerHTML = `${head(t('ASF 连接'), t('浏览器不会直接访问你的 ASF：所有请求都由本网站服务器发出，地址和 IPC 密码加密保存在服务器上。'))}
        ${helpBox(HELP.connections)}
        <section class="panel"><div class="panel-head"><h2>${t('添加连接')}</h2></div>
        <form class="form" id="cf" novalidate>
          <div class="form-grid">
            <div class="field"><label for="cn">${t('连接名称')}</label><input id="cn" type="text" maxlength="40" placeholder="${t('例如：家里的 ASF')}" required></div>
            <div class="field"><label for="ca">${t('ASF IPC 地址')}</label><input id="ca" type="text" maxlength="300" placeholder="${S.me.is_admin ? 'http://127.0.0.1:1242' : 'https://asf.example.com:1242'}" autocomplete="off" required>
              <div class="help">${S.me.is_admin ? t('管理员可以填写本机或内网地址；公网地址必须使用 https。') : t('需要能从公网访问，且使用 https。')}</div></div>
            <div class="field"><label for="cp">${t('IPC 密码')}</label><input id="cp" type="password" maxlength="256" autocomplete="new-password">
              <div class="help">${t('即 ASF 全局配置中的 IPCPassword。不是 Steam 密码。')}</div></div>
          </div>
          <details class="note"><summary>${t('如何让 ASF 可以被连接？')}</summary>
            <ul><li>${t('ASF 默认只允许本机访问 IPC（端口 1242）。需要在 IPC.config 中配置监听地址，并在 ASF.json 中设置 IPCPassword。')}</li>
            <li>${t('建议通过带 HTTPS 证书的反向代理（如 Nginx、Caddy）对外提供 IPC。')}</li>
            <li>${t('IPC 密码拥有该 ASF 的完整管理权限。本网站只会执行固定的操作，不会转发任意命令。')}</li>
            <li>${t('密码连续输错几次后，ASF 会临时封禁来源 IP。')}</li></ul></details>
          <div class="btn-row"><button class="btn primary" type="submit">${t('保存并测试')}</button></div></form></section>
        <div id="clist"><div class="boot">${t('正在读取…')}</div></div>`;
      $('#cf').addEventListener('submit', (e) => { e.preventDefault(); this.create($('button[type=submit]', e.target)); });
      root.addEventListener('click', (e) => this.onClick(e));
      await this.refresh();
    },
    async refresh() {
      let d;
      try { d = await api('GET', '/api/connections'); } catch (e) { $('#clist', this.root).innerHTML = `<div class="note err">${esc(errText(e))}</div>`; return; }
      this.list = d.connections;
      $('#clist', this.root).innerHTML = this.list.length ? this.list.map((c) => this.card(c)).join('')
        : `<div class="panel empty"><p>${t('还没有连接。填写 ASF 地址和 IPC 密码，保存后会自动测试。')}</p></div>`;
    },
    card(c) {
      return `<section class="panel"><div class="panel-head"><div><h2>${esc(c.name)}</h2>
          <div class="muted small">${esc(c.address_hint)} · ${c.has_password ? t('已设置 IPC 密码') : t('未设置 IPC 密码')}</div></div>
          ${badge(CONN_KIND[c.status] || 'err', c.status_text)}</div>
        ${c.status_message ? `<div class="note ${c.status === 'ok' ? '' : 'err'}">${esc(c.status_message)}</div>` : ''}
        <dl class="kv"><dt>${t('ASF 版本')}</dt><dd>${esc(c.asf_version || '—')}</dd>
          <dt>Bot</dt><dd>${c.bots.length ? c.bots.map((b) => `<span class="tag">${esc(b)}${c.assigned && c.assigned[b] ? ` → ${esc(c.assigned[b])}` : ''}</span>`).join(' ') : `<span class="muted">${t('无')}</span>`}
            ${c.assigned && Object.keys(c.assigned).length ? `<div class="muted small">${t('“→ 用户名”表示已分配给该用户，可在“管理 → Bot 分配”收回。')}</div>` : ''}</dd>
          <dt>${t('进行中的时长任务')}</dt><dd>${c.active_tasks}</dd>
          <dt>${t('最近测试')}</dt><dd>${fmtTime(c.last_tested_at)}${c.last_ok_at ? ` · ${t('最近一次正常：%s', fmtTime(c.last_ok_at))}` : ''}</dd></dl>
        <div class="btn-row">
          ${c.enabled ? `<button class="btn sm" data-act="test" data-id="${c.id}">${t('重新测试')}</button>` : ''}
          <button class="btn sm" data-act="edit" data-id="${c.id}">${t('编辑')}</button>
          ${c.enabled ? `<button class="btn sm" data-act="disable" data-id="${c.id}">${t('断开')}</button>` : `<button class="btn sm primary" data-act="enable" data-id="${c.id}">${t('重新启用')}</button>`}
          ${S.me.is_admin ? `<button class="btn sm" data-act="userbots" data-id="${c.id}">${c.allow_user_bots ? t('不再允许朋友新建 Bot') : t('允许朋友在这里新建 Bot')}</button>` : ''}
          <button class="btn sm ghost danger" data-act="delete" data-id="${c.id}">${t('删除')}</button></div></section>`;
    },
    async create(btn) {
      const body = { name: $('#cn').value.trim(), address: $('#ca').value.trim(), ipc_password: $('#cp').value || null };
      if (!body.name || !body.address) { toast(t('请填写连接名称和地址。'), 'error'); return; }
      await busy(btn, async () => {
        try {
          const r = await api('POST', '/api/connections', body);
          $('#cf').reset();
          const c = r.connection;
          toast(c.status === 'ok' ? t('连接成功，发现 %d 个 Bot。', c.bots.length) : t('已保存，但测试未通过：%s', c.status_text), c.status === 'ok' ? 'ok' : 'error', 6000);
        } catch (e) { showError(e); }
      });
      await this.refresh();
    },
    async onClick(e) {
      const b = e.target.closest('[data-act]');
      if (!b) return;
      const c = this.list.find((x) => x.id === Number(b.dataset.id));
      if (!c) return;
      const act = b.dataset.act;
      if (act === 'userbots') {
        const allow = !c.allow_user_bots;
        if (allow && !await confirmBox(t('允许朋友新建 Bot'), `<p>${t('有新建额度的用户可以在 %s 里自己新建 Bot。', `<strong>${esc(c.name)}</strong>`)}</p><ul>
          <li>${t('额度在“管理 → 用户”里给每个人单独设置。')}</li><li>${t('用账号密码登录时，密码会保存在你 ASF 的 Bot 配置里；扫码登录不需要密码。')}</li>
          <li>${t('朋友只能看到和操作自己建的 Bot。')}</li></ul>`, { confirm: t('允许') })) return;
        await busy(b, async () => {
          try { await api('POST', `/api/connections/${c.id}/user-bots`, { allow }); toast(allow ? t('已允许。') : t('已关闭。'), 'ok'); } catch (err) { showError(err); }
        });
        await this.refresh();
        return;
      }
      if (act === 'test') {
        await busy(b, async () => {
          try {
            const r = await api('POST', `/api/connections/${c.id}/test`);
            toast(r.connection.status === 'ok' ? t('连接正常。') : t('测试未通过：%s', r.connection.status_text), r.connection.status === 'ok' ? 'ok' : 'error');
          } catch (err) { showError(err); }
        });
      } else if (act === 'edit') {
        const m = openModal({
          title: t('编辑：%s', c.name),
          body: `<div class="form">
            <div class="field"><label for="en">${t('连接名称')}</label><input id="en" type="text" maxlength="40" value="${esc(c.name)}"></div>
            <div class="field"><label for="ea">${t('新的 ASF 地址')}</label><input id="ea" type="text" maxlength="300" autocomplete="off" placeholder="${t('留空表示不修改')}">
              <div class="help">${t('当前：%s。出于安全原因不显示完整地址。', esc(c.address_hint))}</div></div>
            <div class="field"><label for="ep">${t('新的 IPC 密码')}</label><input id="ep" type="password" maxlength="256" autocomplete="new-password" placeholder="${t('留空表示不修改')}"></div>
            ${c.has_password ? `<label class="check"><input type="checkbox" id="ec"> ${t('清除已保存的 IPC 密码（ASF 未设置密码时使用）')}</label>` : ''}</div>`,
          actions: [{ label: t('取消'), value: null }, {
            label: t('保存'), kind: 'primary', onClick: (btn, close) => busy(btn, async () => {
              try {
                const r = await api('PATCH', `/api/connections/${c.id}`, {
                  name: $('#en').value.trim() || null, address: $('#ea').value.trim() || null,
                  ipc_password: $('#ep').value || null, clear_password: !!($('#ec') && $('#ec').checked),
                });
                const good = r.connection.status === 'ok' || r.connection.status === 'disabled';
                toast(good ? t('已保存。') : t('已保存，测试结果：%s', r.connection.status_text), good ? 'ok' : 'error');
                close(true);
              } catch (err) { showError(err); }
            }),
          }],
        });
        await m.done;
      } else if (act === 'disable') {
        const ok = await confirmBox(t('断开连接'), `<p>${t('断开 %s？', `<strong>${esc(c.name)}</strong>`)}</p><ul>
          <li>${t('本网站将不再访问这个 ASF。')}</li>
          <li>${c.active_tasks ? t('正在进行的 %d 个游戏时长任务会停止，并尝试把相关 Bot 恢复到任务前的挂卡状态。', c.active_tasks) : t('没有进行中的时长任务。')}</li>
          <li>${t('连接信息会保留，之后可以重新启用。')}</li></ul>`, { confirm: t('断开'), danger: true });
        if (!ok) return;
        await busy(b, async () => {
          try {
            const r = await api('POST', `/api/connections/${c.id}/disable`);
            if (r.warnings && r.warnings.length) openModal({ title: t('已断开'), body: `<div class="note warn">${r.warnings.map(esc).join('<br>')}</div>` });
            else toast(t('已断开。'), 'ok');
          } catch (err) { showError(err); }
        });
      } else if (act === 'enable') {
        await busy(b, async () => {
          try { const r = await api('POST', `/api/connections/${c.id}/enable`); toast(t('已启用：%s', r.connection.status_text), r.connection.status === 'ok' ? 'ok' : 'error'); } catch (err) { showError(err); }
        });
      } else if (act === 'delete') {
        const ok = await confirmBox(t('删除连接'), `<p>${t('永久删除 %s？', `<strong>${esc(c.name)}</strong>`)}</p><ul>
          <li>${t('服务器上保存的地址和 IPC 密码会被删除。')}</li>
          <li>${c.active_tasks ? t('该连接下的所有游戏时长任务记录也会删除；进行中的 %d 个任务会先停止，并尝试恢复 Bot 的挂卡状态。', c.active_tasks) : t('该连接下的所有游戏时长任务记录也会删除。')}</li>
          <li>${t('不会影响你 ASF 本身的任何配置。')}</li></ul>`, { confirm: t('删除'), danger: true });
        if (!ok) return;
        await busy(b, async () => {
          try {
            const r = await api('DELETE', `/api/connections/${c.id}`);
            if (r.warnings && r.warnings.length) openModal({ title: t('已删除'), body: `<div class="note warn">${r.warnings.map(esc).join('<br>')}</div>` });
            else toast(t('已删除。'), 'ok');
          } catch (err) { showError(err); }
        });
      }
      await this.refresh();
    },
  };

  const ACTION_TEXT = {
    login: '登录', logout: '退出登录', register: '注册', change_password: '修改密码', revoke_sessions: '退出其他设备',
    invite_create: '创建邀请码', invite_revoke: '撤销邀请码', bot_assign: '分配 Bot', bot_unassign: '收回 Bot',
    bot_create: '新建 Bot', bot_input: '提交验证码', bot_delete: '删除 Bot', bot_qr_start: '扫码登录', user_quota: '设置新建额度',
    connection_user_bots: '设置朋友新建 Bot', user_enable: '启用用户', user_disable: '停用用户', bot_settings: '修改 Bot 设置',
    connection_create: '添加连接', connection_update: '修改连接', connection_test: '测试连接', connection_enable: '启用连接',
    connection_disable: '断开连接', connection_delete: '删除连接', farm_start: '启动挂卡', farm_stop: '停止挂卡',
    farm_pause_temp: '临时暂停挂卡', bot_start: '启动 Bot', bot_stop: '停止 Bot', skip_add: '添加跳过游戏',
    skip_remove: '移除跳过游戏', playtime_create: '创建时长任务', playtime_stop: '停止时长任务',
  };
  const actionText = (a) => (ACTION_TEXT[a] ? t(ACTION_TEXT[a]) : a);
  const outcomeText = (o) => (o === 'ok' ? badge('ok', t('成功')) : badge('err', o));

  const Settings = {
    async render(root) {
      this.root = root;
      let conns = [];
      try { conns = (await api('GET', '/api/targets')).targets.map((x) => ({ id: x.connection_id, name: x.name })); } catch (e) { /* ignore */ }
      const p = S.prefs || { default_minutes: 0, default_connection_id: null };
      root.innerHTML = `${head(t('设置'), t('你的网站账户、登录设备和默认值。'))}
        <section class="panel"><div class="panel-head"><h2>${t('界面语言')}</h2></div>${langSwitch()}</section>
        <section class="panel"><div class="panel-head"><h2>${t('默认值')}</h2></div><form class="form" id="pf">
          <div class="form-grid">
            <div class="field"><label for="pc">${t('默认 ASF 连接')}</label><select id="pc"><option value="">${t('（第一个连接）')}</option>
              ${conns.map((c) => `<option value="${c.id}" ${c.id === p.default_connection_id ? 'selected' : ''}>${esc(c.name)}</option>`).join('')}</select>
              <div class="help">${t('新建游戏时长任务时默认选中。')}</div></div>
            <div class="field"><label>${t('默认目标时长')}</label><div class="inline">
              <input id="ph" type="number" min="0" max="99999" value="${Math.floor(p.default_minutes / 60)}" aria-label="${t('小时')}"> ${t('小时')}
              <input id="pm" type="number" min="0" max="59" value="${p.default_minutes % 60}" aria-label="${t('分钟')}"> ${t('分钟')}</div>
              <div class="help">${t('两项都填 0 表示默认一直挂。')}</div></div>
          </div><div class="btn-row"><button class="btn primary" type="submit">${t('保存默认值')}</button></div></form></section>
        <section class="panel"><div class="panel-head"><h2>${t('修改密码')}</h2></div><form class="form" id="wf" novalidate>
          <div class="form-grid">
            <div class="field"><label for="w0">${t('当前密码')}</label><input id="w0" type="password" autocomplete="current-password" maxlength="128"></div>
            <div class="field"><label for="w1">${t('新密码')}</label><input id="w1" type="password" autocomplete="new-password" maxlength="128"><div class="help">${t('至少 10 个字符。')}</div></div>
            <div class="field"><label for="w2">${t('确认新密码')}</label><input id="w2" type="password" autocomplete="new-password" maxlength="128"></div>
          </div><div class="btn-row"><button class="btn primary" type="submit">${t('修改密码')}</button><span class="muted small">${t('修改后，其他设备上的登录会退出。')}</span></div></form></section>
        <section class="panel"><div class="panel-head"><h2>${t('登录设备')}</h2><button class="btn sm" id="ro">${t('退出其他所有设备')}</button></div><div id="sess"></div></section>
        <section class="panel"><div class="panel-head"><h2>${t('我的操作记录')}</h2></div><div id="act"></div></section>`;
      $('#pf').addEventListener('submit', async (e) => {
        e.preventDefault();
        const mins = Number($('#ph').value || 0) * 60 + Number($('#pm').value || 0);
        if (mins < 0 || Number($('#pm').value) > 59) { toast(t('分钟需在 0–59 之间。'), 'error'); return; }
        await busy($('button[type=submit]', e.target), async () => {
          try {
            const r = await api('PUT', '/api/account/prefs', { default_connection_id: $('#pc').value ? Number($('#pc').value) : null, default_minutes: mins });
            S.prefs = r.prefs; toast(t('已保存。'), 'ok');
          } catch (err) { showError(err); }
        });
      });
      $('#wf').addEventListener('submit', async (e) => {
        e.preventDefault();
        if ($('#w1').value !== $('#w2').value) { toast(t('两次输入的新密码不一致。'), 'error'); return; }
        await busy($('button[type=submit]', e.target), async () => {
          try {
            const r = await api('POST', '/api/account/password', { current_password: $('#w0').value, new_password: $('#w1').value });
            e.target.reset(); toast(r.message, 'ok'); this.loadSessions();
          } catch (err) { showError(err); }
        });
      });
      $('#ro').addEventListener('click', async (e) => {
        if (!await confirmBox(t('退出其他设备'), `<p>${t('除当前浏览器外，所有设备上的登录都会失效。')}</p>`, { confirm: t('退出其他设备'), danger: true })) return;
        await busy(e.target, async () => {
          try { const r = await api('POST', '/api/account/sessions/revoke-others'); toast(t('已退出 %d 个设备。', r.revoked), 'ok'); } catch (err) { showError(err); }
        });
        this.loadSessions();
      });
      this.loadSessions();
      this.loadActivity();
    },
    async loadSessions() {
      try {
        const d = await api('GET', '/api/account/sessions');
        $('#sess', this.root).innerHTML = `<div class="table-wrap"><table><thead><tr><th>${t('设备')}</th><th>IP</th><th>${t('登录时间')}</th><th>${t('最近活动')}</th></tr></thead><tbody>
          ${d.sessions.map((s) => `<tr><td>${esc((s.user_agent || t('未知')).slice(0, 80))}${s.current ? ` <span class="tag">${t('当前')}</span>` : ''}</td>
            <td class="mono">${esc(s.ip || '')}</td><td>${fmtDate(s.created_at)}</td><td>${fmtTime(s.last_seen_at)}</td></tr>`).join('')}</tbody></table></div>`;
      } catch (e) { showError(e); }
    },
    async loadActivity() {
      try {
        const d = await api('GET', '/api/account/activity');
        $('#act', this.root).innerHTML = d.items.length ? `<div class="table-wrap"><table><thead><tr><th>${t('时间')}</th><th>${t('操作')}</th><th>${t('对象')}</th><th>${t('结果')}</th></tr></thead><tbody>
          ${d.items.map((a) => `<tr><td>${fmtDate(a.created_at)}</td><td>${esc(actionText(a.action))}</td>
            <td class="mono small">${esc(a.target || '')}</td><td>${outcomeText(a.outcome)}</td></tr>`).join('')}</tbody></table></div>` : `<p class="muted">${t('暂无记录。')}</p>`;
      } catch (e) { showError(e); }
    },
  };

  const Admin = {
    admin: true,
    tab: 'invites',
    async render(root) {
      this.root = root;
      root.innerHTML = `${head(t('后台管理'), t('这里不显示任何用户的 IPC 地址、IPC 密码或 Steam 信息。'))}
        <div class="tabs" id="atabs">${[['invites', '邀请码'], ['users', '用户'], ['bots', 'Bot 分配'], ['health', '服务状态'], ['audit', '安全审计']].map(([k, label]) =>
          `<button data-tab="${k}">${t(label)}</button>`).join('')}</div><div id="apane"></div>`;
      $('#atabs').addEventListener('click', (e) => { const b = e.target.closest('[data-tab]'); if (b) { this.tab = b.dataset.tab; this.show(); } });
      $('#apane').addEventListener('click', (e) => this.onClick(e));
      await this.show();
    },
    async refresh() { if (this.tab === 'health') await this.show(); },
    async show() {
      $$('#atabs button').forEach((b) => b.classList.toggle('active', b.dataset.tab === this.tab));
      const pane = $('#apane');
      try {
        if (this.tab === 'invites') pane.innerHTML = this.invites((await api('GET', '/api/admin/invites')).invites);
        else if (this.tab === 'users') pane.innerHTML = this.users((await api('GET', '/api/admin/users')).users);
        else if (this.tab === 'bots') { this.botData = await api('GET', '/api/admin/bots'); pane.innerHTML = this.bots(this.botData); }
        else if (this.tab === 'health') pane.innerHTML = this.health(await api('GET', '/api/admin/health'));
        else pane.innerHTML = this.audit((await api('GET', '/api/admin/audit')).items);
      } catch (e) { pane.innerHTML = `<div class="note err">${esc(errText(e))}</div>`; }
      const f = $('#if', pane);
      if (f) f.addEventListener('submit', (e) => { e.preventDefault(); this.createInvite($('button[type=submit]', f)); });
    },
    invites(list) {
      return `<section class="panel"><div class="panel-head"><h2>${t('创建邀请码')}</h2></div><form class="form" id="if">
        <div class="form-grid">
          <div class="field"><label for="iu">${t('可使用次数')}</label><input id="iu" type="number" min="1" max="100" value="1"><div class="help">${t('1 表示一次性，注册成功后立即失效。')}</div></div>
          <div class="field"><label for="ie">${t('有效期')}</label><select id="ie"><option value="24">${t('%d 天', 1)}</option><option value="72" selected>${t('%d 天', 3)}</option>
            <option value="168">${t('%d 天', 7)}</option><option value="720">${t('%d 天', 30)}</option></select></div>
          <div class="field"><label for="in">${t('备注（可选）')}</label><input id="in" type="text" maxlength="100"></div>
        </div><div class="btn-row"><button class="btn primary" type="submit">${t('生成邀请码')}</button>
          <span class="muted small">${t('邀请码只控制网站注册资格，不涉及 ASF 或 Steam 权限。')}</span></div></form></section>
        <section class="panel"><div class="panel-head"><h2>${t('邀请码列表')}</h2></div><div class="table-wrap"><table>
        <thead><tr><th>${t('邀请码')}</th><th>${t('状态')}</th><th>${t('已用/上限')}</th><th>${t('使用者')}</th><th>${t('过期时间')}</th><th>${t('备注')}</th><th></th></tr></thead><tbody>
        ${list.map((i) => `<tr><td class="mono">${esc(i.hint)}</td>
          <td>${badge(i.status === 'active' ? 'ok' : i.status === 'revoked' ? 'err' : 'muted', i.status_text)}</td>
          <td>${i.uses} / ${i.max_uses}</td><td>${esc(i.used_by.join(sep()) || '—')}</td><td>${fmtDate(i.expires_at)}</td>
          <td>${esc(i.note || '')}</td><td>${i.status === 'active' ? `<button class="btn sm ghost danger" data-act="revoke" data-id="${i.id}">${t('撤销')}</button>` : ''}</td></tr>`).join('') || `<tr><td colspan="7" class="muted">${t('还没有邀请码。')}</td></tr>`}
        </tbody></table></div><p class="muted small">${t('出于安全原因，数据库只保存邀请码的摘要，列表只显示末尾几位。')}</p></section>`;
    },
    async createInvite(btn) {
      const body = { max_uses: Number($('#iu').value), expires_in_hours: Number($('#ie').value), note: $('#in').value.trim() || null };
      await busy(btn, async () => {
        try {
          const r = await api('POST', '/api/admin/invites', body);
          const m = openModal({
            title: t('邀请码已生成'),
            body: `<p>${t('请现在复制并发给朋友。关闭后无法再次查看完整邀请码。')}</p><div class="secret" id="code">${esc(r.code)}</div>`,
            actions: [{ label: t('复制'), kind: '', onClick: async () => {
              try { await navigator.clipboard.writeText(r.code); toast(t('已复制。'), 'ok'); } catch (e) { toast(t('无法自动复制，请手动选中复制。'), 'error'); }
            } }, { label: t('我已保存'), kind: 'primary', value: true }],
          });
          await m.done;
        } catch (e) { showError(e); }
      });
      await this.show();
    },
    bots(d) {
      if (!d.bots.length) {
        return `<section class="panel empty"><p>${t('还没有 Bot。先在“ASF 连接”中添加你自己的 ASF，然后在这里新建 Bot。')}</p>
          <div class="btn-row center-row"><a class="btn" href="#/connections">${t('ASF 连接')}</a><button class="btn primary" data-act="newbot">${t('新建 Bot')}</button></div></section>`;
      }
      const users = d.users;
      return `<section class="panel"><div class="panel-head"><h2>${t('Bot 与分配')}</h2><button class="btn sm primary" data-act="newbot">${t('新建 Bot')}</button></div>
        <details class="note help-box"><summary>${t('分配规则')}</summary><ul>
          <li>${t('朋友只能看到并操作分配给他的 Bot，看不到其他 Bot、你的 ASF 地址或 IPC 密码。')}</li>
          <li>${t('一个 Bot 同时只分给一个朋友。分出去的 Bot 仍会显示在你的页面里（带“→ 用户名”），你也可以直接操作它，包括停止朋友的任务。')}</li>
          <li>${t('改分配或收回时，原使用者在该 Bot 上进行中的游戏时长任务会停止。')}</li>
          <li>${t('Bot 从 ASF 中消失后，分配会自动取消；之后出现的同名 Bot 需要重新分配。')}</li></ul></details>
        <div class="table-wrap"><table><thead><tr><th>Bot</th><th>${t('所在连接')}</th><th>${t('分配给')}</th><th>${t('进行中任务')}</th><th></th></tr></thead><tbody>
        ${d.bots.map((b) => `<tr><td><strong>${esc(b.bot)}</strong>${b.present ? '' : ` <span class="tag">${t('ASF 中已不存在')}</span>`}</td>
          <td>${esc(b.connection_name)}${b.enabled ? '' : ` <span class="tag">${t('已断开')}</span>`}</td>
          <td><select data-assign="${b.bot_id}" aria-label="${esc(b.bot)}"><option value="">${t('不分配（我自己管理）')}</option>
            ${users.map((u) => `<option value="${u.id}" ${u.id === b.assigned_user_id ? 'selected' : ''}>${esc(u.username)}${u.is_active ? '' : t('（已停用）')}</option>`).join('')}</select></td>
          <td>${b.active_tasks}</td>
          <td><div class="btn-row"><button class="btn sm primary" data-act="assign" data-id="${b.bot_id}">${t('保存分配')}</button>
            <button class="btn sm" data-act="admincode" data-cid="${b.connection_id}" data-bot="${esc(b.bot)}">${t('输入验证码')}</button></div></td></tr>`).join('')}
        </tbody></table></div>${users.length ? '' : `<p class="muted small">${t('还没有普通用户。先在“邀请码”中邀请朋友注册。')}</p>`}</section>`;
    },
    async newBot() {
      if (await openNewBotDialog((this.botData && this.botData.users) || [])) await this.show();
    },
    users(list) {
      return `<section class="panel"><div class="table-wrap"><table><thead><tr><th>${t('用户名')}</th><th>${t('状态')}</th><th>${t('注册时间')}</th><th>${t('最近登录')}</th><th>${t('连接数')}</th><th>${t('分配的 Bot')}</th><th>${t('自建 Bot 额度')}</th><th>${t('进行中任务')}</th><th></th></tr></thead><tbody>
        ${list.map((u) => `<tr><td>${esc(u.username)}${u.is_admin ? ` <span class="tag">${t('管理员')}</span>` : ''}</td>
          <td>${u.is_active ? badge('ok', t('正常')) : badge('err', t('已停用'))}</td><td>${fmtDate(u.created_at)}</td><td>${fmtTime(u.last_login_at)}</td>
          <td>${u.connections}</td><td>${u.assigned_bots}</td>
          <td>${u.is_admin ? t('不限') : `<div class="inline"><input class="qinput" type="number" min="0" max="50" value="${u.bot_quota}" data-quota="${u.id}" aria-label="${t('额度')}">
            <button class="btn sm" data-act="quota" data-id="${u.id}">${t('保存')}</button></div><div class="muted small">${t('已建 %d 个', u.created_bots)}</div>`}</td>
          <td>${u.active_tasks}</td>
          <td>${u.id === S.me.id ? '' : u.is_active ? `<button class="btn sm ghost danger" data-act="udisable" data-id="${u.id}" data-name="${esc(u.username)}" data-tasks="${u.active_tasks}">${t('停用')}</button>`
            : `<button class="btn sm" data-act="uenable" data-id="${u.id}" data-name="${esc(u.username)}">${t('启用')}</button>`}</td></tr>`).join('')}
        </tbody></table></div></section>`;
    },
    health(h) {
      const s = h.scheduler;
      const conns = Object.entries(h.connections).map(([k, v]) => `${badge(CONN_KIND[k] || 'err', v.text)} ${v.count}`).join(' ') || t('无');
      const tasks = STATUS_TABS.map(([k, label]) => `${badge(TASK_KIND[k], t(label))} ${h.tasks[k] || 0}`).join(' ');
      const l = h.last_24h;
      return `<section class="panel"><div class="panel-head"><h2>${t('任务调度器')}</h2>${s.healthy ? badge('ok', t('运行正常')) : badge('err', t('未在运行或已停滞'))}</div>
        <dl class="kv"><dt>${t('最近一轮')}</dt><dd>${fmtTime(s.last_tick_at)} · ${t('耗时 %s 毫秒，每 %d 秒一轮', s.last_tick_ms ?? '—', s.interval_seconds)}</dd>
          <dt>${t('累计轮次')}</dt><dd>${s.ticks}</dd><dt>${t('最近错误')}</dt><dd>${esc(s.last_error || t('无'))}</dd>
          <dt>${t('异常的 Bot')}</dt><dd>${h.bots_with_errors}</dd></dl></section>
        <section class="panel"><div class="panel-head"><h2>${t('ASF 连接可用性')}</h2></div><p>${conns}</p>
          ${h.shared_addresses ? `<div class="note warn">${t('有 %d 个 ASF 地址被多个网站账户同时添加。ASF 的 IPC 密码是全局管理员权限，这些用户的时长任务可能互相覆盖。建议每位用户使用独立的 ASF 实例。', h.shared_addresses)}</div>` : ''}
          <div class="table-wrap"><table><thead><tr><th>${t('连接')}</th><th>${t('所属用户')}</th><th>${t('状态')}</th><th>${t('ASF 版本')}</th><th>${t('最近正常')}</th></tr></thead><tbody>
          ${h.connections_list.map((c) => `<tr><td>${esc(c.name)}</td><td>${esc(c.owner)}</td><td>${badge(CONN_KIND[c.status] || 'err', c.status_text)}</td>
            <td>${esc(c.asf_version || '—')}</td><td>${fmtTime(c.last_ok_at)}</td></tr>`).join('') || `<tr><td colspan="5" class="muted">${t('暂无')}</td></tr>`}</tbody></table></div></section>
        <section class="panel"><div class="panel-head"><h2>${t('游戏时长任务（全站）')}</h2></div><p>${tasks}</p></section>
        <section class="panel"><div class="panel-head"><h2>${t('最近 24 小时')}</h2></div><dl class="kv">
          <dt>${t('登录成功 / 失败')}</dt><dd>${l.login_ok} / ${l.login_failed}</dd><dt>${t('注册成功 / 失败')}</dt><dd>${l.registrations} / ${l.register_failed}</dd>
          <dt>${t('连接测试失败')}</dt><dd>${l.connection_test_failed}</dd><dt>${t('挂卡操作失败')}</dt><dd>${l.farming_action_failed}</dd></dl></section>`;
    },
    audit(items) {
      return `<section class="panel"><p class="muted small">${t('显示登录、注册、账户与管理操作，以及所有失败的操作（最近 200 条）。不记录密码、IPC 密钥、Cookie 或令牌。')}</p>
        <div class="table-wrap"><table><thead><tr><th>${t('时间')}</th><th>${t('用户')}</th><th>${t('操作')}</th><th>${t('对象')}</th><th>${t('结果')}</th><th>IP</th></tr></thead><tbody>
        ${items.map((a) => `<tr><td>${fmtDate(a.created_at)}</td><td>${esc(a.username || '—')}</td><td>${esc(actionText(a.action))}</td>
          <td class="mono small">${esc(a.target || '')}</td><td>${outcomeText(a.outcome)}</td><td class="mono small">${esc(a.ip || '')}</td></tr>`).join('') || `<tr><td colspan="6" class="muted">${t('暂无')}</td></tr>`}
        </tbody></table></div></section>`;
    },
    async onClick(e) {
      const b = e.target.closest('[data-act]');
      if (!b) return;
      const id = Number(b.dataset.id);
      if (b.dataset.act === 'newbot') { await this.newBot(); return; }
      if (b.dataset.act === 'quota') {
        const v = Number($(`[data-quota="${id}"]`).value);
        await busy(b, async () => {
          try { await api('POST', `/api/admin/users/${id}/quota`, { bot_quota: v }); toast(v ? t('已允许新建 %d 个 Bot。', v) : t('已取消新建权限。'), 'ok'); } catch (err) { showError(err); }
        });
        await this.show();
        return;
      }
      if (b.dataset.act === 'admincode') { await openCodeDialog(Number(b.dataset.cid), b.dataset.bot, null); return; }
      if (b.dataset.act === 'assign') {
        const bot = this.botData.bots.find((x) => x.bot_id === id);
        const sel = $(`[data-assign="${id}"]`);
        const newId = sel.value ? Number(sel.value) : null;
        if (!bot || newId === (bot.assigned_user_id ?? null)) { toast(t('没有变化。')); return; }
        const newName = newId ? (this.botData.users.find((u) => u.id === newId) || {}).username : null;
        const self = t('我自己管理');
        const html = `<p><strong>${esc(bot.bot)}</strong>: ${esc(bot.assigned_username || self)} → <strong>${esc(newName || self)}</strong></p>
          <ul>${newName ? `<li>${t('%s 将可以在面板中操作这个 Bot 的挂卡、跳过列表和游戏时长。', esc(newName))}</li>` : `<li>${t('这个 Bot 不再分给任何朋友。')}</li>`}
          ${bot.active_tasks ? `<li>${t('该 Bot 上不属于新使用者的进行中任务会停止（目前共有 %d 个进行中任务）。', bot.active_tasks)}</li>` : ''}</ul>`;
        if (!await confirmBox(t('修改 Bot 分配'), html, { confirm: t('确认修改') })) { sel.value = bot.assigned_user_id ?? ''; return; }
        await busy(b, async () => {
          try {
            const r = await api('POST', `/api/admin/bots/${id}/assign`, { user_id: newId });
            toast([t('已保存。'), ...(r.warnings || [])].join(' '), 'ok', 6000);
          } catch (err) { showError(err); }
        });
        await this.show();
        return;
      }
      if (b.dataset.act === 'revoke') {
        if (!await confirmBox(t('撤销邀请码'), `<p>${t('撤销后，这个邀请码不能再用于注册。已经注册的账户不受影响。')}</p>`, { confirm: t('撤销'), danger: true })) return;
        await busy(b, async () => { try { await api('POST', `/api/admin/invites/${id}/revoke`); toast(t('已撤销。'), 'ok'); } catch (err) { showError(err); } });
      } else if (b.dataset.act === 'udisable') {
        const n = Number(b.dataset.tasks);
        if (!await confirmBox(t('停用用户'), `<p>${t('停用 %s？', `<strong>${esc(b.dataset.name)}</strong>`)}</p><ul><li>${t('该用户会立即退出登录，且无法再登录。')}</li>
          <li>${n ? t('该用户 %d 个进行中的游戏时长任务会停止，并尝试恢复相关 Bot 的挂卡状态。', n) : t('该用户没有进行中的任务。')}</li>
          <li>${t('该用户的连接和数据会保留，可以重新启用。')}</li></ul>`, { confirm: t('停用'), danger: true })) return;
        await busy(b, async () => {
          try { const r = await api('POST', `/api/admin/users/${id}/disable`); toast([t('已停用。'), ...(r.warnings || [])].join(' '), 'ok', 7000); } catch (err) { showError(err); }
        });
      } else if (b.dataset.act === 'uenable') {
        if (!await confirmBox(t('启用用户'), `<p>${t('重新启用 %s？', `<strong>${esc(b.dataset.name)}</strong>`)}</p>`, { confirm: t('启用') })) return;
        await busy(b, async () => { try { await api('POST', `/api/admin/users/${id}/enable`); toast(t('已启用。'), 'ok'); } catch (err) { showError(err); } });
      }
      await this.show();
    },
  };

  const PAGES = { '': Playtime, overview: Overview, login: Login, register: Register, farming: Farming, playtime: Playtime,
    connections: Connections, settings: Settings, admin: Admin };

  async function loadMe() {
    const d = await api('GET', '/api/auth/me');
    S.me = d.user;
    S.csrf = d.csrf_token || null;
    S.prefs = d.prefs || null;
    S.features = d.features || {};
  }

  async function boot() {
    LANG = pickLang();
    try {
      const r = await fetch('/static/i18n.json?v=10', { credentials: 'same-origin' });
      if (r.ok) DICT = await r.json();
    } catch (e) { /* falls back to Chinese */ }
    applyLang();
    try { await loadMe(); } catch (e) { S.me = null; }
    window.addEventListener('hashchange', route);
    document.addEventListener('click', async (e) => {
      const l = e.target.closest('[data-lang]');
      if (l) { if (l.dataset.lang !== LANG) setLang(l.dataset.lang); return; }
      const b = e.target.closest('[data-act="idleedit"]');
      if (!b) return;
      if (await openBotSettings(Number(b.dataset.cid), b.dataset.bot) && S.page && S.page.refresh) S.page.refresh();
    });
    document.addEventListener('toggle', (e) => {
      const el = e.target;
      if (!el.matches || !el.matches('details.games')) return;
      if (el.open) openLists.add(el.dataset.key); else openLists.delete(el.dataset.key);
    }, true);
    document.addEventListener('visibilitychange', autoRefresh);
    setInterval(autoRefresh, 15000);
    route();
  }
  boot();
})();
