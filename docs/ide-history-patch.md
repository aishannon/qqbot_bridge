# IDE 历史实时刷新补丁

QQ 桥接的会话每一轮都会镜像到 CodeBuddy IDE 的「历史对话」存储（`internal/genie`），
但 IDE 把历史读进内存后不再监听磁盘，所以外部写入的新消息要重启 IDE 才能看到。
本补丁给 IDE 内置的 Genie 扩展加上「磁盘变化 → 清缓存 → 刷新界面」的链路。

> 补丁直接修改 IDE 安装目录里的 JS，**不属于 IDE 官方能力**。IDE 升级后会被覆盖，
> 需要重新打；锚点对不上时脚本会拒绝执行，不会写出半截补丁。

## 一、数据链路与四层缓存

```
CodeBuddy CLI ──jsonl──▶ internal/genie ──写入──▶ Genie 历史库（index.json / messages/*.json）
                                                        │
        ┌───────────────────────────────────────────────┘
        ▼
  ① HistoryStorage.indexCache / conversationManagerCache      （会话列表、索引）
  ② HistoryManager.conversationManagerCache / conversationCache（每个会话的请求索引）
  ③ HistoryController.messageServiceCache                      （消息分页读取）
  ④ webview useMessages 的 ie.current 缓存                     （已打开会话的消息列表）
```

排查结论（按出现顺序）：

| 现象 | 根因 | 补丁 |
|------|------|------|
| 历史列表不出现新会话 | ① 缓存 | 宿主：`getConversations` 前比较索引 mtime，变了就清缓存并重载 |
| 会话列表有了，内容还是旧的 | ③ 消息服务缓存 | 宿主：`getOrCreateCachedMessageService` 同样比较 mtime 后清 `messageServiceCache` |
| 宿主读到新数据，界面仍然 `history-not-newer` | ④ webview 判定「历史不比缓存新」整页丢弃 | webview：按消息 ID 增量合并；无新增也强制重渲染 |
| 点刷新显示的消息数不变 | ② `HistoryManager` 自己的会话管理器缓存 | 宿主：mtime 变化时一并清 `historyManager.conversationManagerCache` / `conversationCache` |
| Reload Window 后 webview 行为不变 | webview JS 文件名固定，被浏览器缓存 | 每次打补丁生成新文件名并改写 `index.html` |

另外有两个坑：

- 聊天窗口和历史列表跑在**不同的 extension host**，每个 host 的缓存是独立的，
  所以 mtime 检查必须放在每个入口里，而不是靠一个全局事件。
- 点 IDE 自己的聊天标签看不到 QQ 消息，必须从「历史对话」打开
  「创建分支修复 …」这类桥接会话（会话 ID 由 `qqbot:<codebuddy session id>` 的 md5 得到）。

## 二、补丁内容

由 `scripts/patch_ide_history.py` 生成，改动 4 个文件（都在 `extensions/genie/out/` 下）：

| 文件 | 改动 |
|------|------|
| `extension/index.js` | 3 处：`getConversations`、`getConversationsWithState`、`getOrCreateCachedMessageService` |
| `webviews/main.<hash>.chunk.js` → `main.<tag>.chunk.js` | 历史列表 3 秒轮询；会话页 3 秒轮询 + 增量合并 + 右上角「⟳ 刷新」按钮 |
| `webviews/runtime.<hash>.js` → `runtime.<tag>.js` | chunk 映射改指新文件 |
| `webviews/index.html` | `<script src>` 指向 `runtime.<tag>.js` |

原文件保留为同目录下的 `*.qqbot-bak` / `*.qqbot-bak2`，永远是最初的干净版本。

## 三、怎么出补丁

```bash
cd qqbot_bridge

# 1. 先只检查锚点（不写文件）
scripts/patch_ide_history.py --check

# 2. 打补丁（自动选 ~/.codebuddy-server-cn/bin/ 下最新的 stable-* 目录，按目录本身的修改时间排）
scripts/patch_ide_history.py
#   或指定目录
scripts/patch_ide_history.py --root ~/.codebuddy-server-cn/bin/stable-<hash>/extensions/genie/out

# 3. 在 IDE（远程窗口）里 Developer: Reload Window
```

要求 Python 3.6+，有 `node` 时会自动做语法检查（`vm.Script`）。脚本是幂等的：
基于备份重新生成，重复执行不会叠加；webview 每次换一个时间戳文件名并清理旧的。

还原：

