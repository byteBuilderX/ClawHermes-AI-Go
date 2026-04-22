#!/bin/bash

# ClawHermes AI Go - 完整启动脚本

set -e

echo "🚀 ClawHermes AI Go - 启动脚本"
echo "================================"

# 检查 Docker
if ! command -v docker &> /dev/null; then
    echo "❌ Docker 未安装，请先安装 Docker"
    exit 1
fi

if ! command -v docker-compose &> /dev/null; then
    echo "❌ Docker Compose 未安装，请先安装 Docker Compose"
    exit 1
fi

# 检查 Go
if ! command -v go &> /dev/null; then
    echo "❌ Go 未安装，请先安装 Go 1.22+"
    exit 1
fi

echo "✓ 依赖检查通过"
echo ""

# 1. 启动 Docker 容器
echo "📦 启动底层服务..."
docker-compose up -d

echo "⏳ 等待服务启动..."
sleep 5

# 检查 NATS
echo -n "检查 NATS... "
if nc -z localhost 4222 2>/dev/null; then
    echo "✓"
else
    echo "⚠ (可能还在启动中)"
fi

# 检查 Milvus
echo -n "检查 Milvus... "
if nc -z localhost 19530 2>/dev/null; then
    echo "✓"
else
    echo "⚠ (可能还在启动中)"
fi

# 检查 Neo4j
echo -n "检查 Neo4j... "
if nc -z localhost 7687 2>/dev/null; then
    echo "✓"
else
    echo "⚠ (可能还在启动中)"
fi

echo ""

# 2. 构建应用
echo "🔨 构建应用..."
go build -o bin/server ./cmd/server
echo "✓ 构建完成"
echo ""

# 3. 启动应用
echo "🎯 启动应用服务..."
echo "服务地址: http://localhost:8080"
echo "健康检查: http://localhost:8080/health"
echo ""
echo "按 Ctrl+C 停止服务"
echo ""

./bin/server
