# Stratum Demo 部署架构说明

本文用教学方式解释一次浏览器请求如何穿过公网、Traefik Ingress、前端 Nginx、Go 后端和集群内依赖服务。
内容按仓库现状核对（`helm/`、`.github/workflows/deploy.yml`、`monitoring/remote/`、`k8s/`），
反映 2026-08 的远程 HTTPS demo 部署形态；可复现的部署契约以 `helm/` values 与
`.github/workflows/deploy.yml` 为准。

当前远程 HTTPS profile 的公网入口是 <https://101.200.181.141:8443>，健康检查是
<https://101.200.181.141:8443/api/health>。该地址不是硬编码，而是来自 GitHub Actions
Production 环境变量 `PUBLIC_BASE_URL`；`scripts/quality/validate-remote-http-base-url.sh`
强制其形如 `https://<public-ip>:8443`，禁止域名、其他端口、路径或尾随 `/`。
TLS 由 cert-manager 签发的内部 CA 证书 `stratum-ingress-tls` 提供（根 CA 不公开受信），
因此 curl 访问需要 `--insecure`，浏览器会提示证书不受信任。

最后核对时间：2026-08-25（doc-cleanup，按 `helm/` 与 `.github/workflows/deploy.yml` 逐项核对）。

历史上（2026-07-07 起）该 profile 曾以明文 HTTP 运行：公网入口为 `http://101.200.181.141:6879`，
由 K3s ServiceLB 暴露给 Traefik `web2` entrypoint。2026-08-01（commit `99e12605`）已切换到
HTTPS:8443，`web2`/`6879` 不再作为公网入口。

## 一句话架构

Stratum demo 部署在一台阿里云 ECS 上，ECS 里运行单节点 K3s。公网 HTTPS 流量先进 K3s 内置
Traefik（`websecure` entrypoint，端口 8443），TLS 终止使用 cert-manager 内部 CA 签发的默认
证书；再进入前端 Nginx。前端 Nginx 同时负责两件事：返回 React 静态页面，以及把 `/api/*` 请求
反向代理到 Go 后端。

业务应用由 `stratum` Helm release 管理，位于 `stratum` namespace。监控栈由独立的 `kps` release
管理，位于 `monitoring` namespace；它不是本仓库 `grafana/` 目录的 docker-compose 配置。
另有三个 chart/manifest 之外的组件：cert-manager（`cert-manager` namespace）、Opik 官方 chart
（`opik` namespace）、以及由 deploy.yml 直接应用的原生 manifest（Loki/Promtail/Jaeger、OTLP
collector、ServiceMonitor/PrometheusRule）。

