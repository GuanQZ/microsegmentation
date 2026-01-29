# 部署指引（测试环境，无脑步骤）

> 目标：在测试集群（上下文：`nmzjeiet`）部署本程序。
> 注意：本文假设你已具备 docker 与 kubectl 权限。

## 1) 切换集群上下文

```bash
kubectl config use-context nmzjeiet
```

## 2) 构建本地镜像

```bash
docker build -t microsegmentation:latest .
```

## 3) 推送到镜像仓库

> 请将 `__IMAGE_PLACEHOLDER__` 替换成你的真实仓库地址（例如 `registry.example.com/team/microsegmentation:latest`）。

```bash
docker tag microsegmentation:latest __IMAGE_PLACEHOLDER__
docker login <你的镜像仓库>
docker push __IMAGE_PLACEHOLDER__
```

## 4) 修改部署清单中的镜像地址

编辑 [manifests/daemonset.yaml](../manifests/daemonset.yaml) 的 `image` 字段：

- 将 `__IMAGE_PLACEHOLDER__` 替换成你的真实镜像地址。

## 5) 部署到集群（含权限与服务）

```bash
kubectl apply -f manifests/daemonset.yaml
```

> 说明：
> - YAML 文件只需要存在于你执行 `kubectl` 的那台机器（比如跳板机/本地电脑），不需要上传到任何节点。
> - `kubectl apply` 会把清单提交到 apiserver，Kubernetes 控制器再去各节点创建资源。

> 说明：
> - RBAC（ClusterRole/Binding）、ServiceAccount、Service 已包含在清单中，无需额外配置。
> - 需要 `NET_ADMIN` 能力以修改主机 iptables。
> - 已包含一个 headless Service（`microsegmentation-api-headless`），用于管理端发现所有节点上的 Pod 并做广播下发。

## 6) 检查运行状态

```bash
kubectl -n microsegmentation get pods -o wide
```

期望：每个节点 1 个 Pod，状态 `Running`。

## 7) 查看日志

```bash
kubectl -n microsegmentation logs -l app=microsegmentation --tail=200
```

期望：看到 `starting api server` 与 `sync completed`。

## 8) 健康检查

```bash
kubectl -n microsegmentation run curl --image=curlimages/curl --restart=Never --command -- \
  curl -sS http://microsegmentation-api.microsegmentation.svc.cluster.local:18080/healthz
```

期望：返回 `ok`。

## 9) 下发策略（示例）

```bash
kubectl -n microsegmentation run curl --image=curlimages/curl --restart=Never --command -- \
  curl -sS -X POST http://microsegmentation-api.microsegmentation.svc.cluster.local:18080/apply \
  -H 'Content-Type: application/json' \
  -d '{"defaultAction":"ALLOW","deployments":[{"namespace":"default","name":"web","ingressFrom":[{"namespace":"default","name":"client"}]}]}'
```

## 9.1) 广播下发（通过 headless Service）

> 说明：普通 Service 只会命中一个 Pod；要覆盖所有节点需解析 headless Service 得到全部 Pod IP 并逐个调用。

管理端实现要点：
1) 解析 `microsegmentation-api-headless.microsegmentation.svc.cluster.local`，拿到所有 Pod IP（多条 A 记录）。
2) 对每个 IP 发送 `POST http://<ip>:18080/apply` 请求。
3) 请求头：
  - `Content-Type: application/json`
  - `X-API-Token: <token>`（如启用鉴权）
4) 请求体：与接口文档一致（白名单/旧规则均可）。
5) 响应：
  - 成功：`200 OK`，Body 为 `ok`
  - 失败：`400/401/500`，Body 为错误文本

```bash
kubectl -n microsegmentation run curl --image=curlimages/curl --restart=Never --command -- \
  sh -c "for ip in $(getent hosts microsegmentation-api-headless.microsegmentation.svc.cluster.local | awk '{print $1}'); do \
    curl -sS -X POST http://$ip:18080/apply -H 'Content-Type: application/json' \
    -d '{\"defaultAction\":\"ALLOW\",\"deployments\":[{\"namespace\":\"default\",\"name\":\"web\",\"ingressFrom\":[{\"namespace\":\"default\",\"name\":\"client\"}]}]}'; \
  done"
```

## 10) 回滚/清理

```bash
kubectl delete -f manifests/daemonset.yaml
```
