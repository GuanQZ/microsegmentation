# MS Iptables Controller

目标：在 CCE 节点上运行的守护进程，用以基于 Kubernetes `Deployment` 管理本节点上的 iptables 规则，实现对各 `Deployment` 的网络访问控制。

主要功能：
- 定期同步集群中 `Deployment` 与本节点上 `Pod` 的对应关系，根据 Pod IP 列表在本节点上创建/更新 iptables 链。
- 对外管理接口：程序内置 HTTP API，外部管理端可直接调用接口下发策略（无需 ConfigMap）。
- 记录每次规则变更的时间（通过日志）。
- 与 Calico 兼容：使用独立的自定义链（前缀 `MS`）并在 FORWARD 上添加跳转，尽量避免直接修改 Calico 的链。
- 高可用性：以 `DaemonSet` 方式在每个节点运行；Kubernetes 负责重启。

目录结构：
- `cmd/` 主程序
- `internal/iptables` iptables 操作封装
- `internal/kube` kube client
- `manifests/` Kubernetes 部署清单
- `Dockerfile` 镜像构建

安装与部署（概要）：

1. 构建镜像

```bash
docker build -t YOUR_REGISTRY/ms-iptables:latest .
docker push YOUR_REGISTRY/ms-iptables:latest
```

2. 修改清单中的镜像地址：`manifests/daemonset.yaml` 中 `image` 字段为上一步镜像。

3. 应用清单

```bash
kubectl apply -f manifests/daemonset.yaml
```

权限要求与安全上下文：
- 需要 `list/watch` 权限用于 `pods` 与 `deployments`（清单中已包含 `ClusterRole`）。
- 容器需要 `NET_ADMIN` 能力以变更主机 iptables（清单已添加 capability）。另外建议以 `hostNetwork: true` 方式运行（清单已配置）。

运行时注意：
- 该程序在容器中执行系统 `iptables` 命令，因此镜像需包含 `iptables` 二进制（Dockerfile 已安装）。
- 请确保容器运行用户有权限执行 `iptables`（通常需要 root 或 NET_ADMIN 能力）。

Calico 兼容性与冲突避免：
- 本程序创建以 `MS-` 前缀命名的自定义链，并在 `FORWARD` 链上添加跳转。Calico 也会操作 iptables，可能会添加自己的链和跳转。为尽量减少冲突：
  - 使用独立命名空间的链（前缀明确）。
  - 不删除或直接修改 Calico 的链；只对自有链进行 flush/append 操作。
  - 在生产环境中先在测试集群验证链优先级与策略，确保没有覆盖或阻断 Calico 必需的规则。

高可用与动态扩展：
- 使用 `DaemonSet` 在每个节点运行本程序，当节点故障时 Kubernetes 会调度 Pod 到其他节点或在节点恢复后重启。
- 程序定期同步 `Deployment` 和 `Pod` 信息，能适应集群规模变化和 Pod 的重建。

日志与审计：
- 程序通过标准输出记录日志，包含每次规则变更时间。建议搭配集群日志系统（例如 Fluentd/Elastic Stack）收集。

权限细化建议（生产）：
- 如果想最小化权限，可将 `ClusterRole` 改为 `Role` 并按命名空间部署多个实例（每个实例仅观察其命名空间）。

常见问题：
- Q: Calico 规则被覆盖？
  A: 本程序不主动删除 Calico 链。若出现冲突，请检查 iptables 链的顺序，并确保本程序插入点合适（在安全的优先级下）。
- Q: 为什么容器需要 `hostNetwork`？
  A: 修改主机 iptables 最可靠方式是运行在主机网络命名空间（`hostNetwork: true`），也可通过特权容器和直接操作 `/run/netns` 实现更细粒度的方案。

管理接口与策略配置：
- 外部管理端通过 HTTP API 直接下发策略（PUT /policy）。
- 默认监听 `:18080`，可通过环境变量 `API_BIND` 调整。
- 若设置 `API_TOKEN`，请求需携带 `X-API-Token` 头。
- 可选 `POLICY_FILE` 用于策略持久化（程序重启后恢复）。
- 默认 `FORWARD_JUMP_POSITION=insert`，确保策略优先匹配；如需降低对 CNI 的影响可切换为 `append`。

策略 JSON 结构（示例）：

```json
{
  "defaultAction": "ALLOW",
  "deployments": [
    {
      "namespace": "default",
      "name": "web",
      "rules": [
        {"action": "ALLOW", "srcCIDR": "10.0.0.0/24", "protocol": "tcp", "port": 80},
        {"action": "DENY", "srcCIDR": "0.0.0.0/0"}
      ]
    }
  ]
}
```

字段说明：
- `defaultAction`: 当未配置具体规则或规则不匹配时的默认动作。支持：`ALLOW`(=ACCEPT)、`DENY`(=DROP)、`REJECT`、`RETURN`。
- `deployments`: Deployment 规则列表。
  - `namespace`: Deployment 所在命名空间。
  - `name`: Deployment 名称。
  - `rules`: 规则列表。
    - `action`: 动作。
    - `srcCIDR`: 源地址 CIDR（可选）。
    - `protocol`: 协议（可选，tcp/udp/icmp）。
    - `port`: 目的端口（仅对 tcp/udp 生效，0 表示忽略）。

外部管理端示例操作（HTTP）：

```bash
curl -X PUT http://<node-ip>:18080/policy \
  -H 'Content-Type: application/json' \
  -H 'X-API-Token: your-token' \
  -d @policy.json

curl http://<node-ip>:18080/policy -H 'X-API-Token: your-token'
```

集群内访问方式（推荐）：
- 已提供 `Service`：`ms-iptables-api`（ClusterIP），管理端可通过集群内 DNS 访问：
  - `http://ms-iptables-api.ms-iptables.svc.cluster.local:18080/policy`

示例（集群内 Pod 中）：

```bash
curl -X PUT http://ms-iptables-api.ms-iptables.svc.cluster.local:18080/policy \
  -H 'Content-Type: application/json' \
  -H 'X-API-Token: your-token' \
  -d @policy.json

curl http://ms-iptables-api.ms-iptables.svc.cluster.local:18080/policy -H 'X-API-Token: your-token'
```

说明：
- 若使用 DaemonSet，每个节点都有一个实例，需要对所有节点进行下发或在管理端实现广播。
- 如需集中式管理，可在集群内增加一个“策略分发服务”，由其负责调用每个节点实例的 API。

进一步工作建议：
- 支持通过 CRD 或注解配置每个 `Deployment` 的访问策略（例如白名单/黑名单、端口、方向）。
- 增加更细粒度的 leader election 或分布式协调策略以避免多副本冲突（当前为每节点本地操作，冲突风险较低）。
- 增加单元测试和集成测试用例。