```mermaid
flowchart TB
    browser["用户浏览器<br/>https://101.200.181.141:8443"]

    subgraph ecs["阿里云 ECS<br/>公网 IP 101.200.181.141"]
        subgraph k3s["K3s 单节点集群"]
            traefik["Traefik Ingress<br/>entrypoints: websecure :8443<br/>router.tls=true<br/>TLSStore 默认证书"]

            subgraph cmns["namespace: cert-manager"]
                ca["ClusterIssuer stratum-internal-ca"]
                cert["Certificate stratum-ingress-tls<br/>IP 101.200.181.141"]
            end

            subgraph ns["namespace: stratum"]
                frontend["stratum-frontend<br/>Nginx + React 静态资源<br/>Service: ClusterIP :80"]
                backend["stratum<br/>Go API / Gin<br/>Service: ClusterIP :80<br/>Container: :8080"]
                postgres["stratum-postgresql<br/>PostgreSQL 16 + zhparser"]
                redis["stratum-redis<br/>Redis 7"]
                nats["stratum-nats<br/>NATS JetStream"]
                milvus["stratum-milvus<br/>Milvus standalone"]
                minio["stratum-minio<br/>MinIO"]
                etcd["stratum-etcd<br/>Milvus metadata"]
                nacos["stratum-nacos<br/>Nacos 配置中心"]
                nacosmysql["stratum-nacos-mysql<br/>MySQL 8.4"]
                collector["stratum-otel-collector Service<br/>opik-otel-collector Deployment"]
                secrets["stratum-secrets<br/>DB/JWT/GitHub OAuth/Nacos"]
                config["stratum-config<br/>URL/CORS/Service config"]
            end

            subgraph opikns["namespace: opik"]
                opik["Opik 2.1.32<br/>backend + python-backend + frontend<br/>MySQL/Redis/ZooKeeper/ClickHouse"]
            end

            subgraph mon["namespace: monitoring"]
                grafana["kps-grafana<br/>Grafana"]
                prometheus["kps-...-prometheus<br/>Prometheus"]
                alertmanager["kps-...-alertmanager<br/>Alertmanager"]
                blackbox["stratum-blackbox<br/>Blackbox exporter"]
                jaeger["jaeger<br/>UI :16686<br/>OTLP :4317"]
                loki["loki + promtail"]
                feishu["stratum-feishu-alert-adapter"]
            end
        end
    end

    browser -->|"HTTPS :8443"| traefik
    traefik -->|"Ingress path / (hostless)"| frontend
    frontend -->|"React 静态资源"| browser
    frontend -->|"/api/* 反代并去掉 /api 前缀"| backend

    cert --> traefik
    ca --> cert

    config --> backend
    secrets --> backend
    backend --> postgres
    backend --> redis
    backend --> nats
    backend --> milvus
    backend --> nacos
    nacos --> nacosmysql
    milvus --> minio
    milvus --> etcd

    backend -->|"OTLP :4317"| collector
    collector -->|"otlphttp/opik"| opik
    collector -->|"otlp/jaeger"| jaeger

    prometheus -->|"scrape /metrics"| backend
    prometheus -->|"scrape"| collector
    prometheus --> alertmanager
    alertmanager -->|"webhook"| feishu
    grafana --> prometheus
    grafana --> loki
    loki --> promtail

    classDef edge fill:#fff7ed,stroke:#c05621,stroke-width:1px,color:#1c1917;
    classDef app fill:#f4f8f1,stroke:#758467,stroke-width:1px,color:#1c1917;
    classDef data fill:#f7f2f8,stroke:#7f5f88,stroke-width:1px,color:#1c1917;
    classDef cfg fill:#eef6ff,stroke:#4078a8,stroke-width:1px,color:#1c1917;
    classDef obs fill:#f1f5f9,stroke:#475569,stroke-width:1px,color:#1c1917;
    class browser,traefik edge;
    class frontend,backend app;
    class postgres,redis,nats,milvus,minio,etcd,nacos,nacosmysql,opik data;
    class secrets,config,collector cfg;
    class grafana,prometheus,alertmanager,blackbox,jaeger,loki,promtail,feishu obs;
```

## HTTPS 请求链路

访问首页时，请求链路是：

```text
浏览器
  -> https://101.200.181.141:8443/
  -> Traefik websecure :8443（TLS 终止，TLSStore 默认证书 stratum-ingress-tls）
  -> Ingress hostless rule（entrypoint websecure, router.tls=true）
  -> stratum-frontend Service :80
  -> Nginx 返回 React 静态资源
```

访问 API 时，请求链路是：

```text
浏览器
  -> https://101.200.181.141:8443/api/health
  -> Traefik websecure :8443
  -> stratum-frontend Nginx
  -> proxy_pass http://stratum:80/
  -> Go 后端 /health
```

这里最容易误解的是 `/api` 前缀。Go 后端真实路由不是 `/api/auth/me`，而是 `/auth/me`。`/api` 是公网侧为了区分前端页面和 API 加的一层前缀，由前端 Nginx 在转发时剥掉。

对应配置在 `helm/templates/frontend-configmap.yaml`：

```nginx
location /api/ {
    proxy_pass http://stratum:80/;
}
```

因为 `proxy_pass` 末尾有 `/`，所以路径会这样变化：

```text
/api/health                  -> /health
/api/auth/github             -> /auth/github
/api/auth/github/callback    -> /auth/github/callback
```

## 当前 Helm Demo 配置

业务应用由 `.github/workflows/deploy.yml` 叠加两层 values 执行部署：

```bash
helm upgrade --install stratum ./helm \
  -f helm/values-demo.yaml \
  -f helm/values-demo-remote-http.yaml \
  --set app.image.repository="$IMAGE_REPO/stratum-backend" \
  --set-string app.image.digest="$BACKEND_DIGEST" \
  --set-string config.frontendUrl="$PUBLIC_BASE_URL" \
  --set-string config.githubCallbackUrl="$PUBLIC_BASE_URL/api/auth/github/callback" \
  --set-string config.secureCookies="false" \
  ... （其余依赖镜像均按 digest 固定）\
  -n stratum
```

