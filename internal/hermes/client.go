package hermes

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/nats-io/nats.go"
	"go.uber.org/zap"
)

type Event struct {
	Type      string      `json:"type"`
	Timestamp int64       `json:"timestamp"`
	Data      interface{} `json:"data"`
	Source    string      `json:"source"`
}

type EventHandler func(event *Event) error

type Client struct {
	conn     *nats.Conn
	handlers map[string][]EventHandler
	mu       sync.RWMutex
	logger   *zap.Logger
}

func NewClient(url string, logger *zap.Logger) (*Client, error) {
	conn, err := nats.Connect(url)
	if err != nil {
		return nil, err
	}
	return &Client{
		conn:     conn,
		handlers: make(map[string][]EventHandler),
		logger:   logger,
	}, nil
}

func (c *Client) Publish(event *Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		c.logger.Error("failed to marshal event", zap.Error(err))
		return err
	}

	subject := fmt.Sprintf("events.%s", event.Type)
	if err := c.conn.Publish(subject, data); err != nil {
		c.logger.Error("failed to publish event", zap.String("type", event.Type), zap.Error(err))
		return err
	}

	c.logger.Debug("event published", zap.String("type", event.Type), zap.String("source", event.Source))
	return nil
}

func (c *Client) Subscribe(eventType string, handler EventHandler) error {
	c.mu.Lock()
	c.handlers[eventType] = append(c.handlers[eventType], handler)
	c.mu.Unlock()

	subject := fmt.Sprintf("events.%s", eventType)
	_, err := c.conn.Subscribe(subject, func(msg *nats.Msg) {
		var event Event
		if err := json.Unmarshal(msg.Data, &event); err != nil {
			c.logger.Error("failed to unmarshal event", zap.Error(err))
			return
		}

		c.mu.RLock()
		handlers := c.handlers[eventType]
		c.mu.RUnlock()

		for _, h := range handlers {
			if err := h(&event); err != nil {
				c.logger.Error("event handler error", zap.String("type", eventType), zap.Error(err))
			}
		}
	})

	if err != nil {
		c.logger.Error("failed to subscribe", zap.String("type", eventType), zap.Error(err))
		return err
	}

	c.logger.Info("subscribed to event", zap.String("type", eventType))
	return nil
}

func (c *Client) Close() {
	c.conn.Close()
	c.logger.Info("hermes client closed")
}
