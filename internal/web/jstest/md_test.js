// Markdown 渲染器单测：node --test internal/web/jstest/
'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const { parse, parseInline, render } = require('../static/md.js');

// ---------- 最小假 DOM：足够把渲染结果序列化成 HTML 字符串来断言 ----------
class Node_ {
  constructor(tag) { this.tag = tag; this.attrs = {}; this.children = []; this.className = ''; this._text = null; }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  appendChild(c) { this.children.push(c); return c; }
  set textContent(v) { this.children = [new Text_(String(v))]; }
}
class Text_ { constructor(v) { this.v = v; } }
const fakeDoc = {
  createElement: (t) => new Node_(t),
  createTextNode: (v) => new Text_(v),
};
const esc = (s) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
function html(n) {
  if (n instanceof Text_) return esc(n.v);
  const attrs = (n.className ? ` class="${n.className}"` : '') +
    Object.keys(n.attrs).map((k) => ` ${k}="${esc(n.attrs[k])}"`).join('');
  return `<${n.tag}${attrs}>${n.children.map(html).join('')}</${n.tag}>`;
}
const md = (s) => html(render(s, fakeDoc));
const inner = (s) => md(s).replace(/^<div class="md">/, '').replace(/<\/div>$/, '');

// ---------- 块 ----------
test('标题与段落', () => {
  assert.equal(inner('# 一级\n\n正文'), '<h1>一级</h1><p>正文</p>');
  assert.equal(inner('###### 六级'), '<h6>六级</h6>');
  assert.equal(inner('#没有空格不是标题'), '<p>#没有空格不是标题</p>');
});

test('单个换行显示为换行（聊天场景）', () => {
  assert.equal(inner('第一行\n第二行'), '<p>第一行<br></br>第二行</p>'.replace('<br></br>', '<br></br>'));
  assert.equal(parse('a\nb')[0].c.map((x) => x.t).join(), 'text,br,text');
});

test('围栏代码块：原样保留，里面的 markdown 和 html 都不处理', () => {
  const out = inner('```js\nconst a = **1**;\n<b>x</b>\n```');
  assert.match(out, /<pre class="md-code"><div class="md-lang">js<\/div><code>const a = \*\*1\*\*;\n&lt;b&gt;x&lt;\/b&gt;<\/code><\/pre>/);
});

test('没有结束围栏的代码块读到末尾', () => {
  assert.match(inner('```\nabc\ndef'), /<code>abc\ndef<\/code>/);
});

test('引用可以嵌套块', () => {
  assert.equal(inner('> 引用\n> - 项'), '<blockquote><p>引用</p><ul><li><p>项</p></li></ul></blockquote>');
});

test('分隔线', () => {
  for (const s of ['---', '***', '___', '* * *']) assert.equal(inner(s), '<hr></hr>', s);
});

test('无序列表与嵌套', () => {
  const out = inner('- a\n  - a1\n  - a2\n- b');
  assert.equal(out, '<ul><li><p>a</p><ul><li><p>a1</p></li><li><p>a2</p></li></ul></li><li><p>b</p></li></ul>');
});

test('有序列表保留起始编号', () => {
  assert.equal(inner('1. 甲\n2. 乙'), '<ol><li><p>甲</p></li><li><p>乙</p></li></ol>');
  assert.match(inner('3. 丙\n4. 丁'), /<ol start="3">/);
});

test('任务列表', () => {
  const out = inner('- [x] 做完\n- [ ] 待办');
  assert.match(out, /<li class="md-task"><span class="md-box on">☑<\/span><p>做完<\/p><\/li>/);
  assert.match(out, /<li class="md-task"><span class="md-box">☐<\/span><p>待办<\/p><\/li>/);
});

test('列表项里可以有代码块', () => {
  const out = inner('1. 运行：\n   ```bash\n   ls -l\n   ```\n2. 完成');
  assert.match(out, /<li><p>运行：<\/p><pre class="md-code"><div class="md-lang">bash<\/div><code>ls -l<\/code><\/pre><\/li>/);
});