`helm/values-demo.yaml` 是 HTTPS demo 基线（域名占位 `demo.example.com`、TLS secret
`stratum-demo-tls`），`helm/values-demo-remote-http.yaml` 是无域名远程覆盖层（host 置空、
TLS secret 换成 `stratum-ingress-tls`）。合并后的核心配置如下：

```yaml
frontend:
  enabled: true
  backendServiceName: stratum
  backendServicePort: 80

config:
  frontendUrl: "https://<public-ip>:8443"          # 来自 PUBLIC_BASE_URL
  githubCallbackUrl: "https://<public-ip>:8443/api/auth/github/callback"
  secureCookies: "true"                            # values 文件是 true，但 CD --set 覆盖为 "false"
  natsUrl: "nats://stratum-nats:4222"
  milvusHost: "stratum-milvus"
  milvusPort: "19530"
  otelCollectorEndpoint: "http://stratum-otel-collector:4317"
  opikUrl: "http://opik-backend.opik.svc.cluster.local:8080"
  nacosUrl: "http://stratum-nacos:8848"
  nacosNamespace: "stratum-prod"

nacos:
  enabled: true

ingress:
  enabled: true
  className: "traefik"
  annotations:
    traefik.ingress.kubernetes.io/router.entrypoints: "websecure"
    traefik.ingress.kubernetes.io/router.tls: "true"
  hosts:
    - host: ""
      paths:
        - path: /
          pathType: Prefix
          service: frontend
  tls:
    - secretName: stratum-ingress-tls
```

这些值的含义：

- `frontend.enabled=true` 表示公网入口先落到前端 Nginx，而不是直接落到 Go 后端。
- `backendServiceName=stratum` 和 `backendServicePort=80` 是前端 Nginx `/api/` 反向代理的目标。
- `frontendUrl` 是后端登录成功后跳回前端的地址，由 CI 注入 `PUBLIC_BASE_URL`。
- `githubCallbackUrl` 是后端传给 GitHub 的 OAuth callback 地址。当前必须带 `/api`，因为公网只有 `/api/*` 会被前端 Nginx 代理到后端。
- `secureCookies`：`values` 文件都写成 `"true"`，但 deploy.yml 的 `--set-string config.secureCookies="false"` 会把它覆盖为 `"false"`（详见"当前已知限制"）。
- `host: ""` 表示 Ingress 不限制 Host，浏览器直接用 IP 访问也能命中规则；`tls.secretName: stratum-ingress-tls` 是 cert-manager 内部 CA 签发的证书。
- entrypoint 只保留 `websecure` 且 `router.tls=true`，即公网只接受 HTTPS。
- `nacos.enabled=true`，`stratum` release 会在集群内创建 `stratum-nacos`（Nacos v2.5.1）和 `stratum-nacos-mysql`（MySQL 8.4）。
- `observability.enabled=true`（`helm/values.yaml` 默认值）。`stratum-otel-collector` 由 deploy.yml
  从 `k8s/opik-otel-collector.yaml` 应用，不再由 chart 开关控制；它把 trace 转发到 Opik 和 Jaeger。

## 当前远端部署的实际资源

业务 namespace：

```text
namespace: stratum
release:   stratum
chart:     ./helm
values:    helm/values-demo.yaml + helm/values-demo-remote-http.yaml
           + CI --set image.repository/image.digest/frontendUrl/githubCallbackUrl/secureCookies
```

业务组件由 chart 直接创建：

```text
stratum-frontend       React 静态资源 + Nginx /api 反代
stratum                Go API 后端
stratum-postgresql     PostgreSQL 16 + zhparser（中文全文检索）
stratum-redis          Redis 7
stratum-nats           NATS JetStream
stratum-milvus         Milvus standalone
stratum-minio          Milvus object storage
stratum-etcd           Milvus metadata
stratum-nacos          Nacos 配置中心（standalone + MySQL 8.4）
stratum-nacos-mysql    Nacos 的 MySQL 8.4 存储
stratum-secrets        POSTGRES_PASSWORD / JWT_PRIVATE_KEY_PEM / GitHub OAuth / Nacos 凭据
aliyun-registry        镜像仓库 pull secret
```

