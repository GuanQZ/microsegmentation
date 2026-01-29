# 管理端接口文档

本文档描述当前对外 HTTP 接口，用于策略下发与健康检查。

## 1. 基本信息
- Base URL：`http://<node-ip>:18080` 或集群内 Service：`http://ms-iptables-api.ms-iptables.svc.cluster.local:18080`
- 认证：可选 Token（请求头 `X-API-Token`）

## 2. 认证方式
- 若设置环境变量 `API_TOKEN`，所有请求必须带：`X-API-Token: <token>`
- 若未设置，则不校验 Token。

## 3. 健康检查
### GET /healthz
- 描述：健康检查
- 请求头：无
- 响应：
  - `200 OK`
  - Body：`ok`

## 4. 查询策略
### GET /policy
- 描述：获取当前策略
- 请求头：
  - `X-API-Token`（可选，若启用鉴权则必填）
- 响应：
  - `200 OK`
  - Body：当前策略 JSON

## 5. 下发策略
### POST /apply
- 描述：更新策略并在下一个同步周期生效
- 请求头：
  - `Content-Type: application/json`
  - `X-API-Token`（可选，若启用鉴权则必填）

### 5.1 请求体结构
根对象：
- `defaultAction` (string，可选)：旧规则兜底动作。可选值：`ALLOW`/`ACCEPT`、`DENY`/`DROP`、`REJECT`、`RETURN`。默认 `ALLOW`。
- `deployments` (array，必填)：策略列表。

`deployments[]` 每一项：
- `namespace` (string，必填)：目标 Deployment 命名空间。
- `name` (string，必填)：目标 Deployment 名称。
- `ingressFrom` (array，可选)：允许访问该 Deployment 的来源白名单（Deployment 引用列表）。为空或缺省表示**不限制来源**。
- `egressTo` (array，可选)：该 Deployment 允许访问的目标白名单（Deployment 引用列表）。为空或缺省表示**不限制去向**。
- `rules` (array，可选)：旧规则（CIDR/端口）列表，仅当 `ingressFrom` 未配置时生效。

`ingressFrom[]` / `egressTo[]` 引用结构：
- `namespace` (string，必填)：引用 Deployment 的命名空间。
- `name` (string，必填)：引用 Deployment 的名称。

`rules[]` 规则结构（旧规则兼容）：
- `action` (string，可选)：动作。可选值：`ALLOW`/`ACCEPT`、`DENY`/`DROP`、`REJECT`、`RETURN`。
- `srcCIDR` (string，可选)：源地址 CIDR，例如 `10.0.0.0/24`。
- `protocol` (string，可选)：`tcp`/`udp`/`icmp`。
- `port` (int，可选)：目的端口，仅 `tcp/udp` 时生效；`0` 或缺省表示不限制端口。

### 5.2 请求体示例
白名单示例：
```json
{
  "defaultAction": "ALLOW",
  "deployments": [
    {
      "namespace": "default",
      "name": "web",
      "ingressFrom": [
        {"namespace": "default", "name": "client"}
      ],
      "egressTo": [
        {"namespace": "default", "name": "api"}
      ]
    }
  ]
}
```
旧规则（CIDR/端口）示例：
```json
{
  "defaultAction": "ALLOW",
  "deployments": [
    {
      "namespace": "default",
      "name": "web",
      "rules": [
        {"action": "ALLOW", "srcCIDR": "10.244.0.0/16", "protocol": "tcp", "port": 80},
        {"action": "DENY", "srcCIDR": "0.0.0.0/0"}
      ]
    }
  ]
}
```

### 5.3 响应
- `200 OK`：`ok`
- `400 Bad Request`：`invalid json`
- `401 Unauthorized`：`unauthorized`
- `500 Internal Server Error`：`set policy failed`

## 6. 策略语义说明
- `ingressFrom`：允许访问该 Deployment 的来源白名单。为空则放行所有来源。
- `egressTo`：该 Deployment 允许访问的目标白名单。为空则放行所有去向。
- 一旦配置白名单，未命中即拒绝。
- 白名单按 Deployment 维度生效，底层以 Pod IP 集合匹配。
- 旧 `rules` 仅在 `ingressFrom` 未配置时生效。

## 7. 注意事项
- 接口无批量广播能力，DaemonSet 每个节点实例需单独下发，或由管理端实现节点级广播。
- 若配置 `POLICY_FILE`，策略会持久化到本地文件并在重启后恢复。