test('表格：表头、对齐、转义的竖线、缺列补空', () => {
  const out = inner('| 名称 | 数量 | 备注 |\n|:--|--:|:-:|\n| 苹果 | 3 | a\\|b |\n| 梨 | 5 |');
  assert.match(out, /<th align="left">名称<\/th><th align="right">数量<\/th><th align="center">备注<\/th>/);
  assert.match(out, /<td align="center">a\|b<\/td>/);
  assert.match(out, /<tr><td align="left">梨<\/td><td align="right">5<\/td><td align="center"><\/td><\/tr>/);
});

test('像表格的普通文字不会被误判', () => {
  assert.equal(inner('a | b\nc | d'), '<p>a | b<br></br>c | d</p>');
});

// ---------- 内联 ----------
test('粗体、斜体、删除线、组合', () => {
  assert.equal(inner('**粗** *斜* ~~删~~'), '<p><strong>粗</strong> <em>斜</em> <del>删</del></p>');
  assert.equal(inner('***又粗又斜***'), '<p><em><strong>又粗又斜</strong></em></p>');
  assert.equal(inner('__粗__ _斜_'), '<p><strong>粗</strong> <em>斜</em></p>');
});

test('单词里的下划线和不成对的星号不当强调', () => {
  assert.equal(inner('snake_case_name 和 my__var__x'), '<p>snake_case_name 和 my__var__x</p>');
  assert.equal(inner('2 * 3 * 4 = 24'), '<p>2 * 3 * 4 = 24</p>');
  assert.equal(inner('a * b'), '<p>a * b</p>');
});

test('行内代码：里面不处理 markdown，支持多反引号', () => {
  assert.equal(inner('`a **b** c`'), '<p><code>a **b** c</code></p>');
  assert.equal(inner('`` a`b ``'), '<p><code>a`b</code></p>');
  assert.equal(inner('没闭合的 ` 反引号'), '<p>没闭合的 ` 反引号</p>');
});

test('反斜杠转义', () => {
  assert.equal(inner('\\*不是斜体\\* 和 \\# 号'), '<p>*不是斜体* 和 # 号</p>');
});

test('链接与裸链接', () => {
  assert.equal(inner('[文档](https://example.com/a?b=1)'),
    '<p><a href="https://example.com/a?b=1" target="_blank" rel="noopener noreferrer nofollow">文档</a></p>');
  assert.match(inner('见 https://example.com/x，然后'), /<a href="https:\/\/example\.com\/x"[^>]*>https:\/\/example\.com\/x<\/a>，然后/);
  assert.match(inner('详见https://example.com/x里面'), /href="https:\/\/example\.com\/x"[^>]*>[^<]*<\/a>里面/);
  assert.match(inner('(https://example.com/a)'), /href="https:\/\/example\.com\/a"/);
  assert.match(inner('<https://example.com/z>'), /href="https:\/\/example\.com\/z"/);
  assert.match(inner('https://en.wikipedia.org/wiki/A_(b)'), /href="https:\/\/en\.wikipedia\.org\/wiki\/A_\(b\)"/);
});

test('图片不加载，显示成链接', () => {
  const out = inner('![截图](https://example.com/a.png)');
  assert.match(out, /<a href="https:\/\/example\.com\/a\.png"[^>]*>🖼 截图<\/a>/);
  assert.ok(!out.includes('<img'));
});

test('中文标点紧挨着强调符号', () => {
  assert.equal(inner('这是**重点**，请看'), '<p>这是<strong>重点</strong>，请看</p>');
});

// ---------- 安全 ----------
test('HTML 标签一律当文字，不生成元素', () => {
  const out = inner('<script>alert(1)</script> <img src=x onerror=alert(1)> <b>粗</b>');
  assert.ok(!out.includes('<script'), out);
  assert.ok(!out.includes('<img'), out);
  assert.ok(!out.includes('<b>'), out);
  assert.match(out, /&lt;script&gt;alert\(1\)&lt;\/script&gt;/);
});