chart 之外、由 deploy.yml（或基础设施 bootstrap）创建的资源：

```text
cert-manager           namespace cert-manager, chart v1.21.1（crds.enabled=true）
stratum-internal-ca    ClusterIssuer（内部 CA，签名自 self-signed 根）
stratum-ingress-tls    Certificate + Traefik TLSStore（k8s/stratum-tls.yaml，IP 101.200.181.141）
opik                   namespace opik, 官方 Opik chart 2.1.32（digest 固定）
stratum-otel-collector Service + opik-otel-collector Deployment（k8s/opik-otel-collector.yaml）
loki + promtail        k8s/logging.yaml（Loki 7 天保留，promtail 采集节点 /var/log/pods）
jaeger                 k8s/tracing.yaml（all-in-one, badger 持久化）
```

注意：`k8s/stratum-tls.yaml` 里的 Certificate 由 `stratum-internal-ca` ClusterIssuer 签发，
IP 固定为 `101.200.181.141`；它同时定义 Traefik TLSStore 的默认证书。Helm chart 的
pre-upgrade hook（`requiredSecrets`）会校验该 Secret 存在，首次部署前需先完成证书 bootstrap。

监控 namespace：

```text
namespace: monitoring
release:   kps              kube-prometheus-stack 87.10.1
release:   stratum-blackbox prometheus-blackbox-exporter 11.15.1
```

远端实际运行的监控组件：

```text
kps-grafana
kps-kube-prometheus-stack-operator
kps-kube-prometheus-stack-prometheus
kps-kube-state-metrics
kps-prometheus-node-exporter
stratum-blackbox
jaeger
loki
promtail
stratum-feishu-alert-adapter
```

注意：`kps` 监控栈不是 `.github/workflows/deploy.yml` 里的 `stratum` Helm 部署创建的。远端监控的唯一
GitOps 权威是 `monitoring/remote/`，固定版本在 `monitoring/remote/versions.env`，由
`scripts/deploy-remote-monitoring.sh` 对独立 Helm release 执行安全升级并应用自定义资源。

## 远端 Grafana 与监控栈

远端 Grafana 的配置来自 `monitoring` namespace 的 `kps` release，不来自仓库根目录 `grafana/`：

```text
Deployment: kps-grafana
Service:    kps-grafana
Secret:     kps-grafana
ConfigMap:  kps-grafana
ConfigMap:  kps-grafana-config-dashboards
ConfigMap:  kps-kube-prometheus-stack-grafana-datasource
ConfigMap:  kps-kube-prometheus-stack-*
```

`kps-grafana` 主配置在 `ConfigMap monitoring/kps-grafana`：

```ini
[server]
root_url = %(protocol)s://%(domain)s/grafana/
serve_from_sub_path = true

[auth.anonymous]
enabled = true
org_role = Viewer
```

Datasource 配置在 `ConfigMap monitoring/kps-kube-prometheus-stack-grafana-datasource`：

```yaml
datasources:
  - name: Prometheus
    type: prometheus
    uid: prometheus
    url: http://kps-kube-prometheus-stack-prometheus.monitoring:9090/
    isDefault: true
  - name: Alertmanager
    type: alertmanager
    uid: alertmanager
    url: http://kps-kube-prometheus-stack-alertmanager.monitoring:9093/
```

Dashboard provider 配置在 `ConfigMap monitoring/kps-grafana-config-dashboards`，Grafana
sidecar 会读取带 `grafana_dashboard=1` 标签的 ConfigMap：

```yaml
providers:
  - name: sidecarProvider
    type: file
    options:
      path: /tmp/dashboards
```

当前 `monitoring/remote/kube-prometheus-stack-values.yaml` 没有给 Grafana 配置公网 Ingress，
远端 Grafana 需通过 kubectl port-forward 或节点端口访问；此前的
`http://demo.stratum.example/grafana` 入口已不存在。

当前仓库根目录的 `grafana/` 只被本地 `docker-compose.yml` 挂载使用：

