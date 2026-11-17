#!/usr/bin/env bash
# 一键安装 QQBot Bridge：编译、装到 ~/.local/bin，可选装成 systemd 用户服务。
#
#   ./scripts/install.sh                    # 只装二进制
#   ./scripts/install.sh --service          # 顺便安装并启动 systemd 服务
#   PREFIX=/usr/local/bin ./scripts/install.sh
#
# 幂等：重复执行等于升级，正在运行的服务会被重启。
set -euo pipefail

PREFIX="${PREFIX:-$HOME/.local/bin}"
WITH_SERVICE=0
INSTANCE_NAME=""
REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

usage() {
	cat <<'EOF'
用法：install.sh [--service [--name 实例名]] [--prefix DIR]

  --service         安装 systemd 用户服务并启动（仅 Linux）
  --name 实例名     安装成模板实例 qqbot_bridge@<实例名>.service，
                    数据目录为 ~/.qqbot_bridge-<实例名>，需配合 --service
  --prefix DIR      安装目录，默认 ~/.local/bin
  -h, --help        显示本帮助

同一台机器跑多套（每套一个 QQ 机器人，appid 不能共用）：
  ./scripts/install.sh --service                      # 默认实例
  ./scripts/install.sh --service --name cursor        # 第二套，换 driver 和端口
EOF
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

# 实例名会变成 unit 名和目录名，只允许 systemd 认得的字符。
# 这段在 die 定义之前执行，所以直接用 printf + exit。
if [ -n "$INSTANCE_NAME" ]; then
	case "$INSTANCE_NAME" in
	*[!A-Za-z0-9_.-]*)
		printf '\033[31m[x]\033[0m %s\n' "实例名只能用字母、数字、_ . -：$INSTANCE_NAME" >&2
		exit 1
		;;
	esac
	if [ "$WITH_SERVICE" != 1 ]; then
		printf '\033[31m[x]\033[0m %s\n' "实例名只在装服务时有意义：install.sh --service --name $INSTANCE_NAME" >&2
		exit 1
	fi
fi

say() { printf '\033[36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[33m[!]\033[0m %s\n' "$*" >&2; }
die() {
	printf '\033[31m[x]\033[0m %s\n' "$*" >&2
	exit 1
}

# ---- 环境检查 -------------------------------------------------------------
# 最低版本以 go.mod 里的 go 指令为准，不在这里写死第二份
command -v go >/dev/null 2>&1 || die "没找到 go，先装 Go：https://go.dev/dl/"
GO_VERSION="$(go env GOVERSION | sed 's/^go//')"
GO_NEED="$(awk '$1=="go"{print $2; exit}' "$REPO_DIR/go.mod")"
if [ -n "$GO_NEED" ] && [ "$(printf '%s\n%s\n' "$GO_NEED" "${GO_VERSION%%[-+ ]*}" | sort -V | head -n1)" != "$GO_NEED" ]; then
	die "Go 版本太旧（$GO_VERSION），本项目需要 $GO_NEED 或更高：https://go.dev/dl/"
fi

if command -v codebuddy >/dev/null 2>&1; then
	say "codebuddy：$(command -v codebuddy)"
else
	warn "没找到 codebuddy，服务能起来但跑不了任务。装好并登录后再启动。"
fi

if [ ! -f "$REPO_DIR/go.mod" ]; then
	die "没找到源码（$REPO_DIR/go.mod），请在仓库目录里运行本脚本"
fi

# ---- 编译 ---------------------------------------------------------------
say "编译中（$REPO_DIR）"
BUILD_DIR="$(mktemp -d)"
trap 'rm -rf "$BUILD_DIR"' EXIT
(
	cd "$REPO_DIR"
	CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=$(git describe --tags --always --dirty 2>/dev/null || echo dev) -X main.buildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)" -o "$BUILD_DIR/qqbot_bridge" .
)

# ---- 安装 ---------------------------------------------------------------
mkdir -p "$PREFIX"
# 先写临时文件再 mv，正在运行的旧二进制不会被覆盖失败（Text file busy）
install -m755 "$BUILD_DIR/qqbot_bridge" "$PREFIX/qqbot_bridge.new"
mv -f "$PREFIX/qqbot_bridge.new" "$PREFIX/qqbot_bridge"
say "已安装：$PREFIX/qqbot_bridge"

case ":$PATH:" in
*":$PREFIX:"*) ;;
*) warn "$PREFIX 不在 PATH 里，把 export PATH=\"$PREFIX:\$PATH\" 加进 shell 配置" ;;
esac

