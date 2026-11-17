(function () {
  'use strict';

  // ---------- 工具 ----------
  var $ = function (id) { return document.getElementById(id); };

  // el 创建 DOM 节点。内容一律用 textContent，页面里不拼 HTML，避免 QQ 消息里的内容注入脚本。
  function el(tag, props) {
    var n = document.createElement(tag);
    props = props || {};
    Object.keys(props).forEach(function (k) {
      var v = props[k];
      if (v == null || v === false) return;
      if (k === 'class') n.className = v;
      else if (k === 'text') n.textContent = v;
      else if (k === 'on') Object.keys(v).forEach(function (ev) { n.addEventListener(ev, v[ev]); });
      else n.setAttribute(k, v === true ? '' : v);
    });
    for (var i = 2; i < arguments.length; i++) {
      var c = arguments[i];
      if (c == null || c === false) continue;
      n.appendChild(typeof c === 'string' ? document.createTextNode(c) : c);
    }
    return n;
  }
  // mdBody 把文本按 Markdown 渲染成一个 <div>；渲染器没加载成功时退回纯文本，保证内容总能看到。
  // 渲染器全程用 DOM API 构造节点，消息里的 HTML / 脚本只会显示成文字。
  function mdBody(text, extraClass) {
    var node;
    if (window.QQMD) {
      try { node = window.QQMD.render(text); } catch (e) { node = null; }
    }
    if (!node) node = el('div', { class: 'md' }, el('pre', { text: text }));
    if (extraClass) node.className += ' ' + extraClass;
    return node;
  }
  function clear(n) { while (n.firstChild) n.removeChild(n.firstChild); }
  function fmtTime(v) {
    if (!v) return '';
    var d = new Date(v);
    return isNaN(d) ? '' : d.toLocaleString();
  }
  // avatar 返回用户头像。图片由服务端代理并缓存；取不到真实头像时服务端会回退成字母头像。
  function avatar(openId, name) {
    var src = '/api/avatar/' + encodeURIComponent(openId) + (name ? '?n=' + encodeURIComponent(name) : '');
    return el('img', { class: 'avatar', src: src, alt: '', width: '24', height: '24', loading: 'lazy' });
  }
  // Go 的零值时间序列化成 0001-01-01，按「没有」处理
  function hasTime(v) { return !!v && String(v).indexOf('0001-') !== 0; }
  function fmtSize(n) {
    if (n < 1024) return n + ' B';
    if (n < 1048576) return (n / 1024).toFixed(1) + ' KB';
    return (n / 1048576).toFixed(1) + ' MB';
  }

  function api(path, opts) {
    opts = opts || {};
    opts.credentials = 'same-origin';
    return fetch(path, opts).then(function (r) {
      if (r.status === 401) { location.href = '/login'; throw new Error('unauthorized'); }
      return r.json().then(function (j) {
        if (!r.ok) throw new Error(j.error || ('HTTP ' + r.status));
        return j;
      });
    });
  }
  function post(path, body) {
    return api(path, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body || {})
    });
  }

  // ---------- 标签页 ----------
  var TABS = ['overview', 'messages', 'whitelist', 'sessions'];
  var current = '';
  var timers = {};

  function showTab(name) {
    if (TABS.indexOf(name) < 0) name = 'overview';
    current = name;
    TABS.forEach(function (t) { $('tab-' + t).hidden = t !== name; });
    Array.prototype.forEach.call($('tabs').children, function (a) {
      a.classList.toggle('active', a.getAttribute('data-tab') === name);
    });
    if (location.hash !== '#' + name) history.replaceState(null, '', '#' + name);
    if (name === 'messages') { fillSessionFilter(); loadMessages(true); }
    if (name === 'whitelist') loadWhitelist();
    if (name === 'sessions') loadSessions();
  }
  $('tabs').addEventListener('click', function (e) {
    var a = e.target.closest('a[data-tab]');
    if (a) showTab(a.getAttribute('data-tab'));
  });
  $('logout').addEventListener('click', function () {
    post('/api/logout').then(function () { location.href = '/login'; });
  });

  // ---------- 概览 ----------
  function row(k, v) {
    return el('div', { class: 'row' }, el('span', { class: 'dim', text: k }), el('span', { class: 'v mono', text: v }));
  }

  // agentName 是当前 agent 的展示名（CodeBuddy、Cursor…），随 /api/state 更新；
  // 老版本服务端没有这个字段时保持 CodeBuddy。
  var agentName = 'CodeBuddy';

  function renderState(st) {
    if (st.agent) agentName = st.agent;
    $('clock').textContent = new Date(st.now).toLocaleTimeString();
    var gw = $('gw');
    var ok = st.gateway === 'connected';
    gw.className = 'badge ' + (ok ? 'ok' : 'bad');
    gw.textContent = ok ? (st.gatewayMsg || '已连接') : (st.gatewayMsg || '未连接');
    // 页头：机器人昵称 · AppID；昵称要等网关登录成功后才知道
    $('appid').textContent = st.bound ? ((st.botName ? st.botName + ' · ' : '') + 'AppID ' + st.appId) : '未绑定';
    document.title = (st.botName ? st.botName + ' · ' : '') + 'QQBot Bridge 管理端';

    var bind = $('bind');
    clear(bind);
    if (st.bound) {
      bind.appendChild(row('机器人昵称', st.botName || '（连接成功后显示）'));
      bind.appendChild(row('AppID', st.appId));
      bind.appendChild(el('div', { class: 'row' },
        el('span', { class: 'dim', text: '绑定者' }),
        el('span', { class: 'v mono person' }, st.userOpenId ? avatar(st.userOpenId) : null,
          (st.userOpenId || '').slice(0, 12) + '…')));
    } else {
      var bs = st.bind || {};
      bind.appendChild(el('div', { class: 'dim', text: '还没有绑定机器人。' }));
      if (bs.url) {
        bind.appendChild(el('div', { id: 'qr' }, el('img', { src: '/api/qr.svg?t=' + Date.now(), alt: '二维码' })));
        bind.appendChild(el('div', { class: 'dim', text: bs.message || '' }));
      } else {
        bind.appendChild(el('div', {}, el('button', {
          type: 'button', text: '生成绑定二维码',
          on: { click: function () { post('/api/bind').then(poll); } }
        })));
      }
    }

    var cur = $('cur');
    clear(cur);
    var s = (st.sessions || []).filter(function (x) { return x.current; })[0];
    if (!s) cur.appendChild(el('span', { class: 'empty', text: '没有会话' }));
    else {
      cur.appendChild(row('名称', s.name || s.id));
      cur.appendChild(row('目录', s.dir));
      if (s.agentId) cur.appendChild(row('会话 ID', s.agentId.slice(0, 8)));
    }

    var t = st.task || {};
    var task = $('task');
    clear(task);
    if (t.running) {
      task.appendChild(row('状态', '执行中'));
      task.appendChild(row('指令', t.prompt));
      task.appendChild(row('开始', new Date(t.startedAt).toLocaleTimeString()));
    } else if (hasTime(t.startedAt)) {
      task.appendChild(row('上次任务', new Date(t.startedAt).toLocaleTimeString() + ' · ' + (t.tools || []).length + ' 步'));
      if (t.result) task.appendChild(el('pre', { class: 'code', text: t.result.slice(0, 1200) }));
      if (t.error) task.appendChild(el('pre', { class: 'code err', text: t.error }));
    } else {
      task.appendChild(el('span', { class: 'empty', text: '空闲' }));
    }

    var logs = $('logs');
    var atBottom = logs.scrollTop + logs.clientHeight >= logs.scrollHeight - 20;
    clear(logs);
    (st.logs || []).slice(-150).forEach(function (l) {
      logs.appendChild(el('div', { class: l.level },
        el('span', { class: 't', text: new Date(l.time).toLocaleTimeString() + ' ' }), l.message));
    });
    if (atBottom) logs.scrollTop = logs.scrollHeight;
  }

  function poll() {
    return api('/api/state').then(renderState).catch(function () {});
  }

  // ---------- 收到的消息 ----------
  var SCENE = { true: '群聊', false: '单聊' };
  var STATUS = {
    accepted: ['已受理', 'ok'],
    denied: ['已拒绝', 'bad'],
    duplicate: ['重复', 'warn']
  };
  var KIND = { text: '', voice: '🎤 语音', attachment: '📎 附件' };
  var msgs = { items: [], hasMore: false };

  function msgQuery(before) {
    var q = '/api/messages?limit=100';
    var st = $('msg-status').value;
    var kw = $('msg-q').value.trim();
    var se = $('msg-session').value;
    if (se) q += '&session=' + encodeURIComponent(se);
    if (st) q += '&status=' + encodeURIComponent(st);
    if (kw) q += '&q=' + encodeURIComponent(kw);
    if (before) q += '&before=' + before;
    return q;
  }

  function loadMessages(reset) {
    return api(msgQuery(0)).then(function (res) {
      if (reset) msgs.items = [];
      var seen = {};
      var merged = res.messages.concat(msgs.items);
      msgs.items = merged.filter(function (m) {
        if (seen[m.id]) return false;
        seen[m.id] = true;
        return true;
      }).sort(function (a, b) { return b.id - a.id; });
      if (reset) msgs.hasMore = res.hasMore;
      renderMessages();
    }).catch(function () {});
  }

  function loadOlderMessages() {
    if (!msgs.items.length) return;
    var last = msgs.items[msgs.items.length - 1].id;
    api(msgQuery(last)).then(function (res) {
      msgs.items = msgs.items.concat(res.messages);
      msgs.hasMore = res.hasMore;
      renderMessages();
    }).catch(function () {});
  }

  // convBadge 说明一条消息属于哪个对话；会话还在时可点击跳到「会话」页。
  function convBadge(m) {
    if (m.status === 'denied') return el('span', { class: 'badge gone', text: '未分配对话 · 已拒绝' });
    if (!m.session) return el('span', { class: 'badge gone', text: '对话：未知（旧记录）' });
    var name = m.sessionNow || m.sessionName || m.session;
    var tip = '会话 ' + m.session + (m.sessionDir ? '\n目录 ' + m.sessionDir : '') +
      (m.conversation ? '\n对话 ' + m.conversation : '\n新对话（这条消息之前还没有 ' + agentName + ' 对话）');
    if (!m.sessionExists) {
      return el('span', { class: 'badge gone', title: tip + '\n（这个会话已被移除）', text: '对话 · ' + name + '（已移除）' });
    }
    return el('span', {
      class: 'badge link', title: tip, text: '对话 · ' + name,
      on: { click: function () { selectSession(m.session); showTab('sessions'); } }
    });
  }

  function convMeta(m) {
    if (m.status === 'denied' || !m.session) return null;
    var parts = [];
    if (m.sessionDir) parts.push(m.sessionDir);
    parts.push(m.conversation ? (agentName + ' 对话 ' + m.conversation.slice(0, 8)) : '新对话');
    return el('div', { class: 'meta', text: parts.join(' · ') });
  }

  function allowUser(openId) {
    return post('/api/whitelist', { action: 'add', openId: openId }).then(function () {
      loadMessages(true);
    }).catch(function (e) { alert(e.message); });
  }

  var msgSig = '';
  function renderMessages() {
    // 内容没变就不重绘，避免每 3 秒打断用户选中文字
    var sig = msgs.hasMore + '|' + msgs.items.map(function (m) { return m.id + (m.allowed ? 'a' : 'n') + (m.sessionExists ? 'e' : 'x') + (m.sessionNow || ''); }).join(',');
    if (sig === msgSig) return;
    msgSig = sig;
    var box = $('msg-list');
    clear(box);
    if (!msgs.items.length) {
      box.appendChild(el('div', { class: 'empty', text: '还没有收到消息' }));
    }
    msgs.items.forEach(function (m) {
      var st = STATUS[m.status] || [m.status, ''];
      var who = (m.name ? m.name + ' ' : '') + m.from;
      var top = el('div', { class: 'top' },
        el('span', { class: 'badge ' + st[1], text: st[0] }),
        el('span', { class: 'badge', text: SCENE[!!m.group] }),
        KIND[m.kind] ? el('span', { class: 'badge', text: KIND[m.kind] + (m.attachments ? ' ×' + m.attachments : '') }) : null,
        m.command ? el('span', { class: 'badge', text: '指令' }) : null,
        convBadge(m),
        el('span', { class: 'person' }, avatar(m.from, m.name), el('span', { class: 'mono', text: who })),
        el('span', { class: 'dim spacer', text: fmtTime(m.time) }));
      if (m.group) top.insertBefore(el('span', { class: 'mono dim', text: '群 ' + m.group.slice(0, 8) }), top.children[2]);
      if (m.status === 'denied' && !m.allowed) {
        top.appendChild(el('button', {
          type: 'button', class: 'small', text: '加入白名单',
          on: { click: function () { allowUser(m.from); } }
        }));
      }
      box.appendChild(el('div', { class: 'list-item' }, top, mdBody(m.content || '（空）', 'body'), convMeta(m)));
    });
    $('msg-more').hidden = !msgs.hasMore;
  }

  // 按对话筛选的下拉框：用会话列表填充，保持当前选择
  function fillSessionFilter() {
    return api('/api/sessions').then(function (res) {
      var sel = $('msg-session');
      var keep = sel.value;
      var sig = (res.sessions || []).map(function (s) { return s.id + ':' + (s.name || ''); }).join('|');
      if (sel.getAttribute('data-sig') === sig) return;
      sel.setAttribute('data-sig', sig);
      clear(sel);
      sel.appendChild(el('option', { value: '', text: '全部对话' }));
      (res.sessions || []).forEach(function (s) {
        sel.appendChild(el('option', { value: s.id, text: (s.name || s.id) + (s.current ? '（当前）' : '') }));
      });
      sel.value = keep;
    }).catch(function () {});
  }
  $('msg-session').addEventListener('change', function () { loadMessages(true); });
  $('msg-status').addEventListener('change', function () { loadMessages(true); });
  var msgDebounce;
  $('msg-q').addEventListener('input', function () {
    clearTimeout(msgDebounce);
    msgDebounce = setTimeout(function () { loadMessages(true); }, 300);
  });
  $('msg-refresh').addEventListener('click', function () { loadMessages(true); });
  $('msg-more').addEventListener('click', loadOlderMessages);

  // ---------- 白名单 ----------
  function person(p, tail) {
    return el('div', { class: 'list-item' },
      el('div', { class: 'top' },
        avatar(p.openId, p.name),
        el('span', { class: 'mono', text: p.openId }),
        p.name ? el('span', { class: 'dim', text: p.name }) : null,
        tail));
  }

  function renderWhitelist(w) {
    var owner = $('wl-owner');
    clear(owner);
    if (w.owner && w.owner.openId) {
      owner.appendChild(person(w.owner, el('span', { class: 'badge ok spacer', text: '绑定者 · 始终可用' })));
    }
    var users = $('wl-users');
    clear(users);
    (w.users || []).forEach(function (u) {
      users.appendChild(person(u, el('button', {
        type: 'button', class: 'small danger spacer', text: '移除',
        on: {
          click: function () {
            if (!confirm('确定把 ' + u.openId + ' 移出白名单？')) return;
            post('/api/whitelist', { action: 'remove', openId: u.openId })
              .then(renderWhitelist).catch(function (e) { alert(e.message); });
          }
        }
      })));
    });
    if (!(w.users || []).length) users.appendChild(el('div', { class: 'empty', text: '暂无其他用户' }));

    var seen = $('wl-seen');
    clear(seen);
    (w.seen || []).forEach(function (s) {
      seen.appendChild(person(s, el('span', { class: 'spacer' },
        el('span', { class: 'dim', text: s.count + ' 次 · ' + fmtTime(s.last) + '  ' }),
        el('button', {
          type: 'button', class: 'small', text: '加入白名单',
          on: {
            click: function () {
              post('/api/whitelist', { action: 'add', openId: s.openId })
                .then(renderWhitelist).catch(function (e) { alert(e.message); });
            }
          }
        }))));
    });
    if (!(w.seen || []).length) seen.appendChild(el('div', { class: 'empty', text: '最近没有被拒绝的用户' }));
  }

  function loadWhitelist() {
    return api('/api/whitelist').then(renderWhitelist).catch(function () {});
  }

  $('wl-form').addEventListener('submit', function (e) {
    e.preventDefault();
    var input = $('wl-input');
    $('wl-err').textContent = '';
    post('/api/whitelist', { action: 'add', openId: input.value.trim() }).then(function (w) {
      input.value = '';
      renderWhitelist(w);
    }).catch(function (err) { $('wl-err').textContent = err.message; });
  });

  // ---------- 会话 ----------
  var sess = { list: [], selected: '', detail: null, items: [], total: 0, hasMore: false };

  function loadSessions() {
    return api('/api/sessions').then(function (res) {
      sess.list = res.sessions || [];
      if (!sess.selected && sess.list.length) {
        var cur = sess.list.filter(function (s) { return s.current; })[0] || sess.list[0];
        selectSession(cur.id);
      }
      renderSessionList();
    }).catch(function () {});
  }

  function renderSessionList() {
    var box = $('sess-list');
    clear(box);
    sess.list.forEach(function (s) {
      var cls = 'list-item sess-item' + (s.id === sess.selected ? ' selected' : '') + (s.current ? ' current' : '');
      box.appendChild(el('div', {
        class: cls, 'data-id': s.id,
        on: { click: function () { selectSession(s.id); } }
      },
        el('div', { class: 'top' },
          el('span', { class: 'name', text: s.name || s.id }),
          s.current ? el('span', { class: 'badge ok', text: '当前' }) : null,
          s.running ? el('span', { class: 'badge warn', text: '执行中' }) : null),
        el('div', { class: 'dir', text: s.dir })));
    });
    if (!sess.list.length) box.appendChild(el('div', { class: 'empty', text: '还没有会话' }));
  }

  function selectSession(id) {
    sess.selected = id;
    sess.detail = null;
    sess.items = [];
    sess.total = 0;
    sess.hasMore = false;
    detailSig = '';
    renderSessionList();
    refreshSession(true);
  }

  function refreshSession(first) {
    var id = sess.selected;
    if (!id) return;
    var detail = api('/api/sessions/' + encodeURIComponent(id));
    var logUrl = '/api/sessions/' + encodeURIComponent(id) + '/log';
    if (!first && sess.total > 0) logUrl += '?after=' + sess.total;
    var log = api(logUrl);
    Promise.all([detail, log]).then(function (r) {
      if (id !== sess.selected) return;
      sess.detail = r[0];
      var lg = r[1];
      if (first || sess.total === 0) {
        sess.items = lg.items;
        sess.hasMore = lg.hasMore;
      } else {
        sess.items = sess.items.concat(lg.items);
      }
      sess.total = lg.total;
      sess.note = lg.note;
      renderSessionDetail();
    }).catch(function () {});
  }

  function loadEarlier() {
    if (!sess.items.length) return;
    var id = sess.selected;
    var first = sess.items[0].idx;
    api('/api/sessions/' + encodeURIComponent(id) + '/log?before=' + first + '&limit=100').then(function (lg) {
      if (id !== sess.selected) return;
      sess.items = lg.items.concat(sess.items);
      sess.hasMore = lg.hasMore;
      renderSessionDetail();
    }).catch(function () {});
  }

  function logItem(it) {
    var time = it.time ? new Date(it.time).toLocaleTimeString() : '';
    if (it.kind === 'text') {
      return el('div', { class: 'msg ' + it.role },
        el('div', { class: 'who', text: (it.role === 'user' ? '用户' : '助手') + ' · ' + time }),
        mdBody(it.text));
    }
    var label = { thinking: '推理', tool: '调用 ' + (it.name || ''), result: '结果 ' + (it.name || '') }[it.kind] || it.kind;
    var firstLine = it.text.split('\n')[0].slice(0, 100);
    return el('div', { class: 'msg ' + it.kind },
      el('details', {},
        el('summary', { text: label + ' · ' + time + ' · ' + firstLine }),
        el('pre', { class: 'code', text: it.text })));
  }

  var detailSig = '';
  function renderSessionDetail() {
    var d = sess.detail;
    var box = $('sess-detail');
    // 没有变化就不重绘，避免折叠项被收起、滚动位置跳动
    var sig = sess.selected + '|' + JSON.stringify(d) + '|' + sess.hasMore + '|' + sess.note + '|' +
      sess.items.length + ':' + (sess.items.length ? sess.items[0].idx : '') + ':' + sess.total;
    if (sig === detailSig) return;
    detailSig = sig;
    // 保留日志滚动位置：在底部时跟随新内容
    var oldLog = $('log-box');
    var atBottom = !oldLog || oldLog.scrollTop + oldLog.clientHeight >= oldLog.scrollHeight - 30;
    var oldTop = oldLog ? oldLog.scrollTop : 0;
    clear(box);
    if (!d) return;

    var info = el('div', { class: 'card' }, el('h2', { text: '会话信息' }),
      row('名称', d.name || d.id),
      row('标识', d.id + (d.current ? '（当前）' : '') + (d.auto ? '（自动发现）' : '')),
      row('目录', d.dir),
      row('模型', d.model || '跟随全局默认'),
      row((d.agent || agentName) + ' 会话', d.agentId || '（还没有）'),
      row('创建时间', d.createdAt),
      row('最后使用', d.lastUsed || '—'),
      row('状态', d.running ? '执行中' : '空闲'));
    if (d.git && d.git.available) {
      var g = d.git;
      var changes = [];
      if (g.staged) changes.push('已暂存 ' + g.staged);
      if (g.modified) changes.push('未暂存 ' + g.modified);
      if (g.untracked) changes.push('未跟踪 ' + g.untracked);
      info.appendChild(row('Git 分支', g.branch + (g.ahead ? ' ↑' + g.ahead : '') + (g.behind ? ' ↓' + g.behind : '')));
      info.appendChild(row('Git 变更', changes.length ? changes.join(' · ') : '干净'));
      if (g.commit) info.appendChild(row('最近提交', g.commit + ' ' + (g.subject || '') + (g.age ? '（' + g.age + '）' : '')));
    }
    var tr = d.transcript || {};
    if (tr.exists) {
      info.appendChild(row('对话标题', tr.title || '—'));
      info.appendChild(row('对话记录', tr.messages + ' 条消息 · ' + tr.tools + ' 次工具调用 · ' + fmtSize(tr.size)));
      info.appendChild(row('记录文件', tr.path));
    }
    box.appendChild(info);

    if (d.task && hasTime(d.task.startedAt)) {
      var t = d.task;
      var card = el('div', { class: 'card' }, el('h2', { text: d.running ? '当前任务' : '最近任务' }),
        row('开始', fmtTime(t.startedAt)), row('指令', t.prompt), row('步数', String((t.tools || []).length)));
      if (t.error) card.appendChild(el('pre', { class: 'code err', text: t.error }));
      box.appendChild(card);
    }

    var logCard = el('div', { class: 'card' }, el('h2', { text: '对话日志' }));
    if (sess.note) logCard.appendChild(el('div', { class: 'dim', text: sess.note }));
    if (sess.hasMore) {
      logCard.appendChild(el('div', { class: 'toolbar' }, el('button', {
        type: 'button', class: 'ghost small', text: '加载更早的记录', on: { click: loadEarlier }
      })));
    }
    var lb = el('div', { id: 'log-box', class: 'log-box' });
    sess.items.forEach(function (it) { lb.appendChild(logItem(it)); });
    if (!sess.items.length && !sess.note) lb.appendChild(el('div', { class: 'empty', text: '暂无记录' }));
    logCard.appendChild(lb);
    box.appendChild(logCard);
    lb.scrollTop = atBottom ? lb.scrollHeight : oldTop;
  }

  // ---------- 定时刷新 ----------
  setInterval(poll, 2000);
  setInterval(function () {
    if (document.hidden) return;
    if (current === 'messages') loadMessages(false);
    if (current === 'sessions') { loadSessions(); refreshSession(false); }
    if (current === 'whitelist') loadWhitelist();
  }, 3000);

  poll();
  showTab(location.hash.replace('#', ''));
})();