```yaml
- ./grafana/dashboards:/etc/grafana/provisioning/dashboards
- ./grafana/datasources:/etc/grafana/provisioning/datasources
```

所以：

- 本地 `make obs-up` / `docker-compose up grafana` 使用仓库 `grafana/`。
- 远端 Grafana 使用 `monitoring/kps-*` ConfigMap。
- 修改仓库 `grafana/datasources/*.yaml` 不会影响远端 Grafana。
- 要改远端 Grafana，应改 `kps` Helm values 或带 `grafana_datasource=1` /
  `grafana_dashboard=1` 标签的 Kubernetes ConfigMap。

## GitHub OAuth 登录链路

点击 GitHub 登录按钮时，链路是：

```text
浏览器
  -> GET https://101.200.181.141:8443/api/auth/github
  -> 前端 Nginx 转发到后端 /auth/github
  -> 后端生成 state cookie
  -> 302 跳转到 GitHub authorize URL
  -> GitHub 回调 https://101.200.181.141:8443/api/auth/github/callback
  -> 前端 Nginx 转发到后端 /auth/github/callback
  -> 后端换取 GitHub access token
  -> 后端签发 Stratum 登录 token
  -> 跳回 frontendUrl + /auth/callback
```

GitHub OAuth App 必须配置：

```text
Homepage URL:
https://101.200.181.141:8443/

Authorization callback URL:
https://101.200.181.141:8443/api/auth/github/callback
```

如果 GitHub 页面提示：

```text
The redirect_uri is not associated with this application.
```

说明 GitHub OAuth App 里登记的 callback URL 和后端传过去的 `redirect_uri` 不一致。修 GitHub OAuth App 配置即可，不需要重新部署后端。

## Secret 和配置注入

GitHub Actions 仓库 secrets 里使用这些名字：

```text
OAUTH_GITHUB_CLIENT_ID
OAUTH_GITHUB_CLIENT_SECRET
JWT_PRIVATE_KEY
POSTGRES_PASSWORD
MINIO_ROOT_PASSWORD
NACOS_PASSWORD
NACOS_MYSQL_PASSWORD
NACOS_AUTH_IDENTITY_KEY
NACOS_AUTH_IDENTITY_VALUE
NACOS_AUTH_TOKEN
FEISHU_WEBHOOK_URL
DOCKER_REGISTRY_URL
DOCKER_USERNAME
DOCKER_PASSWORD
SSH_DEPLOY_KEY
SSH_KNOWN_HOSTS
SSH_DEPLOY_HOST
KUBE_CONFIG
```

`PUBLIC_BASE_URL` 是 GitHub Actions Production **环境变量**（vars，不是 secret），由
`validate-remote-http-base-url.sh` 校验为 `https://<public-ip>:8443`。

注意：GitHub Actions 不允许 repository secret 名以 `GITHUB_` 开头。所以仓库 secret 叫 `OAUTH_GITHUB_CLIENT_ID`，但部署到 Kubernetes 后仍写成后端需要的环境变量名 `GITHUB_CLIENT_ID`。

deploy.yml 用这些 secret 生成两个 k8s Secret：

```yaml
env:
  GITHUB_CLIENT_ID: ${{ secrets.OAUTH_GITHUB_CLIENT_ID }}
  GITHUB_CLIENT_SECRET: ${{ secrets.OAUTH_GITHUB_CLIENT_SECRET }}
  MINIO_ROOT_PASSWORD: ${{ secrets.MINIO_ROOT_PASSWORD || secrets.POSTGRES_PASSWORD }}

# namespace stratum
kubectl create secret generic stratum-secrets \
  --from-literal=POSTGRES_PASSWORD=... \
  --from-literal=MINIO_ROOT_PASSWORD=... \
  --from-literal=JWT_PRIVATE_KEY_PEM=... \
  --from-literal=GITHUB_CLIENT_ID=... \
  --from-literal=GITHUB_CLIENT_SECRET=... \
  --from-literal=NACOS_PASSWORD=... \
  --from-literal=NACOS_MYSQL_PASSWORD=... \
  --from-literal=NACOS_AUTH_IDENTITY_KEY=... \
  --from-literal=NACOS_AUTH_IDENTITY_VALUE=... \
  --from-literal=NACOS_AUTH_TOKEN=...

# namespace monitoring
kubectl create secret generic stratum-monitoring-secrets \
  --from-literal=FEISHU_WEBHOOK_URL=...
```

