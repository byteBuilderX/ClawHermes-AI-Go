package observability

import (
	"context"

	"go.uber.org/zap"
)

type Logger struct {
	*zap.Logger
}

func NewLogger(env string) (*Logger, error) {
	var logger *zap.Logger
	var err error

	if env == "production" {
		logger, err = zap.NewProduction()
	} else {
		logger, err = zap.NewDevelopment()
	}

	if err != nil {
		return nil, err
	}

	return &Logger{logger}, nil
}

type Tracer struct {
	logger *Logger
}

func NewTracer(logger *Logger) *Tracer {
	return &Tracer{logger: logger}
}

func (t *Tracer) StartSpan(ctx context.Context, name string) (context.Context, func()) {
	t.logger.Info("span started", zap.String("name", name))

	return ctx, func() {
		t.logger.Info("span ended", zap.String("name", name))
	}
}

type Metrics struct {
	logger *Logger
}

func NewMetrics(logger *Logger) *Metrics {
	return &Metrics{logger: logger}
}

func (m *Metrics) RecordSkillExecution(skillID string, duration float64, success bool) {
	status := "success"
	if !success {
		status = "failed"
	}
	m.logger.Info("skill execution recorded",
		zap.String("skill_id", skillID),
		zap.Float64("duration_ms", duration),
		zap.String("status", status),
	)
}

func (m *Metrics) RecordAPIRequest(method, path string, statusCode int, duration float64) {
	m.logger.Info("api request recorded",
		zap.String("method", method),
		zap.String("path", path),
		zap.Int("status_code", statusCode),
		zap.Float64("duration_ms", duration),
	)
}

func (m *Metrics) RecordEventPublished(eventType string) {
	m.logger.Debug("event published recorded", zap.String("event_type", eventType))
}
