#!/usr/bin/env python3
"""给 CodeBuddy IDE 的 Genie 扩展打「QQ 桥接会话实时刷新」补丁。

背景见 docs/ide-history-patch.md。补丁只改远端 IDE 扩展目录里的 JS：

  <ide>/extensions/genie/out/extension/index.js          扩展宿主：缓存失效
  <ide>/extensions/genie/out/webviews/main.<hash>.chunk.js  webview：轮询 + 刷新按钮
  <ide>/extensions/genie/out/webviews/runtime.<hash>.js      webview：chunk 映射
  <ide>/extensions/genie/out/webviews/index.html             webview：入口

用法：
  scripts/patch_ide_history.py                  # 自动找最新的 stable-* 目录并打补丁
  scripts/patch_ide_history.py --root <genie/out>   # 指定目录
  scripts/patch_ide_history.py --check          # 只检查锚点是否都能匹配，不写文件
  scripts/patch_ide_history.py --revert         # 用备份还原

幂等：重复执行会基于 .qqbot-bak* 备份重新生成，不会叠加补丁。
webview 文件名固定会被浏览器缓存，所以每次都生成带时间戳的新文件名并改写 index.html。
"""

import argparse
import glob
import os
import re
import shutil
import subprocess
import sys
import time

MARK_HOST = "_qqbotReloadIfChanged"
BAK_HOST = ".qqbot-bak"
BAK_WEB = ".qqbot-bak"
BAK_RUNTIME = ".qqbot-bak2"
BAK_HTML = ".qqbot-bak2"

# ---------------------------------------------------------------- 宿主补丁

HOST_PATCHES = []

# A. 会话列表入口：索引文件 mtime 变化时清掉所有历史缓存并重新加载
HOST_PATCHES.append((
    "getConversations",
    'async getConversations(){this.ensureInitialized();return Array.from(this.conversationMap.values()).sort(((ir,ar)=>new Date(ar.lastMessageAt).getTime()-new Date(ir.lastMessageAt).getTime()))}',
    'async getConversations(){this.ensureInitialized();await this._qqbotReloadIfChanged();return Array.from(this.conversationMap.values()).sort(((ir,ar)=>new Date(ar.lastMessageAt).getTime()-new Date(ir.lastMessageAt).getTime()))}'
    'async _qqbotReloadIfChanged(){try{const p=this.historyStorage&&this.historyStorage.historyManager&&this.historyStorage.historyManager.indexPath;if(!p)return;const m=Rn.statSync(p).mtimeMs;if(this._qqbotIndexMtime===void 0){this._qqbotIndexMtime=m;return}if(m===this._qqbotIndexMtime)return;this._qqbotIndexMtime=m;this.historyStorage.clearIndexCache();try{this.historyStorage.clearConversationCache&&this.historyStorage.clearConversationCache()}catch(_){}'
    'try{const hm=this.historyStorage.historyManager;if(hm){hm.conversationManagerCache&&hm.conversationManagerCache.clear();hm.conversationCache&&hm.conversationCache.clear()}}catch(_){}'
    'const cs=await this.historyStorage.getConversations();for(const c of cs){const old=this.conversationMap.get(c.id);this.conversationMap.set(c.id,old?{...old,...c}:c)}this.historyStorage._qqbotGeneration=(this.historyStorage._qqbotGeneration||0)+1;this.logger&&this.logger.info&&this.logger.info("[qqbot] reloaded IDE history from disk")}catch(ir){}}',
))

# B. 带状态的会话列表有 TTL 快照，直接作废
HOST_PATCHES.append((
    "getConversationsWithState",
    'async getConversationsWithState(){const ir=this.conversationsWithStateSnapshot;if(ir&&Date.now()<ir.expiresAt)',
    'async getConversationsWithState(){this.conversationsWithStateSnapshot=void 0;const ir=this.conversationsWithStateSnapshot;if(ir&&Date.now()<ir.expiresAt)',
))