后端 Pod 里最终看到的是：

```text
GITHUB_CLIENT_ID=SET
GITHUB_CLIENT_SECRET=SET
JWT_PRIVATE_KEY_PEM=SET
GITHUB_CALLBACK_URL=https://<public-ip>:8443/api/auth/github/callback
SECURE_COOKIES=false
NACOS_URL=http://stratum-nacos:8848
OPIK_URL=http://opik-backend.opik.svc.cluster.local:8080
OTEL_EXPORTER_OTLP_ENDPOINT=http://stratum-otel-collector:4317
```

## CI/CD 部署链路

`.github/workflows/deploy.yml`（"Build and Deploy"）的触发方式：

- 主路径是 `workflow_run`：CI 在 `main` 分支跑成功后自动触发；
- 另外支持 `push` tag `v*` 和 `workflow_dispatch` 手动触发。

deploy 第一步会解析并校验**不可变 commit SHA**：`workflow_run` 事件要求触发它的 CI 成功、分支是
`main`，且 candidate SHA 必须等于 GitHub 当前 `main` 的 commit SHA，否则拒绝部署（防止 base 落后
导致的"合并结果变了却还按旧 SHA 部署"）。

```mermaid
flowchart LR
    push["push main / PR"] --> ci["CI<br/>go vet/test、lint、dto-residue-guard"]
    ci -->|"workflow_run completed success"| deploy["Build and Deploy"]
    tag["push tag v*"] --> deploy
    manual["workflow_dispatch"] --> deploy
    deploy --> candidate["解析并校验不可变 commit SHA"]
    candidate --> build["buildx 构建<br/>backend + frontend + feishu-alert-adapter<br/>推阿里云 CR"]
    build --> deps["发布依赖镜像并解析 digest<br/>postgres/redis/nats/etcd/minio/milvus/nacos/mysql"]
    deps --> tunnel["SSH tunnel 到 K3s API :6443"]
    tunnel --> cm["cert-manager v1.21.1 + 内部 CA bootstrap"]
    cm --> opik["安装 pinned Opik 2.1.32<br/>namespace: opik"]
    opik --> helm["helm upgrade --install<br/>values-demo + values-demo-remote-http<br/>--set digest/frontendUrl/secureCookies"]
    helm --> obs["Loki/Promtail/Jaeger/collector<br/>+ ServiceMonitor/PrometheusRule"]
    obs --> verify["rollout status + 公网 /api/health 验证"]
    verify --> conntrack["清理 otel collector 过期 conntrack"]
    conntrack --> evidence["记录部署 receipt + attestation"]
```

部署使用**镜像 digest 固定**：backend/frontend 推送到阿里云 CR 时带 commit SHA tag，deploy 阶段对
所有镜像（backend/frontend/postgres/redis/nats/etcd/minio/milvus/nacos/mysql）解析并注入 registry
digest：

```yaml
--set app.image.repository="$IMAGE_REPO/stratum-backend" \
--set-string app.image.digest="${{ steps.images.outputs.backend }}"
```

这样 K3s 每次都拉取与本次 commit 唯一对应的镜像，彻底避免"CI 成功但节点复用本地旧镜像"的问题
（旧的 `IfNotPresent` + 固定 branch tag 组合的坑）。PostgreSQL 用的是自建
`postgres:16-zhparser` 镜像（`docker/postgres-zhparser.Dockerfile`，发布为
`<CR>/postgres:16-zhparser-v1`），带 zhparser 中文全文检索扩展。

deploy 完成后还会：

- 通过 `scripts/deploy-observability-logging.sh` 应用并滚动 Loki/Promtail/Jaeger，校验 promtail
  日志流真实到达 Loki（fail closed）；
- 应用 `monitoring/remote/resources/observability-monitors.yaml` 与
  `monitoring/remote/generated/stratum-prometheus-rules.yaml`；
- 清理到 otel collector Service VIP 的过期 conntrack 条目，防止 OTLP 连接被钉到被替换的 collector；
- 记录部署 receipt（backend/frontend/adapter digest、rollback basis）并做 attestation。