test('危险协议的链接不生成 <a>', () => {
  for (const url of ['javascript:alert(1)', 'JaVaScRiPt:alert(1)', 'data:text/html;base64,AAAA', 'vbscript:x', '//evil.example/x', ' javascript:alert(1)']) {
    const out = inner(`[点我](${url})`);
    assert.ok(!out.includes('<a '), `${url} -> ${out}`);
    assert.ok(!out.includes('href'), `${url} -> ${out}`);
    assert.match(out, /点我/);
  }
  assert.ok(!inner('![x](javascript:alert(1))').includes('href'));
});

test('链接文字里的 HTML 也只是文字', () => {
  const out = inner('[<img src=x onerror=alert(1)>](https://example.com)');
  assert.ok(!out.includes('<img'), out);
  assert.match(out, /&lt;img src=x onerror=alert\(1\)&gt;/);
});

test('属性里的引号被转义，不能逃逸出 href', () => {
  const out = inner('[x](https://e.com/"onmouseover="alert(1))');
  // 要么不成链接，要么引号被转义；不能出现可执行的属性
  assert.ok(!/ onmouseover=/.test(out.replace(/&quot;/g, '')) || out.includes('&quot;'), out);
  assert.ok(!/<a [^>]*" onmouseover=/.test(out), out);
});

test('链接都带 noopener noreferrer', () => {
  for (const s of ['[a](https://e.com)', 'https://e.com/x', '<https://e.com>', '![i](https://e.com/i.png)']) {
    const out = inner(s);
    assert.match(out, /rel="noopener noreferrer nofollow"/, s);
    assert.match(out, /target="_blank"/, s);
  }
});

test('空输入、null、超长不崩', () => {
  assert.equal(inner(''), '');
  assert.equal(inner(null), '');
  assert.equal(inner(undefined), '');
  const big = ('**a** `b` [c](https://e.com) ').repeat(20000);
  const t0 = Date.now();
  md(big);
  assert.ok(Date.now() - t0 < 3000, '大文本渲染应在 3 秒内完成');
});

test('病态输入不会卡死（回溯/死循环）', () => {
  const nasty = ['*'.repeat(5000), '_'.repeat(5000), '['.repeat(3000), '`'.repeat(3000), '**a*'.repeat(2000),
    '[a](' .repeat(2000), '|'.repeat(3000), '- '.repeat(3000), '> '.repeat(500) + 'x'];
  for (const s of nasty) {
    const t0 = Date.now();
    md(s);
    assert.ok(Date.now() - t0 < 3000, `病态输入耗时过长：${s.slice(0, 12)}…`);
  }
});

test('真实的 QQ 消息和 CodeBuddy 输出', () => {
  const out = inner('## 结果\n\n- **完成**：改了 `main.go`\n- 见 [PR](https://example.com/pr/1)\n\n```go\nfmt.Println("hi")\n```\n\n| 项 | 状态 |\n|---|---|\n| 测试 | ✅ |');
  assert.match(out, /<h2>结果<\/h2>/);
  assert.match(out, /<strong>完成<\/strong>：改了 <code>main\.go<\/code>/);
  assert.match(out, /<pre class="md-code"><div class="md-lang">go<\/div><code>fmt\.Println\(&quot;hi&quot;\)<\/code><\/pre>/);
  assert.match(out, /<td>测试<\/td><td>✅<\/td>/);
});

test('斜杠指令和普通文字保持原样', () => {
  assert.equal(inner('/switch 2'), '<p>/switch 2</p>');
  assert.equal(inner('帮我看看 openplatform 的错误码'), '<p>帮我看看 openplatform 的错误码</p>');
  assert.equal(parseInline('a').length, 1);
});
