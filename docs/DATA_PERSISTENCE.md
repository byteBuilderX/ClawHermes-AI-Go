# 数据持久化指南

## 概述

ClawHermes AI Go 的所有依赖服务都运行在 Docker 容器中，通过 Docker Volumes 实现数据持久化。

## 持久化方案

### 1. NATS (事件总线)

**持久化方式**: JetStream 持久化

```yaml
volumes:
  - nats_data:/data
command: -js -sd /data
```

**特点**:
- 启用 JetStream 模式 (`-js`)
- 指定存储目录 (`-sd /data`)
- 事件消息持久化到磁盘
- 容器重启后消息不丢失

**验证**:
```bash
# 查看 NATS 数据
docker exec clawhermes-ai-go-nats-1 ls -la /data

# 查看 JetStream 状态
docker exec clawhermes-ai-go-nats-1 nats stream list
```

### 2. Neo4j (图数据库)

**持久化方式**: 数据卷挂载

```yaml
volumes:
  - neo4j_data:/data
  - neo4j_logs:/logs
```

**特点**:
- `/data` 存储数据库文件
- `/logs` 存储日志文件
- 支持事务日志恢复
- 容器删除后数据保留

**验证**:
```bash
# 查看 Neo4j 数据
docker exec clawhermes-ai-go-neo4j-1 ls -la /data

# 查看数据库大小
docker exec clawhermes-ai-go-neo4j-1 du -sh /data
```

### 3. Milvus (向量数据库)

**持久化方式**: etcd + MinIO

```yaml
milvus:
  volumes:
    - milvus_data:/var/lib/milvus
  depends_on:
    - etcd
    - minio

etcd:
  volumes:
    - etcd_data:/etcd

minio:
  volumes:
    - minio_data:/minio_data
```

**特点**:
- **etcd**: 存储元数据（集合定义、索引信息）
- **MinIO**: 存储向量数据（S3 兼容对象存储）
- **milvus_data**: 本地缓存

**验证**:
```bash
# 查看 etcd 数据
docker exec clawhermes-ai-go-etcd-1 etcdctl get --prefix ""

# 查看 MinIO 数据
docker exec clawhermes-ai-go-minio-1 ls -la /minio_data

# 查看 Milvus 集合
docker exec clawhermes-ai-go-milvus-1 milvus-cli
```

### 4. OpenTelemetry Collector

**持久化方式**: 配置文件挂载

```yaml
volumes:
  - ./otel-collector-config.yaml:/etc/otel-collector-config.yaml
```

**特点**:
- 配置文件持久化
- 日志导出到文件系统
- 支持多种导出器

## 数据卷管理

### 查看所有数据卷

```bash
docker volume ls | grep clawhermes
```

### 查看数据卷详情

```bash
docker volume inspect clawhermes-ai-go_neo4j_data
```

### 备份数据

```bash
# 备份 Neo4j 数据
docker run --rm -v clawhermes-ai-go_neo4j_data:/data -v $(pwd):/backup \
  alpine tar czf /backup/neo4j_backup.tar.gz -C /data .

# 备份 Milvus 数据
docker run --rm -v clawhermes-ai-go_minio_data:/data -v $(pwd):/backup \
  alpine tar czf /backup/minio_backup.tar.gz -C /data .

# 备份 etcd 数据
docker run --rm -v clawhermes-ai-go_etcd_data:/data -v $(pwd):/backup \
  alpine tar czf /backup/etcd_backup.tar.gz -C /data .
```

### 恢复数据

```bash
# 恢复 Neo4j 数据
docker run --rm -v clawhermes-ai-go_neo4j_data:/data -v $(pwd):/backup \
  alpine tar xzf /backup/neo4j_backup.tar.gz -C /data

# 恢复 Milvus 数据
docker run --rm -v clawhermes-ai-go_minio_data:/data -v $(pwd):/backup \
  alpine tar xzf /backup/minio_backup.tar.gz -C /data

# 恢复 etcd 数据
docker run --rm -v clawhermes-ai-go_etcd_data:/data -v $(pwd):/backup \
  alpine tar xzf /backup/etcd_backup.tar.gz -C /data
```

