package cli

import (
	// 注册可选的 agent 驱动：服务启动时按配置里的 agent.driver 取用。
	// 放在这里是为了让二进制只依赖一处注册点，避免「编译通过但运行时报未知驱动」。
	_ "qqbot_bridge/internal/agent/cursor"
)
