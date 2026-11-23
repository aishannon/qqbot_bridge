#!/usr/bin/env bash
# 一键安装 QQBot Bridge：编译并装到用户目录。Linux、macOS、Windows（Git Bash）都能跑。
#
#   ./scripts/install.sh                    # 只装二进制
#   ./scripts/install.sh --service          # Linux：顺便装成 systemd 用户服务
#   PREFIX=/usr/local/bin ./scripts/install.sh
#
# 重复执行等于升级。已经有实例在跑时会询问要不要重启，不直接重启。
# Windows 没有后台模式，重启只是停掉旧进程，再按打印出来的命令前台启动。
set -euo pipefail

# ---- 平台 -----------------------------------------------------------------
uname_s="$(uname -s 2>/dev/null || echo unknown)"
case "$uname_s" in
Linux*) OS=linux ;;
Darwin*) OS=mac ;;
MINGW* | MSYS* | CYGWIN*) OS=windows ;;
*) OS=other ;;
esac

BIN_NAME="qqbot_bridge"
if [ "$OS" = "windows" ]; then
	BIN_NAME="qqbot_bridge.exe"
fi

PREFIX="${PREFIX:-$HOME/.local/bin}"
WITH_SERVICE=0
INSTANCE_NAME=""
REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DATA_DIR="$HOME/.qqbot_bridge"
JUST_STARTED=0
SHOWED_START=0

say() { printf '\033[36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[33m[!]\033[0m %s\n' "$*" >&2; }
die() {
	printf '\033[31m[x]\033[0m %s\n' "$*" >&2
	exit 1
}
# 用户要自己执行的命令单独占一行，前面不加说明文字，方便整行复制。
show_cmd() { printf '%s\n' "$*"; }

usage() {
	cat <<'EOF'
用法：install.sh [--service [--name 实例名]] [--prefix DIR]

  --service         安装 systemd 用户服务并启动（仅 Linux）
  --name 实例名     安装成模板实例 qqbot_bridge@<实例名>.service，
                    数据目录为 ~/.qqbot_bridge-<实例名>，需配合 --service
  --prefix DIR      安装目录，默认 ~/.local/bin
  -h, --help        显示本帮助

同一台机器跑多套（每套一个 QQ 机器人，appid 不能共用，仅 Linux + systemd）：
  ./scripts/install.sh --service
  ./scripts/install.sh --service --name cursor

macOS 与 Windows 没有 systemd。Windows 请在 Git Bash 里运行本脚本，
程序只能前台启动（qqbot_bridge.exe -f）。
EOF
}

confirm() {
	# $1 提示，$2 默认值 y 或 n。非交互终端不提问，按默认值返回。
	local prompt="$1" def="${2:-y}" hint="[Y/n]" ans
	if [ "$def" = "n" ]; then
		hint="[y/N]"
	fi
	if [ ! -t 0 ]; then
		[ "$def" = "y" ]
		return
	fi
	printf '%s %s ' "$prompt" "$hint" >&2
	IFS= read -r ans || ans=""
	ans="$(printf '%s' "$ans" | tr '[:upper:]' '[:lower:]' | tr -d '[:space:]')"
	case "$ans" in
	y | yes | 是 | 好) return 0 ;;
	n | no | 否 | 不) return 1 ;;
	esac
	[ "$def" = "y" ]
}

bin_path() { printf '%s/%s' "$PREFIX" "$BIN_NAME"; }

# 把路径转成本机原生形式（Windows Git Bash 下 Go 认盘符路径）。
native_path() {
	local p="$1"
	if [ "$OS" = "windows" ] && command -v cygpath >/dev/null 2>&1; then
		cygpath -w "$p"
		return
	fi
	printf '%s\n' "$p"
}