### 清理数据

```bash
# 删除所有数据卷（谨慎操作！）
docker-compose down -v

# 删除特定数据卷
docker volume rm clawhermes-ai-go_neo4j_data
```

## 数据卷位置

### Linux/WSL

```bash
# 查看 Docker 数据卷根目录
docker info | grep "Docker Root Dir"

# 通常位置
/var/lib/docker/volumes/clawhermes-ai-go_*/_data
```

### macOS

```bash
# Docker Desktop 使用虚拟机
# 数据卷位置在虚拟机内
docker run --rm -it -v /var/lib/docker:/docker alpine ls -la /docker/volumes
```

### Windows (WSL2)

```bash
# 在 WSL2 中查看
wsl -d docker-desktop
ls -la /var/lib/docker/volumes
```

## 持久化验证

### 启动服务

```bash
./start.sh
```

### 创建测试数据

```bash
# 创建 Neo4j 节点
curl -X POST http://localhost:8080/skills \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Test Skill",
    "description": "Test persistence",
    "type": "code"
  }'
```

### 停止容器

```bash
./stop.sh
```

### 重启服务

```bash
./start.sh
```

### 验证数据

```bash
# 数据应该仍然存在
curl http://localhost:8080/health
```

## 性能优化

### 1. 调整 Neo4j 内存

```yaml
environment:
  NEO4J_dbms_memory_heap_max__size: 4G  # 根据系统调整
```

### 2. 调整 etcd 自动压缩

```yaml
environment:
  ETCD_AUTO_COMPACTION_MODE: revision
  ETCD_AUTO_COMPACTION_RETENTION: 1000
  ETCD_QUOTA_BACKEND_BYTES: 4294967296  # 4GB
```

### 3. MinIO 性能调优

```yaml
environment:
  MINIO_BROWSER: on
  MINIO_STORAGE_CLASS_STANDARD: EC:2
```

## 故障恢复

### NATS 数据损坏

```bash
# 清理 NATS 数据并重启
docker-compose down
docker volume rm clawhermes-ai-go_nats_data
docker-compose up -d nats
```

### Neo4j 无法启动

```bash
# 检查日志
docker logs clawhermes-ai-go-neo4j-1

# 清理并重启
docker-compose down
docker volume rm clawhermes-ai-go_neo4j_data
docker-compose up -d neo4j
```

### Milvus 连接失败

```bash
# 检查 etcd 和 MinIO
docker-compose logs etcd
docker-compose logs minio

# 重启依赖
docker-compose restart etcd minio
docker-compose restart milvus
```

## 监控数据卷使用

```bash
# 查看所有数据卷大小
docker system df -v

# 查看特定卷大小
docker run --rm -v clawhermes-ai-go_neo4j_data:/data alpine du -sh /data
docker run --rm -v clawhermes-ai-go_minio_data:/data alpine du -sh /data
docker run --rm -v clawhermes-ai-go_etcd_data:/data alpine du -sh /data
```

## 生产环境建议

1. **定期备份**: 使用 cron 定期备份数据卷
2. **监控磁盘**: 监控数据卷磁盘使用情况
3. **日志轮转**: 配置 Neo4j 日志轮转
4. **etcd 压缩**: 定期执行 etcd 自动压缩
5. **MinIO 清理**: 定期清理过期对象
6. **容器更新**: 更新镜像时保留数据卷

## 相关文档

- [Docker Volumes 官方文档](https://docs.docker.com/storage/volumes/)
- [NATS JetStream 文档](https://docs.nats.io/nats-concepts/jetstream)
- [Neo4j 数据持久化](https://neo4j.com/docs/operations-manual/current/backup-restore/)
- [Milvus 存储配置](https://milvus.io/docs/deploy_s3.md)
- [etcd 备份恢复](https://etcd.io/docs/v3.5/op-guide/recovery/)
