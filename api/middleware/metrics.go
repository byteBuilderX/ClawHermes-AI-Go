package middleware

import (
	"time"

	"github.com/byteBuilderX/ClawHermes-AI-Go/pkg/observability"
	"github.com/gin-gonic/gin"
)

func MetricsMiddleware(metrics *observability.Metrics) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		c.Next()

		duration := time.Since(start).Seconds() * 1000
		metrics.RecordAPIRequest(c.Request.Method, c.Request.URL.Path, c.Writer.Status(), duration)
	}
}
