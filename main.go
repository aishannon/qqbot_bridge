// Command qqbot_bridge 把手机 QQ 的单聊消息接到本机 CodeBuddy：
// 扫码绑定机器人后，QQ 里发的指令会在指定工作目录下执行，
// 执行过程、结果和新生成的截图自动发回 QQ。
//
//	qqbot_bridge config      # 服务初始化：扫码绑定、设置管理端密码
//	qqbot_bridge bind        # 只做扫码绑定（config 的旧名字，保留兼容）
//	qqbot_bridge passwd      # 设置 Web 管理端的访问密码（没设置前管理端拒绝访问）
//	qqbot_bridge             # 后台启动网关 + 本地控制台
//	qqbot_bridge -f          # 前台运行
//	qqbot_bridge -stop       # 停止后台实例
//	qqbot_bridge -addr :8686 # 指定控制台端口
package main

import (
	"os"

	"qqbot_bridge/internal/cli"
)

// 以下两个变量由构建时 -ldflags "-X main.version=... -X main.buildTime=..." 注入。
// 链接期只能改 main 包里的变量，所以真正取值留在这里，再转发给 internal/cli。
var (
	version   = "dev"
	buildTime = ""
)

func main() {
	cli.Version = version
	cli.BuildTime = buildTime
	os.Exit(cli.Main(os.Args[1:]))
}
