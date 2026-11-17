(function () {
  'use strict';
  var form = document.getElementById('form');
  var pw = document.getElementById('pw');
  var go = document.getElementById('go');
  var msg = document.getElementById('msg');
  var note = document.getElementById('note');

  fetch('/api/auth').then(function (r) { return r.json(); }).then(function (st) {
    if (st.authenticated) { location.replace('/'); return; }
    if (!st.configured) {
      go.disabled = true;
      pw.disabled = true;
      note.textContent = '还没有设置访问密码。请在运行服务的机器上执行：qqbot_bridge passwd，设置后刷新本页。';
    }
  }).catch(function () {});

  form.addEventListener('submit', function (e) {
    e.preventDefault();
    msg.textContent = '';
    go.disabled = true;
    fetch('/api/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ password: pw.value })
    }).then(function (r) {
      return r.json().then(function (j) { return { ok: r.ok, body: j }; });
    }).then(function (res) {
      if (res.ok) { location.replace('/'); return; }
      msg.textContent = res.body.error || '登录失败';
      pw.value = '';
      pw.focus();
    }).catch(function () {
      msg.textContent = '网络错误，请重试';
    }).then(function () { go.disabled = false; });
  });
})();
