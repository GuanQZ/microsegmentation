# 策略生效测试文档（可复现步骤）

本文档用于验证本程序的策略下发与实际网络访问控制效果。步骤已在本机 kind 环境验证，可复现。

> 说明：默认已将 `FORWARD_JUMP_POSITION` 设置为 `insert`，以确保规则优先生效。

## 一、前置条件

1. 已创建 kind 集群（K8s v1.28.x）并安装 Calico。
2. 已部署本程序：

```bash
kubectl apply -f manifests/daemonset.yaml
```

3. 确认 `ms-iptables` Pod 正常运行：

```bash
kubectl -n ms-iptables get pods -o wide
```

## 二、开启“强制优先”跳转（测试用）

> 目的：避免 Calico 在 FORWARD 链前置 ACCEPT 规则导致策略不生效。

```bash
kubectl -n ms-iptables set env daemonset/ms-iptables FORWARD_JUMP_POSITION=insert
kubectl -n ms-iptables delete pod -l app=ms-iptables
kubectl -n ms-iptables get pods -o wide
```

## 三、创建测试服务（Deployment）

```bash
cat <<'EOF' > /tmp/web-deploy.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: default
spec:
  replicas: 1
  selector:
    matchLabels:
      app: web
  template:
    metadata:
      labels:
        app: web
    spec:
      containers:
        - name: nginx
          image: nginx:1.25-alpine
          ports:
            - containerPort: 80
EOF

kubectl apply -f /tmp/web-deploy.yaml
kubectl rollout status deployment/web --timeout=120s
```

获取 `web` Pod IP：

```bash
WEB_POD_IP=$(kubectl get pod -l app=web -o jsonpath='{.items[0].status.podIP}')
echo $WEB_POD_IP
```

## 四、准备一个“客户端 Pod”

```bash
kubectl -n default delete pod client --ignore-not-found
kubectl -n default run client --image=curlimages/curl --restart=Never --command -- sleep 3600
kubectl -n default get pod client -o wide
```

## 五、测试“拒绝”行为

### 5.1 下发更严格策略（允许 CIDR 不包含当前 Pod 源 IP）

```bash
kubectl -n ms-iptables run curl --image=curlimages/curl --restart=Never --command -- \
  curl -sS -X PUT http://ms-iptables-api.ms-iptables.svc.cluster.local:18080/policy \
  -H 'Content-Type: application/json' \
  -d '{"defaultAction":"ALLOW","deployments":[{"namespace":"default","name":"web","rules":[{"action":"ALLOW","srcCIDR":"10.244.0.0/24","protocol":"tcp","port":80},{"action":"DENY","srcCIDR":"0.0.0.0/0"}]}]}'

kubectl -n ms-iptables delete pod curl --ignore-not-found
sleep 35
```

### 5.2 从 client Pod 访问 web:80（预期超时）

```bash
kubectl -n default exec client -- \
  curl -sS -m 5 -o /dev/null -w 'HTTP:%{http_code}\n' http://$WEB_POD_IP:80
```

**预期结果**：
- 返回 `HTTP:000`
- curl 超时（`Connection timed out`）

## 六、测试“允许”行为

### 6.1 下发放宽策略（允许 10.244.0.0/16）

```bash
kubectl -n ms-iptables run curl --image=curlimages/curl --restart=Never --command -- \
  curl -sS -X PUT http://ms-iptables-api.ms-iptables.svc.cluster.local:18080/policy \
  -H 'Content-Type: application/json' \
  -d '{"defaultAction":"ALLOW","deployments":[{"namespace":"default","name":"web","rules":[{"action":"ALLOW","srcCIDR":"10.244.0.0/16","protocol":"tcp","port":80},{"action":"DENY","srcCIDR":"0.0.0.0/0"}]}]}'

kubectl -n ms-iptables delete pod curl --ignore-not-found
sleep 35
```

### 6.2 从 client Pod 访问 web:80（预期成功）

```bash
kubectl -n default exec client -- \
  curl -sS -m 5 -o /dev/null -w 'HTTP:%{http_code}\n' http://$WEB_POD_IP:80
```

**预期结果**：
- 返回 `HTTP:200`

## 七、Service 场景测试（ClusterIP）

> 目的：验证通过 Service 访问时策略是否生效。

1) 创建或确认 Service：

```bash
kubectl get svc web -n default >/dev/null 2>&1 || \
  kubectl expose deploy web -n default --port=80 --target-port=80 --name web

WEB_SVC_IP=$(kubectl -n default get svc web -o jsonpath='{.spec.clusterIP}')
echo $WEB_SVC_IP
```

2) 允许策略（`10.244.0.0/16`）访问 Service：

```bash
kubectl -n default exec client -- \
  curl -sS -m 5 -o /dev/null -w 'HTTP:%{http_code}\n' http://$WEB_SVC_IP:80
```

**实测结果**：`HTTP:200`

3) 拒绝策略（`10.244.0.0/24`）访问 Service：

```bash
kubectl -n default exec client -- \
  curl -sS -m 5 -o /dev/null -w 'HTTP:%{http_code}\n' http://$WEB_SVC_IP:80
```

**实测结果**：`HTTP:000` + 超时

## 八、DNS 场景测试（Service DNS）

> 目的：验证通过 DNS 访问时策略是否生效。

1) 允许策略访问 DNS：

```bash
kubectl -n default exec client -- \
  curl -sS -m 5 -o /dev/null -w 'HTTP:%{http_code}\n' http://web.default.svc.cluster.local:80
```

