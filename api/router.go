package api

import (
	"github.com/byteBuilderX/ClawHermes-AI-Go/api/handler"
	"github.com/byteBuilderX/ClawHermes-AI-Go/api/middleware"
	"github.com/byteBuilderX/ClawHermes-AI-Go/internal/config"
	"github.com/byteBuilderX/ClawHermes-AI-Go/internal/llmgateway"
	"github.com/byteBuilderX/ClawHermes-AI-Go/internal/orchestrator"
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