# C. 消息读取入口：聊天窗口和历史列表在不同 extension host，各自检查索引 mtime
HOST_PATCHES.append((
    "getOrCreateCachedMessageService",
    'async getOrCreateCachedMessageService(ir){var ar,tn,rn;let nn=this.messageServiceCache.get(ir);',
    'async getOrCreateCachedMessageService(ir){var ar,tn,rn;try{const qqbotHS=this.conversationManager&&this.conversationManager.historyStorage;const qqbotP=qqbotHS&&qqbotHS.historyManager&&qqbotHS.historyManager.indexPath;if(qqbotP){const qqbotM=require("fs").statSync(qqbotP).mtimeMs;if(this._qqbotMsgMtime===void 0){this._qqbotMsgMtime=qqbotM}else if(qqbotM!==this._qqbotMsgMtime){this._qqbotMsgMtime=qqbotM;'
    'for(const qqbotSvc of this.messageServiceCache.values()){try{qqbotSvc.dispose()}catch(qqbotErr){}}this.messageServiceCache.clear();'
    'try{qqbotHS.clearIndexCache&&qqbotHS.clearIndexCache()}catch(qqbotErr){}try{qqbotHS.clearConversationCache&&qqbotHS.clearConversationCache()}catch(qqbotErr){}'
    'try{const qqbotHM=qqbotHS.historyManager;if(qqbotHM){qqbotHM.conversationManagerCache&&qqbotHM.conversationManagerCache.clear();qqbotHM.conversationCache&&qqbotHM.conversationCache.clear()}}catch(qqbotErr){}'
    'this.logger&&this.logger.info&&this.logger.info("[qqbot] cleared message cache")}}}catch(qqbotErr){if(!this._qqbotMsgErrLogged){this._qqbotMsgErrLogged=true;this.logger&&this.logger.warn&&this.logger.warn("[qqbot] message cache check failed: "+qqbotErr)}}let nn=this.messageServiceCache.get(ir);',
))

# 宿主补丁里用到了模块内的 fs 别名 Rn，先确认这个版本里它确实是 fs
HOST_ASSERT = 'Rn.existsSync(ar)||await Sn.promises.mkdir(ar,{recursive:!0})'

# ---------------------------------------------------------------- webview 补丁

WEB_PATCHES = []

# D. 历史列表：每 3 秒拉一次
WEB_PATCHES.append((
    "history-list-poll",
    '(0,d.useEffect)(()=>{let e1=!1;return(async()=>{try{const r=(0,g.getConversationViewService)(),{conversations:o,currentId:i,currentOk:s}=await fetchConversationsAndCurrent(r);',
    '(0,d.useEffect)(()=>{let e1=!1;const qqbotPoll=setInterval(()=>{s().catch(()=>{})},3000);return(async()=>{try{const r=(0,g.getConversationViewService)(),{conversations:o,currentId:i,currentOk:s}=await fetchConversationsAndCurrent(r);',
    1,
))
WEB_PATCHES.append((
    "history-list-poll-cleanup",
    '}})(),()=>{e1=!0}},[]),(0,d.useEffect)(()=>{const e1=(0,g.getConversationViewService)().stateChangeSubject',
    '}})(),()=>{e1=!0;clearInterval(qqbotPoll)}},[]),(0,d.useEffect)(()=>{const e1=(0,g.getConversationViewService)().stateChangeSubject',
    1,
))