远端监控 reconcile 不在 deploy 内联执行：`.github/workflows/reconcile-monitoring.yml` 通过
`workflow_run` 在 "Build and Deploy" 完成后触发（另加每日 schedule `17 3 * * *` 与手动触发），
与 deploy 共享 `stratum-production` concurrency group，避免两个工作流同时访问集群。reconcile 消费
的 Feishu adapter 镜像优先读集群内已部署的 digest 形式，缺失时按 head SHA 从镜像仓库解析并校验
`sha256:` digest，保证永不回退到可变 tag。

另有 `.github/workflows/rollback.yml`（手动 Helm rollback）和
`.github/workflows/remote-health-monitor.yml`（每 5 分钟探测 `PUBLIC_BASE_URL/api/health` 并对账
告警状态）。

## 后端 auth route 的启动条件

后端的 `/auth/*` 路由不是无条件注册的。当前代码要求：

```text
GITHUB_CLIENT_ID 非空
JWTService 初始化成功
```

如果 JWT 私钥解析失败，后端会跳过注册 `/auth/github`、`/auth/github/callback`、`/auth/me` 等路由，外部表现就是：

```text
GET /api/auth/github -> 404
```

当前代码已支持两种 RSA 私钥格式：

- PKCS#1: `-----BEGIN RSA PRIVATE KEY-----` <!-- gitleaks:allow -->
- PKCS#8: `-----BEGIN PRIVATE KEY-----` <!-- gitleaks:allow -->

这点很重要，因为很多工具生成的是 PKCS#8。如果只支持 PKCS#1，日志会出现类似错误：

```text
JWT private key parse failed, auth routes disabled
```

## 当前验证命令

从本机验证公网入口（证书是内部 CA，需 `--insecure`）：

```bash
curl --noproxy '*' -kI https://101.200.181.141:8443/
```

验证 API 代理链路：

```bash
curl --noproxy '*' -ki https://101.200.181.141:8443/api/health
```

验证 GitHub OAuth 登录入口：

```bash
curl --noproxy '*' -ki https://101.200.181.141:8443/api/auth/github
```

期望结果是 `302 Found`，并且 `Location` 指向 GitHub：

```text
Location: https://github.com/login/oauth/authorize?...redirect_uri=https://101.200.181.141:8443/api/auth/github/callback...
```

查看集群状态：

```bash
ssh root@101.200.181.141 'kubectl get pods -n stratum -o wide'
```

查看远端监控栈：

```bash
ssh root@101.200.181.141 'kubectl get all -n monitoring'
ssh root@101.200.181.141 'kubectl get ingress,svc,cm -n monitoring | grep -i grafana'
ssh root@101.200.181.141 'kubectl get secret -n monitoring -l owner=helm'
```

确认后端 Pod 里的关键配置：

```bash
ssh root@101.200.181.141 \
  'kubectl exec -n stratum deploy/stratum -- sh -c '"'"'
    echo GITHUB_CALLBACK_URL=$GITHUB_CALLBACK_URL
    for k in GITHUB_CLIENT_ID GITHUB_CLIENT_SECRET JWT_PRIVATE_KEY_PEM; do
      if [ -n "$(printenv $k)" ]; then echo "$k=SET"; else echo "$k=UNSET"; fi
    done
  '"'"''
```

确认后端 auth route 已注册：

```bash
ssh root@101.200.181.141 \
  'kubectl logs -n stratum deploy/stratum --tail=160 | grep -E "GET[[:space:]]+/auth|/auth/github"'
```

期望能看到：

```text
GET /auth/github
GET /auth/github/callback
GET /auth/me
```

## 当前已知限制

- 公网是 HTTPS，但证书来自 cert-manager **内部 CA**（根 CA 不公开受信），浏览器首次访问有安全
  警告；脚本/curl 验证需 `--insecure`。
- `secureCookies`：`helm/values-demo.yaml` 和 `helm/values-demo-remote-http.yaml` 都写
  `"true"`，但 deploy.yml 的 `--set-string config.secureCookies="false"` 会把它覆盖为 `"false"`
  （该覆盖自 2026-07 引入 remote HTTP profile 时保留至今）。接入正式可信 HTTPS 后应移除该覆盖。
