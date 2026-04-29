package main

import (
	"context"
	"log"

	"github.com/byteBuilderX/ClawHermes-AI-Go/api"
	"github.com/byteBuilderX/ClawHermes-AI-Go/internal/config"
	"github.com/byteBuilderX/ClawHermes-AI-Go/internal/hermes"
	"github.com/byteBuilderX/ClawHermes-AI-Go/internal/llmgateway"
	"github.com/byteBuilderX/ClawHermes-AI-Go/internal/orchestrator"
	"go.uber.org/zap"
)

func main() {
	logger, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("Failed to create logger: %v", err)
	}
	defer logger.Sync()

	// 加载配置
	cfg, err := config.Load()
	if err != nil {
		logger.Fatal("Failed to load config", zap.Error(err))
	}

	// 初始化底层服务
	services, err := config.InitializeServices(cfg, logger)
	if err != nil {
		logger.Fatal("Failed to initialize services", zap.Error(err))
	}
	defer services.Close()

	// 初始化 Hermes 事件总线
	hermesClient, err := hermes.NewClient(cfg.NatsURL, logger)
	if err != nil {
		logger.Warn("Failed to connect to NATS", zap.Error(err))
		// 不中断启动，继续运行
	} else {
		defer hermesClient.Close()
		logger.Info("Connected to NATS", zap.String("url", cfg.NatsURL))
	}

	// 初始化 LLM Gateway
	llmCfg := llmgateway.LoadConfig()
	gateway := llmgateway.InitializeGateway(llmCfg, logger)

	// 检查 LLM 服务健康状态
	if err := gateway.Health(context.Background()); err != nil {
		logger.Warn("LLM gateway health check failed", zap.Error(err))
	}

	// 初始化 Skill Registry
	registry := orchestrator.NewRegistry()

	// 创建路由
	router := api.NewRouter(cfg, registry, logger, gateway)

	logger.Info("Starting server", zap.String("port", cfg.Port))
	if err := router.Run(":" + cfg.Port); err != nil {
		logger.Fatal("Failed to start server", zap.Error(err))
	}
}
