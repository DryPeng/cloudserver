package bootstrap

import (
	"context"

	"github.com/cloudreve/Cloudreve/v3/models/scripts/invoker"
	"github.com/cloudreve/Cloudreve/v3/pkg/logger"
)

func RunScript(name string) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := invoker.RunDBScript(name, ctx); err != nil {
		logger.Error("数据库脚本执行失败: %s", err)
		return
	}

	logger.Info("数据库脚本 [%s] 执行完毕", name)
}
