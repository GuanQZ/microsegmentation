package main

import (
    "context"
    "flag"
    "log"
    "net/http"
    "os"
    "time"

    "github.com/example/iptables-controller/internal/controller"
    "github.com/example/iptables-controller/internal/kube"
)

// 程序入口：初始化 Kubernetes 客户端并启动守护进程的周期性同步循环。
// 说明：
// - 从环境变量 `NODE_NAME` 获取所在节点名（在 DaemonSet 中通过 fieldRef 填充）。
// - 使用 `kube.NewClient()` 优先采用 InClusterConfig，回退到本地 kubeconfig 以便本地调试。
// - 创建 `controller` 实例并以 `sync-interval` 指定的间隔周期性调用 `Sync` 方法，保持本节点 iptables 规则与集群 Deployment/Pod 状态一致。
func main() {
    var syncInterval time.Duration
    flag.DurationVar(&syncInterval, "sync-interval", 30*time.Second, "sync interval")
    flag.Parse()

    ctx := context.Background()

    // 环境变量说明：
    // - NODE_NAME: 在 DaemonSet 中该环境变量通常通过 fieldRef 填充为当前 Pod 所在的节点名（spec.nodeName）。
    //   用途：用于筛选属于本节点的 Pod（通过 fieldSelector: spec.nodeName=<NODE_NAME>）。
    //   注意：若在本地调试运行，可手动设置该环境变量；在集群中部署时无需手动设置。
    // - API_BIND: HTTP 管理接口监听地址（默认 :18080）。
    // - API_TOKEN: 可选 API 访问令牌（若设置，客户端需在请求头中带 X-API-Token）。
    // - POLICY_FILE: 可选策略持久化文件路径（为空则不落盘）。
    // - FORWARD_JUMP_POSITION: FORWARD 链跳转插入方式（append/insert）。
    nodeName := os.Getenv("NODE_NAME")
    if nodeName == "" {
        log.Fatal("NODE_NAME environment variable is required")
    }

    apiBind := os.Getenv("API_BIND")
    if apiBind == "" {
        apiBind = ":18080"
    }
    apiToken := os.Getenv("API_TOKEN")
    policyFile := os.Getenv("POLICY_FILE")
    forwardJumpPosition := os.Getenv("FORWARD_JUMP_POSITION")

    kc, err := kube.NewClient()
    if err != nil {
        log.Fatalf("failed to create kube client: %v", err)
    }

    // 初始化策略存储与 HTTP API（同一进程内）
    policyStore := controller.NewPolicyStore(policyFile)
    apiServer := controller.NewAPIServer(policyStore, apiToken)

    // 启动 HTTP 管理接口
    go func() {
        log.Printf("starting api server on %s", apiBind)
        if err := http.ListenAndServe(apiBind, apiServer.Handler()); err != nil {
            log.Printf("api server error: %v", err)
        }
    }()

    ctrl := controller.NewController(kc, nodeName, policyStore, forwardJumpPosition)

    // 变量说明：
    // - syncInterval: 控制器周期性同步间隔，单位为 time.Duration。默认 30s，可通过命令行参数 `-sync-interval` 覆盖。
    //   用途：控制调用 `Sync` 的频率，过于频繁会增加 API 调用和 iptables 操作负载，过于稀疏则策略更新延迟较大。
    // 简单的周期性同步循环：在每次定时触发时调用控制器的 Sync 方法。
    // 目的：保证节点上 iptables 的自定义链与当前 Deployment/Pod 状态一致，并记录同步日志。
    ticker := time.NewTicker(syncInterval)
    defer ticker.Stop()

    log.Printf("starting iptables-controller for node %s", nodeName)
    for {
        select {
        case <-ticker.C:
            if err := ctrl.Sync(ctx); err != nil {
                log.Printf("sync error: %v", err)
            }
        case <-ctx.Done():
            return
        }
    }
}