**实测结果**：`HTTP:200`

2) 拒绝策略访问 DNS：

```bash
kubectl -n default exec client -- \
  curl -sS -m 5 -o /dev/null -w 'HTTP:%{http_code}\n' http://web.default.svc.cluster.local:80
```

**实测结果**：`HTTP:000` + 超时

## 九、跨命名空间场景测试（ns2）

> 目的：验证跨命名空间 Service/DNS 访问是否受策略控制。

1) 创建 ns2 与服务：

```bash
kubectl get ns ns2 >/dev/null 2>&1 || kubectl create ns ns2

cat <<'EOF' > /tmp/api-deploy.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: ns2
spec:
  replicas: 1
  selector:
    matchLabels:
      app: api
  template:
    metadata:
      labels:
        app: api
    spec:
      containers:
        - name: nginx
          image: nginx:1.25-alpine
          ports:
            - containerPort: 80
EOF

kubectl apply -f /tmp/api-deploy.yaml
kubectl rollout status deployment/api -n ns2 --timeout=120s
kubectl get svc api -n ns2 >/dev/null 2>&1 || \
  kubectl expose deploy api -n ns2 --port=80 --target-port=80 --name api

NS2_SVC_IP=$(kubectl -n ns2 get svc api -o jsonpath='{.spec.clusterIP}')
echo $NS2_SVC_IP
```

2) 允许策略（`10.244.0.0/16`）访问跨命名空间 Service：

```bash
kubectl -n default exec client -- \
  curl -sS -m 5 -o /dev/null -w 'HTTP:%{http_code}\n' http://$NS2_SVC_IP:80
```

**实测结果**：`HTTP:200`

3) 拒绝策略（`10.244.0.0/24`）访问跨命名空间 Service：

```bash
kubectl -n default exec client -- \
  curl -sS -m 5 -o /dev/null -w 'HTTP:%{http_code}\n' http://$NS2_SVC_IP:80
```

**实测结果**：`HTTP:000` + 超时

4) 跨命名空间 DNS 访问：

```bash
kubectl -n default exec client -- \
  curl -sS -m 5 -o /dev/null -w 'HTTP:%{http_code}\n' http://api.ns2.svc.cluster.local:80
```

**实测结果**：
- 允许策略：`HTTP:200`
- 拒绝策略：`HTTP:000` + 超时

## 十、检查 iptables 规则（可选）

```bash
docker exec -i ms-dev-control-plane iptables -S MS-ROOT-CHAIN
docker exec -i ms-dev-control-plane iptables -S MS-DEFAULT-WEB
```

预期规则示例（ALLOW /16）：

```
-A MS-DEFAULT-WEB -s 10.244.0.0/16 -d <WEB_POD_IP>/32 -p tcp --dport 80 -j ACCEPT
-A MS-DEFAULT-WEB -d <WEB_POD_IP>/32 -j DROP
```

## 十一、清理测试资源（可选）

```bash
kubectl -n default delete pod client --ignore-not-found
kubectl -n default delete deployment web --ignore-not-found
```

## 十二、实测结果记录（本次）

环境：kind（K8s v1.28.6）+ Calico，`FORWARD_JUMP_POSITION=insert`

1) 拒绝测试（允许 CIDR 不匹配）
- 策略：允许 `10.244.0.0/24` + 默认 `DROP`
- 从 `client` 访问 `web:80` 结果：
  - `HTTP:000`
  - `Connection timed out`（5s 超时）

2) 允许测试（允许 CIDR 覆盖来源）
- 策略：允许 `10.244.0.0/16` + 默认 `DROP`
- 从 `client` 访问 `web:80` 结果：
  - `HTTP:200`

## 十三、当前测试资源清单（可直接复用）

> 以下信息基于本次测试环境的实测输出，便于他人快速对照与复现。

### 13.1 kind 集群信息

- 集群名称：`ms-dev`
- 节点：`ms-dev-control-plane`
- Kubernetes 版本：`v1.28.6`
- 容器运行时：`containerd://1.7.13`

### 13.2 命名空间

- `default`
- `kube-system`
- `local-path-storage`
- `ms-iptables`
- `ns2`

### 13.3 部署（Deployments）

- `default/web`
- `ns2/api`
- `kube-system/calico-kube-controllers`
- `kube-system/coredns`
- `local-path-storage/local-path-provisioner`

### 13.4 Service

- `default/web`（ClusterIP）
  - ClusterIP: `10.96.105.79`
  - Port: `80/TCP`
- `ns2/api`（ClusterIP）
  - ClusterIP: `10.96.198.239`
  - Port: `80/TCP`
- `ms-iptables/ms-iptables-api`（ClusterIP）
  - ClusterIP: `10.96.249.152`
  - Port: `18080/TCP`

### 13.5 Pod

- `default/web-7c979499f4-fn87c`（Running）
- `default/client`（Completed）
- `ns2/api-699b7f9464-74jv2`（Running）
- `ms-iptables/ms-iptables-rglnf`（Running）
- `kube-system/calico-node-qnqkk`（Running）
- `kube-system/coredns-*`（Running）

### 13.6 快速获取资源信息命令（复用）

```bash
kind get clusters
kubectl get nodes -o wide
kubectl get ns
kubectl get deploy -A
kubectl get svc -A
kubectl get pods -A
```

---

如需恢复更“温和”的规则插入策略（减少对 CNI 影响），可将 `FORWARD_JUMP_POSITION` 设置为 `append` 并重启 DaemonSet。