# E. 打开的会话：历史页比缓存短时，按消息 ID 增量合并而不是整页丢弃
WEB_PATCHES.append((
    "messages-merge",
    'if("replace"!==c.apply)return void U.info(`[useMessages] focus refresh kept (${e1}): reason=${c.reason}, cache=${ie.current.length}, history=${s.length}, chatState=${se.current}`);',
    'if("replace"!==c.apply){const qqbotIds=new Set(ie.current.map(m=>m.id));const qqbotFresh=s.filter(m=>!qqbotIds.has(m.id));if(qqbotFresh.length>0){const qqbotMerged=[...ie.current,...qqbotFresh].sort((a,b)=>(a.createTime||0)-(b.createTime||0));ie.current=qqbotMerged;G([...qqbotMerged]);U.info(`[useMessages] qqbot merged ${qqbotFresh.length} new messages`);return}return void U.info(`[useMessages] focus refresh kept (${e1}): reason=${c.reason}, cache=${ie.current.length}, history=${s.length}, chatState=${se.current}`)}',
    1,
))

# F. 没有新消息时也强制重渲染一次（上面合并块内和原有 inflight 分支共 2 处）
WEB_PATCHES.append((
    "messages-rerender",
    'return void U.info(`[useMessages] focus refresh kept',
    'G([...ie.current]);return void U.info(`[useMessages] focus refresh kept',
    2,
))

# G. 打开的会话：3 秒轮询 + 右上角手动刷新按钮
WEB_PATCHES.append((
    "messages-poll-and-button",
    '}}},[L,Q,U]),xe=(0,s.useCallback)(async e1=>{',
    '}}},[L,Q,U]),qqbotMsgPoll=(0,s.useEffect)(()=>{const t=setInterval(()=>{Oe("qqbot-poll").catch(()=>{})},3000);let qb=document.getElementById("qqbot-refresh-btn");if(!qb){qb=document.createElement("button");qb.id="qqbot-refresh-btn";qb.textContent="⟳ 刷新";qb.title="从磁盘重新读取本会话最新消息";qb.style.cssText="position:fixed;top:6px;right:84px;z-index:99999;padding:2px 10px;font-size:12px;border:1px solid #888;border-radius:4px;background:#2d6cdf;color:#fff;cursor:pointer;opacity:.9";document.body.appendChild(qb)}'
    'const reset=()=>{setTimeout(()=>{qb.textContent="⟳ 刷新"},2000)};const run=async()=>{if(!L)return;qb.textContent="刷新中…";try{const r0=i.ContainerUtil.get(l.ChatViewService);const a0=await r0.getMessagesPaginatedByMessageBudget(L,{targetMessageCount:M,maxRequests:S});if(le.current!==L)return;const s0=(0,d.excludeDiscardedRequestMessages)(Ae(a0.messages),me.current);ie.current=s0;G([...s0]);q(void 0);X(a0.hasMore);a0.lastRequestId?pe.current=a0.lastRequestId:s0.length>0&&(pe.current=s0[0].requestId);qb.textContent="✓ 已刷新 "+s0.length;reset()}catch(e0){qb.textContent="刷新失败";reset()}};'
    'qb.onclick=run;return()=>{clearInterval(t);qb.onclick=null;if(qb.parentNode)qb.parentNode.removeChild(qb)}},[Oe]),xe=(0,s.useCallback)(async e1=>{',
    1,
))


# ---------------------------------------------------------------- 工具函数

def log(msg):
    print(msg, flush=True)


def die(msg):
    print("错误：" + msg, file=sys.stderr)
    sys.exit(1)


def read(path):
    with open(path, encoding="utf-8") as f:
        return f.read()


def write(path, text):
    with open(path, "w", encoding="utf-8") as f:
        f.write(text)


def find_root():
    base = os.path.expanduser("~/.codebuddy-server-cn/bin")
    cands = glob.glob(os.path.join(base, "stable-*", "extensions", "genie", "out"))
    cands = [c for c in cands if os.path.isfile(os.path.join(c, "extension", "index.js"))]
    if not cands:
        die("找不到 ~/.codebuddy-server-cn/bin/stable-*/extensions/genie/out，请用 --root 指定")
    # 按 stable-* 目录本身的时间排：补丁会改 index.js 的 mtime，旧版本反而显得更新
    stable = lambda p: p.split(os.sep + "extensions" + os.sep)[0]
    cands.sort(key=lambda p: os.path.getmtime(stable(p)), reverse=True)
    return cands[0]