```bash
scripts/patch_ide_history.py --revert     # 用备份还原，然后 Reload Window
```

### SSH 远程连接

补丁在**远端服务器**上，本地 IDE 不用动：在远程窗口里执行 Reload Window 即可。
若 Reload 后行为没变，用 `Remote-SSH: Kill VS Code Server on Host` 后重连。

## 四、IDE 升级后怎么重新出补丁

1. IDE 升级会在 `~/.codebuddy-server-cn/bin/` 下出现新的 `stable-<hash>/` 目录，旧补丁不会带过去
   （旧目录里的补丁还在，但 IDE 已经不用它了，所以表现为「刷新按钮不见了」）。
2. 先 `scripts/patch_ide_history.py --check`，确认它选中的是新目录（输出第一行「目标：…」）：
   - 全部匹配：直接 `scripts/patch_ide_history.py` 打补丁即可，再 Reload Window。
   - 报「锚点匹配 0 处」：新版本的压缩代码变了，需要更新脚本里的锚点，方法见下。
   - 目标不是新目录：用 `--root ~/.codebuddy-server-cn/bin/stable-<新hash>/extensions/genie/out` 显式指定。

> 历史上踩过的坑：自动选目录曾按 `extension/index.js` 的修改时间排序，而打过补丁的旧目录里这个文件
> 反而更新，结果新装的目录被跳过。现在改成按 `stable-*` 目录本身的时间排。

### 更新锚点

锚点都是压缩后的 JS 片段，按下面方式在新版本里重新定位（以 `getConversations` 为例）：

```bash
G=~/.codebuddy-server-cn/bin/stable-<新hash>/extensions/genie/out
python3 - <<'PY'
import os
s=open(os.path.expanduser('<G>/extension/index.js'),encoding='utf-8').read()
i=s.find('async getConversations(){this.ensureInitialized()')
print(s[i:i+400])
PY
```

| 补丁 | 定位关键字 |
|------|-----------|
| 宿主 `getConversations` | `async getConversations(){this.ensureInitialized()` |
| 宿主 `getConversationsWithState` | `async getConversationsWithState(){const ir=this.conversationsWithStateSnapshot` |
| 宿主消息缓存 | `async getOrCreateCachedMessageService(` |
| 宿主 fs 别名确认 | `ensureStorageDirectories` 里的 `Rn.existsSync` |
| webview 历史列表 | `fetchConversationsAndCurrent` 附近的 `useGetConversations` |
| webview 会话页 | `[useMessages] focus refresh kept` 日志字符串所在的 `Oe=(0,s.useCallback)` |

压缩后的变量名（`Rn`、`Oe`、`ie`、`G` …）每次构建都可能变，改锚点时要同步改补丁正文里的变量名。
改完后：`--check` → 在临时目录复现（把 `*.qqbot-bak*` 拷过去用 `--root` 指向它）→ 再打到真实目录。

## 五、怎么验证

远端扩展日志（不是本地客户端日志）：

```bash
tail -f $(ls -t ~/.codebuddy-server-cn/data/logs/*/exthost*/Tencent-Cloud.coding-copilot/*.log | head -1)
```

重点看：

| 日志 | 含义 |
|------|------|
| `[qqbot] reloaded IDE history from disk` | 历史列表入口检测到索引变化 |
| `[qqbot] cleared message cache` | 消息入口检测到索引变化并清了缓存 |
| `getMessagesPaginatedByMessageBudget SUCCESS: messageCount=…` | 宿主读到的条数在增长 |
| `[useMessages] qqbot merged N new messages` | webview 合并了新消息 |
| `focus refresh applied (qqbot-poll)` | webview 整页替换成功 |
| `[qqbot] message cache check failed` | mtime 检查本身出错，需要看原因 |

数据对账：

```bash
# 磁盘上的请求数
jq '.requests|length' <history>/<conv-id>/index.json
# 宿主返回的最后一个请求 id 应该是磁盘上最后一页里的请求
```

## 六、不想动 IDE 时的替代方案

- **Web 管理端的「会话」标签**：直接读 CodeBuddy 的会话文件，显示完整对话日志（文本、推理、工具调用和结果，
  文本按 Markdown 渲染），自动增量刷新，不经过 IDE 的任何缓存。需要先 `qqbot_bridge passwd` 设置管理端密码。
- 在 IDE 终端里 `codebuddy -r <session-id>` 直接恢复会话，永远是最新内容。
- QQ 里发 `/sync` 手动把当前会话再镜像一次。
