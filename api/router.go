package api

import (
	"clawhermes-ai-go/api/handler"
	"clawhermes-ai-go/api/middleware"
	"clawhermes-ai-go/internal/config"
	"clawhermes-ai-go/internal/llmgateway"
	"clawhermes-ai-go/internal/orchestrator"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func NewRouter(cfg *config.Config, registry *orchestrator.Registry, logger *zap.Logger, gateway *llmgateway.Gateway) *gin.Engine {
	router := gin.Default()

	router.Use(middleware.CORSMiddleware())
	router.Use(middleware.ErrorHandler(logger))

	skillHandler := handler.NewSkillHandler(registry, logger, gateway)

	skills := router.Group("/skills")
	{
		skills.POST("", skillHandler.CreateSkill)
		skills.GET("/:id", skillHandler.GetSkill)
		skills.POST("/:id/execute", skillHandler.ExecuteSkill)
	}

	router.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	return router
}