# 若路径已是绝对路径则原样返回，否则拼成绝对路径；不解析符号链接，
# 以便保留 cursor-agent 的稳定 shim（.local/bin），避免钉死到带版本号的目录。
abs_path() {
	local p="$1" dir base
	case "$p" in
	/* | [A-Za-z]:/* | [A-Za-z]:\\*)
		printf '%s\n' "$p"
		return
		;;
	esac
	dir="$(cd "$(dirname "$p")" 2>/dev/null && pwd)" || return 1
	base="$(basename "$p")"
	printf '%s/%s\n' "$dir" "$base"
}

# cursor-agent 安装器常把 PATH 指到 .../versions/<hash>/，升级后会失效。
# 官方布局是 agent-cli/.local/share/cursor-agent/versions/<hash>/cursor-agent，
# 稳定入口在同级的 agent-cli/.local/bin/cursor-agent（符号链接，随升级更新）。
prefer_stable_agent() {
	local name="$1" p="$2" shim local_root
	case "$p" in
	*/versions/*/"$name" | */versions/*/"$name".exe)
		# dirname=.../versions/<hash> → ../../../.. = .../.local
		local_root="$(cd "$(dirname "$p")/../../../.." 2>/dev/null && pwd)" || true
		if [ -n "$local_root" ]; then
			shim="$local_root/bin/$name"
			if [ -x "$shim" ] || [ -f "$shim" ]; then
				printf '%s\n' "$shim"
				return
			fi
		fi
		if [ -x "$HOME/.local/bin/$name" ]; then
			printf '%s\n' "$HOME/.local/bin/$name"
			return
		fi
		;;
	esac
	printf '%s\n' "$p"
}

# 在 PATH 与各系统常见安装位置里找 codebuddy / cursor-agent，返回绝对路径。
# 找不到则输出空行。不 follow 到版本目录，见 prefer_stable_agent。
resolve_agent() {
	local name="$1" cand p=""
	if command -v "$name" >/dev/null 2>&1; then
		p="$(command -v "$name")"
	fi
	if [ -z "$p" ]; then
		for cand in \
			"$HOME/.local/bin/$name" \
			"$HOME/bin/$name" \
			"/usr/local/bin/$name" \
			"/opt/homebrew/bin/$name" \
			"$HOME/.cursor/bin/$name" \
			"$HOME/.codebuddy/bin/$name" \
			"$HOME/Library/Application Support/Cursor/User/globalStorage/anysphere.cursor-agent-worker/agent-cli/.local/bin/$name" \
			"$HOME/AppData/Local/cursor-agent/$name" \
			"$HOME/AppData/Local/Programs/cursor/resources/app/resources/helpers/$name"; do
			if [ -x "$cand" ] || [ -f "$cand" ]; then
				p="$cand"
				break
			fi
		done
	fi
	if [ -z "$p" ]; then
		return 0
	fi
	p="$(prefer_stable_agent "$name" "$p")"
	p="$(abs_path "$p")" || return 0
	native_path "$p"
}

# 限时执行命令：stdout+stderr 打到标准输出，超时返回 124。
# macOS 没有 GNU timeout，用后台进程 + sleep 轮询，兼容 bash 3.2。
run_timed() {
	local secs="$1" tmp pid waited=0 rc=0
	shift
	tmp="$(mktemp)" || return 1
	(
		"$@" >"$tmp" 2>&1
	) &
	pid=$!
	while kill -0 "$pid" 2>/dev/null; do
		if [ "$waited" -ge "$secs" ]; then
			kill "$pid" 2>/dev/null || true
			wait "$pid" 2>/dev/null || true
			rm -f "$tmp"
			return 124
		fi
		sleep 1
		waited=$((waited + 1))
	done
	wait "$pid" || rc=$?
	cat "$tmp" 2>/dev/null || true
	rm -f "$tmp"
	return "$rc"
}

# 判断 cursor-agent status 的 JSON 是否已登录。
cursor_status_logged_in() {
	local json="$1"
	printf '%s' "$json" | grep -Eqi '"isAuthenticated"[[:space:]]*:[[:space:]]*true' && return 0
	printf '%s' "$json" | grep -Eqi '"status"[[:space:]]*:[[:space:]]*"authenticated"' && return 0
	return 1
}

# 检查 codebuddy：--version 能跑通即视为可用（登录态由首次任务暴露）。
# 返回：0 可用，1 跑不起来。
check_codebuddy() {
	local bin="$1" out rc=0
	out="$(run_timed 15 "$bin" --version)" || rc=$?
	if [ "$rc" = 124 ]; then
		warn "codebuddy --version 超时"
		return 1
	fi
	if [ "$rc" != 0 ] || [ -z "$(printf '%s' "$out" | tr -d '[:space:]')" ]; then
		warn "codebuddy 无法运行（--version 失败，exit ${rc}）"
		return 1
	fi
	say "codebuddy 可运行：$(printf '%s' "$out" | head -n1)"
	return 0
}

# 检查 cursor-agent：先 --version，再 status 看是否已单独登录（与 IDE 登录态无关）。
# 设了 CURSOR_API_KEY / CURSOR_AUTH_TOKEN 也算通过。
# 返回：0 可用，1 跑不起来，2 能跑但未登录。
check_cursor() {
	local bin="$1" out rc=0
	out="$(run_timed 15 "$bin" --version)" || rc=$?
	if [ "$rc" = 124 ]; then
		warn "cursor-agent --version 超时"
		return 1
	fi
	if [ "$rc" != 0 ] || [ -z "$(printf '%s' "$out" | tr -d '[:space:]')" ]; then
		warn "cursor-agent 无法运行（--version 失败，exit ${rc}）"
		return 1
	fi
	say "cursor-agent 可运行：$(printf '%s' "$out" | head -n1)"

	if [ -n "${CURSOR_API_KEY:-}" ] || [ -n "${CURSOR_AUTH_TOKEN:-}" ]; then
		say "cursor-agent：已设置 CURSOR_API_KEY / CURSOR_AUTH_TOKEN，跳过 login 检查"
		return 0
	fi

	rc=0
	out="$(run_timed 15 "$bin" status --format json)" || rc=$?
	if [ "$rc" = 124 ]; then
		warn "cursor-agent status 超时；请确认本机网络，并执行："
		show_cmd "cursor-agent login"
		return 2
	fi
	if [ "$rc" != 0 ]; then
		warn "cursor-agent status 失败（exit ${rc}）。CLI 需单独登录，与 Cursor IDE 无关："
		show_cmd "cursor-agent login"
		return 2
	fi
	if cursor_status_logged_in "$out"; then
		say "cursor-agent 已登录"
		return 0
	fi
	warn "cursor-agent 未登录（桥接不读 IDE 登录态）。请执行："
	show_cmd "cursor-agent login"
	warn "或给服务设置环境变量 CURSOR_API_KEY"
	return 2
}

# 对已解析到的两个 CLI 做可运行/登录检测，设置：
#   codebuddy_ready / cursor_ready（0/1）
probe_agents() {
	codebuddy_ready=0
	cursor_ready=0
	local rc

	if [ -n "${CODEBUDDY_ABS:-}" ]; then
		if check_codebuddy "$CODEBUDDY_ABS"; then
			codebuddy_ready=1
		fi
	else
		warn "未找到 codebuddy"
	fi

	if [ -n "${CURSOR_ABS:-}" ]; then
		rc=0
		check_cursor "$CURSOR_ABS" || rc=$?
		if [ "$rc" = 0 ]; then
			cursor_ready=1
		fi
	else
		warn "未找到 cursor-agent"
	fi

	if [ "$codebuddy_ready" = 0 ] && [ "$cursor_ready" = 0 ]; then
		warn "目前没有可用的 agent CLI。服务能起来（扫码、Web 管理端可用），但跑不了任务。"
		warn "装好并登录后再发消息：codebuddy，或 cursor-agent login（/ 设 CURSOR_API_KEY）。"
	elif [ "$codebuddy_ready" = 0 ] && [ "$cursor_ready" = 1 ]; then
		say "将使用已就绪的 cursor-agent（安装结束会写入绝对路径并切到 cursor）。"
	elif [ "$codebuddy_ready" = 1 ] && [ "$cursor_ready" = 0 ]; then
		if [ -n "${CURSOR_ABS:-}" ]; then
			warn "cursor-agent 尚未就绪；默认仍可用 codebuddy。修好登录后，/driver cursor 即可切换。"
		fi
	else
		say "两个 CLI 都可用。"
	fi
}

write_agent_paths() {
	local cb_path="" cu_path="" args=()
	cb_path="$(resolve_agent codebuddy || true)"
	cu_path="$(resolve_agent cursor-agent || true)"
	if [ -z "$cb_path" ] && [ -z "$cu_path" ]; then
		warn "未写入 agent 路径：本机仍未找到 codebuddy / cursor-agent。"
		return 0
	fi
	if [ -n "$cb_path" ]; then
		args+=(--codebuddy "$cb_path")
		say "写入 codebuddy：${cb_path}"
	fi
	if [ -n "$cu_path" ]; then
		args+=(--cursor "$cu_path")
		say "写入 cursor-agent：${cu_path}"
	fi
	if ! "$(bin_path)" -home "$DATA_DIR" set-agent-paths "${args[@]}"; then
		warn "写入 agent 绝对路径失败。可稍后手动执行（路径含空格时请加引号）："
		show_cmd "$(bin_path) -home ${DATA_DIR} set-agent-paths --codebuddy <path> --cursor <path>"
		return 0
	fi
}

unit_name() {
	if [ -n "$INSTANCE_NAME" ]; then
		printf 'qqbot_bridge@%s' "$INSTANCE_NAME"
	else
		printf 'qqbot_bridge'
	fi
}

pid_file() { printf '%s/qqbot_bridge.pid' "$DATA_DIR"; }

# bash 3.2 在函数里遇到未放进 if 的失败命令会直接退出，所以探测都放在条件里。
systemd_active() {
	if [ "$OS" != "linux" ]; then
		return 1
	fi
	if ! command -v systemctl >/dev/null 2>&1; then
		return 1
	fi
	if systemctl --user is-active --quiet "$(unit_name)" >/dev/null 2>&1; then
		return 0
	fi
	return 1
}

bin_running() {
	local pidfile pid
	pidfile="$(pid_file)"
	if [ -f "$pidfile" ]; then
		pid="$(tr -cd '0-9' <"$pidfile" || true)"
		if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
			return 0
		fi
	fi
	if [ "$OS" = "windows" ] && command -v tasklist >/dev/null 2>&1; then
		if MSYS_NO_PATHCONV=1 tasklist /FI "IMAGENAME eq qqbot_bridge.exe" 2>/dev/null | grep -q "qqbot_bridge.exe"; then
			return 0
		fi
		return 1
	fi
	if command -v ps >/dev/null 2>&1; then
		# grep 自己的命令行也会带上路径，用 -v grep 去掉。
		if ps -ax -o command= 2>/dev/null | grep -F "$(bin_path)" | grep -v grep >/dev/null; then
			return 0
		fi
	fi
	return 1
}

service_running() {
	if [ "$OS" = "linux" ] && [ "$PREFIX" = "$HOME/.local/bin" ] && systemd_active; then
		return 0
	fi
	# --service 会把单元改成当前 PREFIX，自定义目录也算这份服务在跑。
	if [ "$WITH_SERVICE" = 1 ] && systemd_active; then
		return 0
	fi
	if bin_running; then
		return 0
	fi
	return 1
}

print_restart_cmds() {
	if systemd_active; then
		say "用 systemd 重启："
		show_cmd "systemctl --user restart $(unit_name)"
		SHOWED_START=1
		return
	fi
	if [ "$OS" = "windows" ]; then
		say "在正在跑的窗口按 Ctrl+C 停掉，然后前台启动："
		show_cmd "$(bin_path) -f"
		SHOWED_START=1
		return
	fi
	say "停掉旧进程，再启动新版本："
	show_cmd "$(bin_path) -stop"
	show_cmd "$(bin_path)"
	SHOWED_START=1
}

print_start_cmd() {
	if [ "$OS" = "windows" ]; then
		say "Windows 只能前台运行："
		show_cmd "$(bin_path) -f"
	else
		say "启动服务："
		show_cmd "$(bin_path)"
	fi
	SHOWED_START=1
}

do_restart() {
	if [ "$OS" = "linux" ] && { [ "$PREFIX" = "$HOME/.local/bin" ] || [ "$WITH_SERVICE" = 1 ]; } && systemd_active; then
		systemctl --user restart "$(unit_name)"
		say "已重启 $(unit_name)"
		JUST_STARTED=1
		return
	fi
	if [ "$OS" = "windows" ]; then
		if command -v taskkill >/dev/null 2>&1; then
			MSYS_NO_PATHCONV=1 taskkill /F /IM qqbot_bridge.exe >/dev/null 2>&1 || true
		fi
		say "已停掉旧进程。请在新的终端前台启动："
		show_cmd "$(bin_path) -f"
		SHOWED_START=1
		return
	fi
	"$(bin_path)" -stop >/dev/null 2>&1 || true
	"$(bin_path)" >/dev/null
	say "已重启后台进程"
	JUST_STARTED=1
}

ask_restart() {
	echo
	say "这是重新安装，已有实例在运行。现在重启才会用上刚装的版本。"
	if [ ! -t 0 ]; then
		warn "当前不是交互终端，没有自动重启。需要时执行："
		print_restart_cmds
		return
	fi
	if confirm "要现在重启吗？" y; then
		do_restart
		return
	fi
	say "没有重启。需要时执行："
	print_restart_cmds
}

install_bin() {
	local src="$1" dest
	dest="$(bin_path)"
	mkdir -p "$PREFIX"
	if command -v install >/dev/null 2>&1; then
		install -m755 "$src" "${dest}.new"
	else
		cp "$src" "${dest}.new"
		chmod 755 "${dest}.new" 2>/dev/null || true
	fi
	if ! mv -f "${dest}.new" "$dest"; then
		rm -f "${dest}.new"
		die "无法覆盖正在运行的 ${dest}。先停掉进程再安装。"
	fi
}

while [ $# -gt 0 ]; do
	case "$1" in
	--service) WITH_SERVICE=1 ;;
	--name)
		shift
		INSTANCE_NAME="${1:?--name 需要一个实例名，如 cursor}"
		;;
	--prefix)
		shift
		PREFIX="${1:?--prefix 需要一个目录}"
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		echo "未知参数：$1" >&2
		usage >&2
		exit 1
		;;
	esac
	shift
done

if [ "$OS" = "other" ]; then
	die "不认识的系统：${uname_s}。本脚本支持 Linux、macOS 和 Windows Git Bash。"
fi
if [ "$WITH_SERVICE" = 1 ] && [ "$OS" != "linux" ]; then
	die "--service 只用在 Linux 的 systemd 上。${OS} 请去掉 --service，装好后按脚本打印的命令启动。"
fi
if [ -n "$INSTANCE_NAME" ]; then
	case "$INSTANCE_NAME" in
	*[!A-Za-z0-9_.-]*)
		die "实例名只能用字母、数字、_ . -：${INSTANCE_NAME}"
		;;
	esac
	if [ "$WITH_SERVICE" != 1 ]; then
		warn "实例名只在装服务时有意义。执行："
		show_cmd "$0 --service --name ${INSTANCE_NAME}"
		exit 1
	fi
	DATA_DIR="$HOME/.qqbot_bridge-${INSTANCE_NAME}"
fi

# ---- 环境检查 -------------------------------------------------------------
command -v go >/dev/null 2>&1 || die "没找到 go，先装 Go：https://go.dev/dl/"
GO_VERSION="$(go env GOVERSION | sed 's/^go//')"
GO_NEED="$(awk '$1=="go"{print $2; exit}' "$REPO_DIR/go.mod")"
if [ -n "$GO_NEED" ] && [ "$(printf '%s\n%s\n' "$GO_NEED" "${GO_VERSION%%[-+ ]*}" | sort -V | head -n1)" != "$GO_NEED" ]; then
	die "Go 版本太旧（${GO_VERSION}），本项目需要 ${GO_NEED} 或更高：https://go.dev/dl/"
fi

# 先按 PATH + 常见安装位置解析绝对路径，再探测能否真正跑起来（含 cursor 独立登录）。
# 装完二进制后把可用路径写入配置。
CODEBUDDY_ABS="$(resolve_agent codebuddy || true)"
CURSOR_ABS="$(resolve_agent cursor-agent || true)"
if [ -n "$CODEBUDDY_ABS" ]; then
	say "codebuddy：${CODEBUDDY_ABS}"
fi
if [ -n "$CURSOR_ABS" ]; then
	say "cursor-agent：${CURSOR_ABS}"
fi
probe_agents

if [ ! -f "$REPO_DIR/go.mod" ]; then
	die "没找到源码（${REPO_DIR}/go.mod），请在仓库目录里运行本脚本"
fi

# 覆盖安装之前先看有没有旧进程。编译期间它还会继续跑。
WAS_RUNNING=0
if service_running; then
	WAS_RUNNING=1
fi

# ---- 编译 ---------------------------------------------------------------
say "编译中（${REPO_DIR}）"
BUILD_DIR="$(mktemp -d)"
trap 'rm -rf "$BUILD_DIR"' EXIT
(
	cd "$REPO_DIR"
	CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=$(git describe --tags --always --dirty 2>/dev/null || echo dev) -X main.buildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)" -o "$BUILD_DIR/$BIN_NAME" .
)

# ---- 安装 ---------------------------------------------------------------
install_bin "$BUILD_DIR/$BIN_NAME"
say "已安装：$(bin_path)"

case ":$PATH:" in
*":$PREFIX:"* | *":$PREFIX/:"*) ;;
*)
	warn "${PREFIX} 不在 PATH 里。把下面这一行加进 shell 配置后再开一个终端："
	show_cmd "export PATH=\"${PREFIX}:\$PATH\""
	;;
esac

# 把本机 agent 的绝对路径写进配置，避免后台/systemd 的 PATH 找不到命令。
write_agent_paths

# ---- 可选：systemd 用户服务（仅 Linux）-----------------------------------
if [ "$WITH_SERVICE" = 1 ]; then
	command -v systemctl >/dev/null 2>&1 || die "没有 systemctl，去掉 --service 后按脚本打印的命令手动启动"
	UNIT_DIR="$HOME/.config/systemd/user"
	mkdir -p "$UNIT_DIR"
	if command -v install >/dev/null 2>&1; then
		install -m644 "$REPO_DIR/deploy/qqbot_bridge.service" "$UNIT_DIR/qqbot_bridge.service"
		install -m644 "$REPO_DIR/deploy/qqbot_bridge@.service" "$UNIT_DIR/qqbot_bridge@.service"
	else
		cp "$REPO_DIR/deploy/qqbot_bridge.service" "$UNIT_DIR/qqbot_bridge.service"
		cp "$REPO_DIR/deploy/qqbot_bridge@.service" "$UNIT_DIR/qqbot_bridge@.service"
	fi
	if [ "$PREFIX" != "$HOME/.local/bin" ]; then
		for unit in qqbot_bridge.service qqbot_bridge@.service; do
			sed -i.bak \
				-e "s#$HOME/.local/bin/qqbot_bridge#$PREFIX/qqbot_bridge#g" \
				-e "s#%h/.local/bin/qqbot_bridge#$PREFIX/qqbot_bridge#g" \
				"$UNIT_DIR/$unit"
			rm -f "$UNIT_DIR/$unit.bak"
		done
	fi
	systemctl --user daemon-reload

	if [ "$WAS_RUNNING" = 1 ]; then
		ask_restart
	elif [ -n "$INSTANCE_NAME" ]; then
		systemctl --user enable --now "$(unit_name)" >/dev/null 2>&1 ||
			warn "启动 $(unit_name) 失败。查看状态："
		if ! systemd_active; then
			show_cmd "systemctl --user status $(unit_name)"
		else
			JUST_STARTED=1
			say "实例已启动：$(unit_name)"
		fi
	else
		systemctl --user enable --now qqbot_bridge >/dev/null 2>&1 ||
			warn "启动 qqbot_bridge 失败。查看状态："
		if systemd_active; then
			JUST_STARTED=1
			say "服务已启动"
		else
			show_cmd "systemctl --user status qqbot_bridge"
		fi
	fi

	if [ -n "$INSTANCE_NAME" ]; then
		echo
		say "绑定这个实例自己的机器人："
		show_cmd "$(bin_path) -home ${DATA_DIR} config -addr 127.0.0.1:8687"
		say "状态："
		show_cmd "systemctl --user status $(unit_name)"
		say "日志："
		show_cmd "journalctl --user -u $(unit_name) -f"
		warn "同一个 appid 只能给一个实例用。第二套请扫另一个机器人，并把 -addr 换成没被占用的端口。"
	else
		echo
		say "再加一套（另一个机器人、另一个端口）："
		show_cmd "$0 --service --name cursor"
	fi
elif [ "$WAS_RUNNING" = 1 ]; then
	ask_restart
fi

# ---- 收尾 ---------------------------------------------------------------
echo
if ! grep -Eq '^ *appId: *"?[^" ]|"appId": *"[^"]' "$DATA_DIR/config.yaml" "$DATA_DIR/config.json" 2>/dev/null; then
	say "下一步：扫码绑定。扫码成功后会立刻连上 QQ。密码等其它配置可以随后再设，重启后生效。"
	show_cmd "$(bin_path) config"
else
	say "已绑定过机器人。"
	if [ "$JUST_STARTED" != 1 ] && [ "$SHOWED_START" != 1 ] && ! service_running; then
		print_start_cmd
	fi
fi
if [ ! -s "$DATA_DIR/web-auth.json" ]; then
	echo
	say "管理端密码可以在连上之后再设，不用为密码重启："
	show_cmd "$(bin_path) passwd"
fi
