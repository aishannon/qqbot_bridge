/*
 * 轻量 Markdown 渲染器，给管理端显示 QQ 消息和对话日志用。
 *
 * 安全：全程用 DOM API 构造节点（createElement / textContent），不使用 innerHTML，
 * 消息里写的 <script>、<img onerror=…> 之类只会被当成文字显示，不会被执行。
 * 链接只放行 http / https / mailto，并带 rel="noopener noreferrer nofollow"；
 * 图片不加载（避免通过外链泄漏管理端访问），显示为带图片名的链接。
 *
 * 聊天场景的取舍：单个换行显示为换行（而不是合并成空格），因为 QQ 消息和对话输出大多是这么写的。
 *
 * 支持：标题、段落、粗体/斜体/删除线、行内代码、围栏代码块、引用、有序/无序/任务列表（可嵌套）、
 *      表格（含对齐）、分隔线、链接与裸链接、反斜杠转义。
 *
 * 暴露：window.QQMD = { parse, render }；在 Node 里 module.exports 同样的对象（供单测）。
 */
(function (root) {
  'use strict';

  // ---------- 内联 ----------

  var PUNCT = /[!-\/:-@\[-`{-~]/;
  var SAFE_URL = /^(https?:\/\/|mailto:)/i;
  // 裸链接只含 ASCII 的 URL 字符：遇到中文标点或汉字就自然截断，「详见https://a.com/x里面」不会吞掉后面的字
  var BARE_URL = /^https?:\/\/[A-Za-z0-9\-._~:\/?#\[\]@!$&'()*+,;=%]+/i;
  var URL_TAIL = /^[.,;:!?\]}'"]$/;

  function isSpace(c) { return c === undefined || c === '' || /\s/.test(c); }
  function isAlnum(c) { return !!c && /[A-Za-z0-9]/.test(c); }

  function textNode(v) { return { t: 'text', v: v }; }

  // 在 text 里从 from 开始找 ch 重复 len 次的结束分隔符；找不到返回 -1
  function findClose(text, from, ch, len) {
    var j = from;
    while (j < text.length) {
      var c = text[j];
      if (c === '\\') { j += 2; continue; }
      if (c === '`') { // 跳过行内代码，里面的 * 不算
        var k = j; while (text[k] === '`') k++;
        var run = k - j;
        var end = text.indexOf(new Array(run + 1).join('`'), k);
        j = end < 0 ? k : end + run;
        continue;
      }
      if (c === ch) {
        var m = j; while (text[m] === ch) m++;
        var r = m - j;
        var prev = text[j - 1];
        var next = text[m];
        var rightFlanking = j > from && !isSpace(prev);
        var ok = rightFlanking && (len === 1 ? r === 1 : r >= len);
        if (ok && ch === '_' && isAlnum(next)) ok = false; // snake_case 里的下划线不是强调
        if (ok) return j;
        j = m;
        continue;
      }
      j++;
    }
    return -1;
  }

  // 找与 text[open]==='[' 配对的 ']'
  function matchBracket(text, open) {
    var depth = 0;
    for (var i = open; i < text.length; i++) {
      var c = text[i];
      if (c === '\\') { i++; continue; }
      if (c === '`') {
        var k = i; while (text[k] === '`') k++;
        var end = text.indexOf(new Array(k - i + 1).join('`'), k);
        if (end > 0) i = end + (k - i) - 1;
        continue;
      }
      if (c === '[') depth++;
      else if (c === ']') { depth--; if (depth === 0) return i; }
    }
    return -1;
  }

  // 解析 ( 开始的链接目标，返回 {url, end}（end 指向 ')' 之后）；失败返回 null
  function parseLinkDest(text, open) {
    var i = open + 1, depth = 1, start = i;
    while (i < text.length && text[i] !== '\n') {
      var c = text[i];
      if (c === '\\') { i += 2; continue; }
      if (c === '(') depth++;
      else if (c === ')') { depth--; if (depth === 0) break; }
      i++;
    }
    if (depth !== 0 || i >= text.length) return null;
    var inner = text.slice(start, i).trim();
    // 去掉可选标题： url "title"
    var m = /^(<[^>]*>|\S+)(?:\s+(?:"[^"]*"|'[^']*'))?$/.exec(inner);
    if (!m) return null;
    var url = m[1];
    if (url[0] === '<' && url[url.length - 1] === '>') url = url.slice(1, -1);
    return { url: url, end: i + 1 };
  }

  function parseInline(text) {
    var out = [];
    var buf = '';
    function flush() { if (buf) { out.push(textNode(buf)); buf = ''; } }
    var i = 0;
    while (i < text.length) {
      var c = text[i];

      if (c === '\\' && i + 1 < text.length) {
        if (PUNCT.test(text[i + 1])) { buf += text[i + 1]; i += 2; continue; }
        if (text[i + 1] === '\n') { flush(); out.push({ t: 'br' }); i += 2; continue; }
      }

      if (c === '\n') { flush(); out.push({ t: 'br' }); i++; continue; }

      if (c === '`') {
        var k = i; while (text[k] === '`') k++;
        var run = k - i;
        var fence = new Array(run + 1).join('`');
        var end = text.indexOf(fence, k);
        // 结束串必须恰好 run 个反引号
        while (end >= 0 && (text[end + run] === '`' || text[end - 1] === '`')) end = text.indexOf(fence, end + 1);
        if (end > 0) {
          var code = text.slice(k, end).replace(/\n/g, ' ');
          if (code.length > 2 && code[0] === ' ' && code[code.length - 1] === ' ' && code.trim()) code = code.slice(1, -1);
          flush(); out.push({ t: 'code', v: code });
          i = end + run; continue;
        }
        buf += fence; i = k; continue;
      }

      if (c === '!' && text[i + 1] === '[') {
        var cb = matchBracket(text, i + 1);
        if (cb > 0 && text[cb + 1] === '(') {
          var d = parseLinkDest(text, cb + 1);
          if (d) { flush(); out.push({ t: 'img', alt: text.slice(i + 2, cb), href: d.url }); i = d.end; continue; }
        }
      }

      if (c === '[') {
        var close = matchBracket(text, i);
        if (close > 0 && text[close + 1] === '(') {
          var dest = parseLinkDest(text, close + 1);
          if (dest) {
            flush();
            out.push({ t: 'link', href: dest.url, c: parseInline(text.slice(i + 1, close)) });
            i = dest.end; continue;
          }
        }
      }

      if (c === '<') { // <https://…> 自动链接；其他 <…> 一律当文字
        var gt = text.indexOf('>', i);
        if (gt > 0) {
          var inner = text.slice(i + 1, gt);
          if (/^(https?:\/\/|mailto:)\S+$/i.test(inner)) {
            flush(); out.push({ t: 'link', href: inner, c: [textNode(inner)] });
            i = gt + 1; continue;
          }
        }
      }

      if ((c === 'h' || c === 'H') && /^https?:\/\//i.test(text.substr(i, 8)) && !isAlnum(text[i - 1])) {
        var bm = BARE_URL.exec(text.slice(i));
        if (bm) {
          var url = bm[0];
          // 反复去掉末尾的标点；)  只有在括号不成对（) 比 ( 多）时才去，保留 .../A_(b) 这类链接
          for (;;) {
            var last = url[url.length - 1];
            if (URL_TAIL.test(last)) { url = url.slice(0, -1); continue; }
            if (last === ')' && (url.match(/\)/g) || []).length > (url.match(/\(/g) || []).length) { url = url.slice(0, -1); continue; }
            break;
          }
          if (url.length > 8) {
            flush(); out.push({ t: 'link', href: url, c: [textNode(url)] });
            i += url.length; continue;
          }
        }
      }

      if (c === '~' && text[i + 1] === '~' && !isSpace(text[i + 2])) {
        var ce = findClose(text, i + 2, '~', 2);
        if (ce > 0) { flush(); out.push({ t: 'del', c: parseInline(text.slice(i + 2, ce)) }); i = ce + 2; continue; }
      }

      if (c === '*' || c === '_') {
        var m = i; while (text[m] === c) m++;
        var runLen = m - i;
        var after = text[m];
        var before = text[i - 1];
        var leftFlanking = !isSpace(after);
        if (c === '_' && isAlnum(before)) leftFlanking = false; // snake_case
        if (leftFlanking) {
          var want = Math.min(runLen, 3);
          var closeAt = findClose(text, m, c, want);
          while (closeAt < 0 && want > 1) { want--; closeAt = findClose(text, m, c, want); }
          if (closeAt > 0) {
            // 多出来的开头分隔符当文字
            var extra = runLen - want;
            buf += new Array(extra + 1).join(c);
            var inner2 = parseInline(text.slice(m, closeAt));
            flush();
            var node = want === 3
              ? { t: 'em', c: [{ t: 'strong', c: inner2 }] }
              : { t: want === 2 ? 'strong' : 'em', c: inner2 };
            out.push(node);
            i = closeAt + want; continue;
          }
        }
        buf += new Array(runLen + 1).join(c); i = m; continue;
      }

      buf += c; i++;
    }
    flush();
    return out;
  }

  // ---------- 块 ----------

  var RE_FENCE = /^ {0,3}(`{3,}|~{3,})\s*([^\s`]*)[^`]*$/;
  var RE_HEADING = /^ {0,3}(#{1,6})\s+(.*?)\s*#*\s*$/;
  var RE_HR = /^ {0,3}([-*_])(?:[ \t]*\1){2,}[ \t]*$/;
  var RE_QUOTE = /^ {0,3}>/;
  var RE_LIST = /^( *)([-*+]|\d{1,9}[.)])[ \t]+(.*)$/;
  var RE_TABLE_SEP = /^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$/;

  function isBlank(l) { return /^\s*$/.test(l); }

  function splitRow(line) {
    var s = line.trim();
    if (s[0] === '|') s = s.slice(1);
    if (s[s.length - 1] === '|' && s[s.length - 2] !== '\\') s = s.slice(0, -1);
    var cells = [], cur = '';
    for (var i = 0; i < s.length; i++) {
      if (s[i] === '\\' && s[i + 1] === '|') { cur += '|'; i++; }
      else if (s[i] === '|') { cells.push(cur.trim()); cur = ''; }
      else cur += s[i];
    }
    cells.push(cur.trim());
    return cells;
  }

  function startsBlock(line, nextLine) {
    if (RE_FENCE.test(line) || RE_HEADING.test(line) || RE_HR.test(line) || RE_QUOTE.test(line)) return true;
    var m = RE_LIST.exec(line);
    if (m && (/^[-*+]$/.test(m[2]) || /^1[.)]$/.test(m[2])) && m[3] !== '') return true;
    if (nextLine !== undefined && line.indexOf('|') >= 0 && RE_TABLE_SEP.test(nextLine) && nextLine.indexOf('-') >= 0 &&
        splitRow(line).length === splitRow(nextLine).length) return true;
    return false;
  }

  function parseBlocks(lines) {
    var out = [];
    var i = 0, n = lines.length;
    while (i < n) {
      var line = lines[i];
      if (isBlank(line)) { i++; continue; }

      var fm = RE_FENCE.exec(line);
      if (fm) {
        var mark = fm[1], body = [];
        i++;
        var closeRe = new RegExp('^ {0,3}' + (mark[0] === '`' ? '`' : '~') + '{' + mark.length + ',}\\s*$');
        while (i < n && !closeRe.test(lines[i])) { body.push(lines[i]); i++; }
        i++; // 跳过结束围栏（没有也无妨）
        out.push({ t: 'code', lang: fm[2] || '', v: body.join('\n') });
        continue;
      }

      var hm = RE_HEADING.exec(line);
      if (hm) { out.push({ t: 'h', level: hm[1].length, c: parseInline(hm[2]) }); i++; continue; }

      if (RE_HR.test(line)) { out.push({ t: 'hr' }); i++; continue; }

      if (RE_QUOTE.test(line)) {
        var q = [];
        while (i < n && RE_QUOTE.test(lines[i])) { q.push(lines[i].replace(/^ {0,3}> ?/, '')); i++; }
        out.push({ t: 'quote', c: parseBlocks(q) });
        continue;
      }

      if (line.indexOf('|') >= 0 && i + 1 < n && RE_TABLE_SEP.test(lines[i + 1]) && lines[i + 1].indexOf('-') >= 0) {
        var head = splitRow(line), sep = splitRow(lines[i + 1]);
        if (head.length === sep.length) {
          var align = sep.map(function (s) {
            var l = s[0] === ':', r = s[s.length - 1] === ':';
            return l && r ? 'center' : r ? 'right' : l ? 'left' : '';
          });
          var rows = [];
          i += 2;
          while (i < n && !isBlank(lines[i]) && lines[i].indexOf('|') >= 0) {
            var cells = splitRow(lines[i]);
            while (cells.length < head.length) cells.push('');
            rows.push(cells.slice(0, head.length).map(parseInline));
            i++;
          }
          out.push({ t: 'table', align: align, head: head.map(parseInline), rows: rows });
          continue;
        }
      }

      var lm = RE_LIST.exec(line);
      if (lm) {
        var res = parseList(lines, i);
        out.push(res.node);
        i = res.next;
        continue;
      }

      // 段落：一直读到空行或下一个块的开头
      var para = [line];
      i++;
      while (i < n && !isBlank(lines[i]) && !startsBlock(lines[i], lines[i + 1])) { para.push(lines[i]); i++; }
      out.push({ t: 'p', c: parseInline(para.join('\n').replace(/[ \t]+\n/g, '\n').trim()) });
    }
    return out;
  }

  function parseList(lines, start) {
    var n = lines.length;
    var first = RE_LIST.exec(lines[start]);
    var base = first[1].length;
    var ordered = /\d/.test(first[2]);
    var items = [];
    var i = start;

    while (i < n) {
      var m = RE_LIST.exec(lines[i]);
      if (!m || m[1].length > base + 1 || m[1].length < base) break;
      if (/\d/.test(m[2]) !== ordered) break;

      var body = [m[3]];
      // 内容缩进 = 标记后第一个字符所在的列；续行按它去缩进，这样列表项里的代码块、嵌套列表缩进才对
      var contentIndent = Math.min(lines[i].length - m[3].length, base + 4);
      i++;
      while (i < n) {
        var l = lines[i];
        if (isBlank(l)) {
          // 空行：后面还有缩进的内容才算同一项
          var j = i; while (j < n && isBlank(lines[j])) j++;
          if (j < n && indentOf(lines[j]) >= base + 2) { for (; i < j; i++) body.push(''); continue; }
          break;
        }
        var ind = indentOf(l);
        if (ind >= base + 2) { body.push(l.slice(Math.min(ind, contentIndent))); i++; continue; }
        var sib = RE_LIST.exec(l);
        if (sib) break;                          // 同级或上级的列表项
        if (startsBlock(l, lines[i + 1])) break; // 别的块
        body.push(l.trim()); i++;               // 懒惰续行
      }

      var blocks = parseBlocks(body);
      var item = { c: blocks };
      var fp = blocks[0];
      if (fp && fp.t === 'p' && fp.c[0] && fp.c[0].t === 'text') {
        var tm = /^\[([ xX])\]\s+/.exec(fp.c[0].v);
        if (tm) { item.checked = tm[1] !== ' '; fp.c[0] = textNode(fp.c[0].v.slice(tm[0].length)); }
      }
      items.push(item);
    }

    var node = { t: ordered ? 'ol' : 'ul', items: items };
    if (ordered) { var sn = parseInt(first[2], 10); if (sn !== 1) node.start = sn; }
    return { node: node, next: i };
  }

  function indentOf(l) { return /^ */.exec(l)[0].length; }

  function parse(text) {
    text = String(text == null ? '' : text).replace(/\r\n?/g, '\n').replace(/\t/g, '    ');
    return parseBlocks(text.split('\n'));
  }

  // ---------- 渲染（只用 DOM API） ----------

  function safeHref(url) { return SAFE_URL.test(url) ? url : ''; }

  function renderInline(nodes, doc, parent) {
    nodes.forEach(function (n) {
      switch (n.t) {
        case 'text': parent.appendChild(doc.createTextNode(n.v)); break;
        case 'br': parent.appendChild(doc.createElement('br')); break;
        case 'code': { var c = doc.createElement('code'); c.textContent = n.v; parent.appendChild(c); break; }
        case 'strong': case 'em': {
          var e = doc.createElement(n.t); renderInline(n.c, doc, e); parent.appendChild(e); break;
        }
        case 'del': { var d = doc.createElement('del'); renderInline(n.c, doc, d); parent.appendChild(d); break; }
        case 'link': {
          var href = safeHref(n.href);
          if (!href) { renderInline(n.c, doc, parent); break; }
          var a = doc.createElement('a');
          a.setAttribute('href', href);
          a.setAttribute('target', '_blank');
          a.setAttribute('rel', 'noopener noreferrer nofollow');
          renderInline(n.c, doc, a);
          parent.appendChild(a);
          break;
        }
        case 'img': {
          // 不加载图片，显示成指向原地址的链接
          var label = '🖼 ' + (n.alt || '图片');
          var ih = safeHref(n.href);
          if (!ih) { parent.appendChild(doc.createTextNode(label)); break; }
          var ia = doc.createElement('a');
          ia.setAttribute('href', ih);
          ia.setAttribute('target', '_blank');
          ia.setAttribute('rel', 'noopener noreferrer nofollow');
          ia.textContent = label;
          parent.appendChild(ia);
          break;
        }
      }
    });
  }

  function renderBlocks(blocks, doc, parent) {
    blocks.forEach(function (b) {
      switch (b.t) {
        case 'p': { var p = doc.createElement('p'); renderInline(b.c, doc, p); parent.appendChild(p); break; }
        case 'h': { var h = doc.createElement('h' + b.level); renderInline(b.c, doc, h); parent.appendChild(h); break; }
        case 'hr': parent.appendChild(doc.createElement('hr')); break;
        case 'code': {
          var pre = doc.createElement('pre'); pre.className = 'md-code';
          if (b.lang) {
            var tag = doc.createElement('div'); tag.className = 'md-lang'; tag.textContent = b.lang;
            pre.appendChild(tag);
          }
          var code = doc.createElement('code'); code.textContent = b.v; pre.appendChild(code);
          parent.appendChild(pre); break;
        }
        case 'quote': { var bq = doc.createElement('blockquote'); renderBlocks(b.c, doc, bq); parent.appendChild(bq); break; }
        case 'ul': case 'ol': {
          var list = doc.createElement(b.t);
          if (b.start) list.setAttribute('start', String(b.start));
          b.items.forEach(function (it) {
            var li = doc.createElement('li');
            if (typeof it.checked === 'boolean') {
              li.className = 'md-task';
              var box = doc.createElement('span');
              box.className = 'md-box' + (it.checked ? ' on' : '');
              box.textContent = it.checked ? '☑' : '☐';
              li.appendChild(box);
            }
            renderBlocks(it.c, doc, li);
            list.appendChild(li);
          });
          parent.appendChild(list); break;
        }
        case 'table': {
          var wrap = doc.createElement('div'); wrap.className = 'md-table';
          var tbl = doc.createElement('table');
          var thead = doc.createElement('thead'), htr = doc.createElement('tr');
          b.head.forEach(function (cell, ci) {
            var th = doc.createElement('th'); if (b.align[ci]) th.setAttribute('align', b.align[ci]);
            renderInline(cell, doc, th); htr.appendChild(th);
          });
          thead.appendChild(htr); tbl.appendChild(thead);
          var tbody = doc.createElement('tbody');
          b.rows.forEach(function (row) {
            var tr = doc.createElement('tr');
            row.forEach(function (cell, ci) {
              var td = doc.createElement('td'); if (b.align[ci]) td.setAttribute('align', b.align[ci]);
              renderInline(cell, doc, td); tr.appendChild(td);
            });
            tbody.appendChild(tr);
          });
          tbl.appendChild(tbody); wrap.appendChild(tbl); parent.appendChild(wrap); break;
        }
      }
    });
  }

  // render 把 Markdown 文本渲染成一个 <div class="md">，doc 默认用页面的 document。
  function render(text, doc) {
    doc = doc || root.document;
    var box = doc.createElement('div');
    box.className = 'md';
    renderBlocks(parse(text), doc, box);
    return box;
  }

  var api = { parse: parse, parseInline: parseInline, render: render };
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  root.QQMD = api;
})(typeof window !== 'undefined' ? window : globalThis);