- Ingress 当前不限制 Host（`host: ""`），适合 IP demo；生产环境应配置正式域名。
- `observability.enabled` 默认 `true`；`stratum-otel-collector` 由 deploy.yml 从
  `k8s/opik-otel-collector.yaml` 应用，不再受 chart 开关控制。它把 trace 转发到
  `opik-backend.opik`（OTLP/HTTP）和 `jaeger.monitoring`（OTLP/gRPC）。
- **同名 Service 冲突（repo 现状，运行期需确认）**：`k8s/tracing.yaml` 还定义了一套
  `otel-collector` Deployment（image `otel/opentelemetry-collector-contrib:0.102.0`，
  selector `app: otel-collector`）和同名 Service `stratum-otel-collector`
  （selector `app: otel-collector`），与 `k8s/opik-otel-collector.yaml` 的 Service 同名。
  两份 manifest 均被 deploy.yml 应用（L557 应用 opik-otel-collector.yaml，L628 经
  `deploy-observability-logging.sh` 应用 tracing.yaml），同名 Service 的最终归属取决于
  kubectl apply 顺序；运行期实际由哪个 collector 承接后端 OTLP 流量，需
  `kubectl get deploy,svc -n stratum` 确认。
- 远端 Grafana 是独立 `kps` 监控栈，不使用仓库根目录 `grafana/`；当前 values 未给 Grafana 配公网
  Ingress，需 port-forward 访问。
- 远端监控配置只认 `monitoring/remote/`；运行与告警处置见
  `docs/operations/remote-monitoring-runbook.md` 和 `docs/operations/alerts/`。
- 监控升级/回滚不得 uninstall release，也不得删除 Prometheus/Grafana PVC、monitoring CRD 或 Helm history。
- 单节点 K3s 适合 demo，不是高可用生产架构。

## 后续接入正式域名和公共证书

当前已是 HTTPS（IP + 内部 CA）。有正式域名后，建议按这个顺序升级：

1. DNS A 记录指向公网 IP（`101.200.181.141`）。
2. `ingress.hosts[0].host` 从空字符串改成正式域名。
3. 把 TLS 换成公共可信证书：用 Let's Encrypt 之类的外部 CA（例如 `helm/values-prod.yaml` 的
   `ingress.annotations["cert-manager.io/cluster-issuer"]: letsencrypt-prod`），或为域名申请商业证书，
   替换 `stratum-ingress-tls` 的内部 CA 证书。
4. `frontendUrl` / `githubCallbackUrl` 改成 `https://<正式域名>`（同时更新 `PUBLIC_BASE_URL`）。
5. GitHub OAuth App 的 callback URL 同步改成 HTTPS 域名地址。
6. 移除 deploy.yml 里 `--set-string config.secureCookies="false"` 覆盖，让 `secureCookies` 保持
   `"true"`。
7. 删除 `k8s/stratum-tls.yaml` 中 IP 固定的 Certificate 相关逻辑（或改按域名签发）。

域名和正式证书接入后，请重新验证：

```bash
curl -I https://<正式域名>/
curl -i https://<正式域名>/api/health
curl -i https://<正式域名>/api/auth/github
```

## 排障思路

遇到访问问题时，按层排查，不要直接猜代码问题：

1. 首页打不开：先查 ECS 安全组（放行 8443）、Traefik Ingress、frontend Pod、`stratum-ingress-tls`
   证书 Secret。
2. `/api/health` 失败：查前端 Nginx `/api/` 代理和 backend Service。
3. `/api/auth/github` 404：查后端是否注册 `/auth/github`，再查 `GITHUB_CLIENT_ID` 和 `JWT_PRIVATE_KEY_PEM`。
4. GitHub 提示 redirect_uri 不匹配：查 GitHub OAuth App callback URL（必须是
   `https://<public-ip>:8443/api/auth/github/callback`）。
5. CI 成功但代码没变：确认 Pod 镜像 digest 是否与本次 commit 对应，而不是旧镜像缓存。
6. 证书不受信任：预期行为（内部 CA）。接入正式域名后应替换为公共证书。
7. Grafana 看不到新配置：先确认你改的是远端 `monitoring/kps-*` ConfigMap，而不是本地
   docker-compose 使用的仓库 `grafana/` 目录。
