package controller

import (
    "context"
    "fmt"
    "log"
    "sort"

    "github.com/example/iptables-controller/internal/iptables"
    "k8s.io/apimachinery/pkg/labels"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/client-go/kubernetes"
)

// Controller 是核心结构，负责将 Kubernetes 的 Deployment/Pod 状态映射为本节点的 iptables 规则。
// 字段说明：
// - client: Kubernetes API client
// - nodeName: 控制器运行所在节点名（用于筛选仅属于本节点的 Pods）
// - prefix: 生成链名时使用的前缀，以便区分系统内其它链（例如 Calico 的链）
type Controller struct {
    client   *kubernetes.Clientset
    nodeName string
    prefix   string
    // policyStore: 策略存储，来自 API 下发（内存/可选文件持久化）
    policyStore *PolicyStore
    // forwardJumpPosition: FORWARD 链跳转插入方式（append/insert）
    forwardJumpPosition string
}

// DeploymentKey 用于标识一个 Deployment（命名空间 + 名称）。
// 说明：使用结构体避免对 "namespace/name" 字符串进行解析，减少匹配错误。
type DeploymentKey struct {
    Namespace string
    Name      string
}

// NewController 创建并返回一个 Controller 实例。
// 说明：
// - 默认使用前缀 "MS" 来标识本程序管理的链名；可在创建后扩展配置以使用其它前缀。
// - policyStore 来自程序内置的管理 API，用于存放外部下发的策略。
func NewController(client *kubernetes.Clientset, nodeName string, policyStore *PolicyStore, forwardJumpPosition string) *Controller {
    if forwardJumpPosition == "" {
        forwardJumpPosition = "insert"
    }
    return &Controller{
        client:      client,
        nodeName:    nodeName,
        prefix:      "MS",
        policyStore: policyStore,
        forwardJumpPosition: forwardJumpPosition,
    }
}

// Sync 执行一次同步操作，将集群中的 Deployment 与本节点上的 Pod 进行关联，并确保相应的 iptables 链与规则被正确创建或更新。
// 主要步骤：
// 1. 列出集群中所有 Deployment；将每个 Deployment 的 LabelSelector 转换为 Selector。
// 2. 列出本节点上的 Pod（通过 fieldSelector 指定 `spec.nodeName`）。
// 3. 对于本节点上的每个 Pod，匹配属于哪个 Deployment（使用 LabelSelector），收集每个 Deployment 在本节点上的 Pod IP 列表。
// 4. 确保存在一个根链（rootChain），并在 `FORWARD` 链上插入跳转到该根链的规则（通过 `EnsureChain` 与 `EnsureJump`）。
// 5. 为每个有 Pod 在本节点运行的 Deployment 创建或更新一个独立链（链名由 `MakeChainName` 生成），并在该链中 `ACCEPT` 对应 Pod IP 的流量。
// 6. 在 rootChain 中添加对每个 Deployment 专用链的跳转（如果尚未存在）。
// 设计要点：
// - 通过独立命名的自定义链避免直接改动 CNI（如 Calico）创建的链；只插入跳转并管理自有链的内容。
// - 目前的策略为基于 Pod 源 IP 的简单允许（ACCEPT）示例；实际环境可扩展为白名单/黑名单/端口/方向等更复杂策略。
func (c *Controller) Sync(ctx context.Context) error {
    // 列出所有命名空间的 Deployments
    deps, err := c.client.AppsV1().Deployments("").List(ctx, metav1.ListOptions{})
    if err != nil {
        return fmt.Errorf("list deployments: %w", err)
    }

    // 列出本节点上的 Pods（通过 fieldSelector 过滤）
    podList, err := c.client.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + c.nodeName})
    if err != nil {
        return fmt.Errorf("list pods: %w", err)
    }

    // 将每个 Deployment 的 LabelSelector 转换为 Selector，并记录到映射中： key = "namespace/name"
    depSelectors := map[DeploymentKey]labels.Selector{}
    for _, d := range deps.Items {
        sel, err := metav1.LabelSelectorAsSelector(d.Spec.Selector)
        if err != nil {
            log.Printf("invalid selector for deployment %s/%s: %v", d.Namespace, d.Name, err)
            continue
        }
        key := DeploymentKey{Namespace: d.Namespace, Name: d.Name}
        depSelectors[key] = sel
    }

    // 遍历本节点上的 Pod，判断其匹配哪些 Deployment，并收集每个 Deployment 在本节点上的 Pod IP
    depPodIPs := map[DeploymentKey][]string{}
    for _, p := range podList.Items {
        for key, sel := range depSelectors {
            if sel.Matches(labels.Set(p.Labels)) {
                depPodIPs[key] = append(depPodIPs[key], p.Status.PodIP)
            }
        }
    }

    // 从内存策略存储读取当前策略（由 API 下发）
    policy := c.policyStore.Get()

    // 确保根链存在并在 FORWARD 链插入跳转点
    rootChain := iptables.MakeChainName(c.prefix, "ROOT", "CHAIN")
    if err := iptables.EnsureChain(rootChain); err != nil {
        return fmt.Errorf("ensure root chain: %w", err)
    }
    if err := iptables.EnsureJump(rootChain, c.forwardJumpPosition); err != nil {
        return fmt.Errorf("ensure jump: %w", err)
    }

    // 收集所有需要挂接到 rootChain 的专用链名
    desiredChains := []string{}

    // 对于每个在本节点运行的 Deployment，创建/更新专用链并添加允许该 Deployment Pod IP 的规则
    for depKey, ips := range depPodIPs {
        if len(ips) == 0 {
            continue
        }
        // 使用结构化字段，避免字符串解析误差
        ns, name := depKey.Namespace, depKey.Name
        chain := iptables.MakeChainName(c.prefix, ns, name)
        desiredChains = append(desiredChains, chain)
        if err := iptables.EnsureChain(chain); err != nil {
            log.Printf("ensure chain %s: %v", chain, err)
            continue
        }

        // 在专用链中根据外部策略生成规则：
        // - 默认将规则作用于目的地址（-d PodIP），以实现对该 Deployment 的访问控制。
        // - 规则来源于 ConfigMap 中该 Deployment 的策略；若没有规则则应用 defaultAction。
        rules := buildRulesForDeployment(ips, &policy, ns, name)

        if _, err := iptables.SyncRules(chain, rules); err != nil {
            log.Printf("sync rules for %s: %v", chain, err)
            continue
        }
    }

    // 用最新的专用链列表重建 rootChain，避免历史残留链导致策略失效
    sort.Strings(desiredChains)
    rootRules := [][]string{}
    for _, chain := range desiredChains {
        rootRules = append(rootRules, []string{"-j", chain})
    }
    if _, err := iptables.SyncRules(rootChain, rootRules); err != nil {
        log.Printf("sync rules for %s: %v", rootChain, err)
    }

    log.Printf("sync completed for node %s", c.nodeName)
    return nil
}