def pristine(path, bak_suffix, marker):
    """返回未打补丁的原始内容：优先用备份；没有备份且当前文件是干净的就先建备份。"""
    bak = path + bak_suffix
    if os.path.exists(bak):
        return read(bak), bak
    text = read(path)
    if marker and marker in text:
        die(f"{path} 已被打过补丁但找不到备份 {bak}，无法安全重打；请先从 IDE 重装/还原该文件")
    return text, bak


def apply_patches(text, patches, label):
    for name, old, new, *rest in patches:
        want = rest[0] if rest else 1
        n = text.count(old)
        if n != want:
            die(f"{label}：补丁「{name}」锚点匹配 {n} 处（期望 {want}）；IDE 版本可能变了，需要更新脚本里的锚点")
        text = text.replace(old, new)
    return text


def node_check(path):
    if not shutil.which("node"):
        log(f"  跳过语法检查（没有 node）：{os.path.basename(path)}")
        return
    code = (
        "const fs=require('fs'),vm=require('vm');"
        "try{new vm.Script(fs.readFileSync(process.argv[1],'utf8'),{filename:process.argv[1]})}"
        "catch(e){console.error(e.message);process.exit(1)}"
    )
    r = subprocess.run(["node", "-e", code, path], stdout=subprocess.PIPE,
                       stderr=subprocess.PIPE, universal_newlines=True)
    if r.returncode != 0:
        die(f"语法检查失败 {path}：{r.stderr.strip()[:300]}")
    log(f"  语法检查通过：{os.path.basename(path)}")


# ---------------------------------------------------------------- 主流程

def current_runtime(web):
    html = read(os.path.join(web, "index.html"))
    m = re.search(r'src="(runtime\.[^"]+\.js)"', html)
    if not m:
        die("index.html 里找不到 runtime 脚本引用")
    return m.group(1)


def original_runtime_name(web):
    """未打补丁时 index.html 引用的是 webpack 生成的 runtime.<hash>.js。"""
    bak = os.path.join(web, "index.html" + BAK_HTML)
    html = read(bak) if os.path.exists(bak) else read(os.path.join(web, "index.html"))
    m = re.search(r'src="(runtime\.[0-9a-f]{20}\.js)"', html)
    if not m:
        die("找不到原始 runtime.<hash>.js 引用；index.html 可能已被改写且没有备份")
    return m.group(1)


def main_chunk_hash(runtime_text, web):
    for m in re.finditer(r'main:"([0-9a-f]{8})"', runtime_text):
        if os.path.exists(os.path.join(web, f"main.{m.group(1)}.chunk.js")):
            return m.group(1)
    die("runtime 里找不到对应的 main.<hash>.chunk.js")


def patch_host(root, dry):
    path = os.path.join(root, "extension", "index.js")
    text, bak = pristine(path, BAK_HOST, MARK_HOST)
    if HOST_ASSERT not in text:
        die("宿主：找不到 Rn=fs 的确认锚点，模块别名可能已变化，需要更新脚本")
    out = apply_patches(text, HOST_PATCHES, "宿主 index.js")
    if dry:
        log("  宿主锚点全部匹配")
        return
    if not os.path.exists(bak):
        shutil.copy2(path, bak)
    write(path, out)
    node_check(path)