# ---- 可选：systemd 用户服务 ---------------------------------------------
if [ "$WITH_SERVICE" = 1 ]; then
	command -v systemctl >/dev/null 2>&1 || die "没有 systemctl，去掉 --service 手动启动"
	UNIT_DIR="$HOME/.config/systemd/user"
	mkdir -p "$UNIT_DIR"
	install -m644 "$REPO_DIR/deploy/qqbot_bridge.service" "$UNIT_DIR/qqbot_bridge.service"
	# 模板 unit 一起装上，以后加实例不用重跑脚本
	install -m644 "$REPO_DIR/deploy/qqbot_bridge@.service" "$UNIT_DIR/qqbot_bridge@.service"
	# 已经是自定义路径的话，把 ExecStart 改成实际安装位置
	if [ "$PREFIX" != "$HOME/.local/bin" ]; then
		# 服务 unit 里写的是 $HOME，模板 unit 里写的是 systemd 的 %h，两种都要换
		for unit in qqbot_bridge.service qqbot_bridge@.service; do
			sed -i.bak \
				-e "s#$HOME/.local/bin/qqbot_bridge#$PREFIX/qqbot_bridge#g" \
				-e "s#%h/.local/bin/qqbot_bridge#$PREFIX/qqbot_bridge#g" \
				"$UNIT_DIR/$unit"
			rm -f "$UNIT_DIR/$unit.bak"
		done
	fi
	systemctl --user daemon-reload

	if [ -n "$INSTANCE_NAME" ]; then
		DATA_DIR="$HOME/.qqbot_bridge-$INSTANCE_NAME"
		systemctl --user enable --now "qqbot_bridge@$INSTANCE_NAME" >/dev/null 2>&1 ||
			warn "启动 qqbot_bridge@$INSTANCE_NAME 失败，看：systemctl --user status qqbot_bridge@$INSTANCE_NAME"
		say "实例已安装：qqbot_bridge@$INSTANCE_NAME（数据目录 $DATA_DIR）"
		echo "  1) 绑定这个实例自己的机器人：$PREFIX/qqbot_bridge -home $DATA_DIR config -addr 127.0.0.1:8687"
		echo "  2) 让新配置生效：systemctl --user restart qqbot_bridge@$INSTANCE_NAME"
		echo "  状态：systemctl --user status qqbot_bridge@$INSTANCE_NAME"
		echo "  日志：journalctl --user -u qqbot_bridge@$INSTANCE_NAME -f"
		warn "同一个 appid 只能给一个实例用；第二套请扫另一个机器人，并把 -addr 换成没被占用的端口。"
	else
		systemctl --user enable qqbot_bridge >/dev/null 2>&1 || true
		systemctl --user restart qqbot_bridge
		say "服务已启动：systemctl --user status qqbot_bridge"
		echo "  想再加一套（另一个机器人 + 另一个端口）：$0 --service --name cursor"
	fi
else
	# 只重启「服务用的就是这次装的二进制」那一份，避免装到别的目录却把线上服务踢掉
	if [ "$PREFIX" = "$HOME/.local/bin" ] && systemctl --user is-active --quiet qqbot_bridge 2>/dev/null; then
		systemctl --user restart qqbot_bridge
		say "已重启 systemd 服务，新版本生效"
	elif pgrep -f "$PREFIX/qqbot_bridge -f" >/dev/null 2>&1; then
		say "检测到自建后台实例，重启使它生效"
		"$PREFIX/qqbot_bridge" -stop >/dev/null 2>&1 || true
		"$PREFIX/qqbot_bridge" >/dev/null
	else
		say "已安装；有服务在跑的话记得重启：systemctl --user restart qqbot_bridge"
	fi
fi

# ---- 收尾 ---------------------------------------------------------------
# 新版配置是 config.yaml（appId 可能带引号），旧版是 config.json，两种都认
if ! grep -Eq '^ *appId: *"?[^" ]|"appId": *"[^"]' "$HOME/.qqbot_bridge/config.yaml" "$HOME/.qqbot_bridge/config.json" 2>/dev/null; then
	echo
	say "下一步：扫码绑定机器人"
	echo "    $PREFIX/qqbot_bridge config"
else
	echo
	say "已绑定过机器人，直接发消息试试；要改配置运行 qqbot_bridge config。"
fi
if [ ! -s "$HOME/.qqbot_bridge/web-auth.json" ]; then
	echo
	say "Web 管理端需要密码才能访问，请先设置："
	echo "    $PREFIX/qqbot_bridge passwd"
fi
