#!/bin/bash

# ClawHermes AI Go - 停止脚本

echo "🛑 停止 ClawHermes AI Go"
echo "========================"

# 停止 Docker 容器
echo "停止 Docker 容器..."
docker-compose down

echo "✓ 已停止所有服务"