def patch_web(root, tag, dry):
    web = os.path.join(root, "webviews")
    orig_runtime = original_runtime_name(web)
    runtime_path = os.path.join(web, orig_runtime)
    runtime_text = read(runtime_path)
    h = main_chunk_hash(runtime_text, web)

    main_path = os.path.join(web, f"main.{h}.chunk.js")
    main_text, main_bak = pristine(main_path, BAK_WEB, "qqbotPoll")
    out_main = apply_patches(main_text, WEB_PATCHES, "webview main chunk")

    old = f'main:"{h}"'
    if runtime_text.count(old) != 1:
        die(f"runtime 里 main:\"{h}\" 匹配 {runtime_text.count(old)} 处（期望 1）")
    out_runtime = runtime_text.replace(old, f'main:"{tag}"')

    html_path = os.path.join(web, "index.html")
    html_text, html_bak = pristine(html_path, BAK_HTML, "qqbotfix")
    ref = f'src="{orig_runtime}"'
    if html_text.count(ref) != 1:
        die(f"index.html 里 {ref} 匹配 {html_text.count(ref)} 处（期望 1）")
    out_html = html_text.replace(ref, f'src="runtime.{tag}.js"')

    if dry:
        log("  webview 锚点全部匹配")
        return

    # 备份只做一次，永远保存最初的干净版本
    for src, bak in ((main_path, main_bak), (html_path, html_bak)):
        if not os.path.exists(bak):
            shutil.copy2(src, bak)
    rt_bak = runtime_path + BAK_RUNTIME
    if not os.path.exists(rt_bak):
        shutil.copy2(runtime_path, rt_bak)

    # 主 chunk 保持原文件不动，补丁内容写成新文件名，绕过浏览器缓存
    new_main = os.path.join(web, f"main.{tag}.chunk.js")
    new_runtime = os.path.join(web, f"runtime.{tag}.js")
    write(new_main, out_main)
    write(new_runtime, out_runtime)
    write(html_path, out_html)
    node_check(new_main)
    log(f"  webview 新文件：main.{tag}.chunk.js / runtime.{tag}.js，index.html 已指向它们")


def cleanup_old_tags(root, keep_tag):
    web = os.path.join(root, "webviews")
    for pat in ("main.qqbot*.chunk.js", "runtime.qqbot*.js"):
        for f in glob.glob(os.path.join(web, pat)):
            if keep_tag not in os.path.basename(f):
                os.remove(f)
                log(f"  清理旧补丁文件：{os.path.basename(f)}")


def revert(root):
    host = os.path.join(root, "extension", "index.js")
    web = os.path.join(root, "webviews")
    pairs = [
        (host, host + BAK_HOST),
        (os.path.join(web, "index.html"), os.path.join(web, "index.html" + BAK_HTML)),
    ]
    for f in glob.glob(os.path.join(web, "main.*.chunk.js" + BAK_WEB)):
        pairs.append((f[: -len(BAK_WEB)], f))
    done = 0
    for dst, bak in pairs:
        if os.path.exists(bak):
            shutil.copy2(bak, dst)
            log(f"  已还原：{os.path.basename(dst)}")
            done += 1
    for pat in ("main.qqbot*.chunk.js", "runtime.qqbot*.js"):
        for f in glob.glob(os.path.join(web, pat)):
            os.remove(f)
            log(f"  已删除：{os.path.basename(f)}")
    if not done:
        die("没有找到任何备份，无需还原")


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--root", help="genie/out 目录，默认自动找最新的 stable-*")
    ap.add_argument("--tag", help="webview 新文件名标签，默认 qqbot<时间戳>")
    ap.add_argument("--check", action="store_true", help="只检查锚点，不写文件")
    ap.add_argument("--revert", action="store_true", help="用备份还原")
    args = ap.parse_args()

    root = os.path.abspath(args.root) if args.root else find_root()
    log(f"目标：{root}")
    if args.revert:
        revert(root)
        log("还原完成，需要 Developer: Reload Window")
        return

    tag = args.tag or time.strftime("qqbot%Y%m%d%H%M%S")
    log("宿主 extension/index.js：")
    patch_host(root, args.check)
    log("webview：")
    patch_web(root, tag, args.check)
    if args.check:
        log("检查通过：所有锚点都匹配")
        return
    cleanup_old_tags(root, tag)
    log("完成。请在 IDE 里执行 Developer: Reload Window。")


if __name__ == "__main__":
    main()
