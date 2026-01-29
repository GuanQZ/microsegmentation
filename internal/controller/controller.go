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

    // 列出全量 Pods（用于构建跨节点来源/去向白名单）
    podList, err := c.client.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
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

    // 遍历 Pods，判断其匹配哪些 Deployment，并分别收集：
    // - 全量 Pod IP（用于跨节点白名单匹配）
    // - 本节点 Pod IP（用于本节点链规则）
    depPodIPsAll := map[DeploymentKey][]string{}
    depPodIPsLocal := map[DeploymentKey][]string{}
    for _, p := range podList.Items {
        for key, sel := range depSelectors {
            if sel.Matches(labels.Set(p.Labels)) {
                depPodIPsAll[key] = append(depPodIPsAll[key], p.Status.PodIP)
                if p.Spec.NodeName == c.nodeName {
                    depPodIPsLocal[key] = append(depPodIPsLocal[key], p.Status.PodIP)
                }
            }
        }
    }

    // 从内存策略存储读取当前策略（由 API 下发）
    policy := c.policyStore.Get()

    // 确保入向/出向根链存在并在 FORWARD 链插入跳转点
    rootChainIn := iptables.MakeChainName(c.prefix, "ROOT", "IN")
    rootChainOut := iptables.MakeChainName(c.prefix, "ROOT", "OUT")
    if err := iptables.EnsureChain(rootChainOut); err != nil {
        return fmt.Errorf("ensure root out chain: %w", err)
    }
    if err := iptables.EnsureChain(rootChainIn); err != nil {
        return fmt.Errorf("ensure root in chain: %w", err)
    }
    // 顺序：先出向（OUT）再入向（IN），保证先进行出向控制，再做入向控制
    // insert 情况下需要先插入 IN 再插入 OUT，才能保证 OUT 在更靠前的位置。
    if c.forwardJumpPosition == "insert" {
        if err := iptables.EnsureJump(rootChainIn, c.forwardJumpPosition); err != nil {
            return fmt.Errorf("ensure jump in: %w", err)
        }
        if err := iptables.EnsureJump(rootChainOut, c.forwardJumpPosition); err != nil {
            return fmt.Errorf("ensure jump out: %w", err)
        }
    } else {
        if err := iptables.EnsureJump(rootChainOut, c.forwardJumpPosition); err != nil {
            return fmt.Errorf("ensure jump out: %w", err)
        }
        if err := iptables.EnsureJump(rootChainIn, c.forwardJumpPosition); err != nil {
            return fmt.Errorf("ensure jump in: %w", err)
        }
    }

    // 收集所有需要挂接到 rootChain 的专用链名
    desiredChainsIn := []string{}
    desiredChainsOut := []string{}

    // 对于每个在本节点运行的 Deployment，创建/更新入向/出向专用链
    for depKey, localIPs := range depPodIPsLocal {
        if len(localIPs) == 0 {
            continue
        }
        // 使用结构化字段，避免字符串解析误差
        ns, name := depKey.Namespace, depKey.Name
        chainIn := iptables.MakeChainName(c.prefix, "IN", ns+"-"+name)
        chainOut := iptables.MakeChainName(c.prefix, "OUT", ns+"-"+name)
        desiredChainsIn = append(desiredChainsIn, chainIn)
        desiredChainsOut = append(desiredChainsOut, chainOut)
        if err := iptables.EnsureChain(chainIn); err != nil {
            log.Printf("ensure chain %s: %v", chainIn, err)
            continue
        }
        if err := iptables.EnsureChain(chainOut); err != nil {
            log.Printf("ensure chain %s: %v", chainOut, err)
            continue
        }

        depPolicy := findDeploymentPolicy(&policy, ns, name)
        srcSetName := ""
        dstSetName := ""
        if depPolicy != nil && len(depPolicy.IngressFrom) > 0 {
            srcSetName = iptables.MakeSetName(c.prefix, "SRC", ns+"-"+name)
            allowedSrcIPs := collectPeerIPs(depPolicy.IngressFrom, depPodIPsAll)
            if err := iptables.SyncIPSet(srcSetName, allowedSrcIPs); err != nil {
                log.Printf("sync ipset %s: %v", srcSetName, err)
            }
        }
        if depPolicy != nil && len(depPolicy.EgressTo) > 0 {
            dstSetName = iptables.MakeSetName(c.prefix, "DST", ns+"-"+name)
            allowedDstIPs := collectPeerIPs(depPolicy.EgressTo, depPodIPsAll)
            if err := iptables.SyncIPSet(dstSetName, allowedDstIPs); err != nil {
                log.Printf("sync ipset %s: %v", dstSetName, err)
            }
        }

        ingressRules := buildIngressRules(localIPs, &policy, ns, name, srcSetName)
        if _, err := iptables.SyncRules(chainIn, ingressRules); err != nil {
            log.Printf("sync rules for %s: %v", chainIn, err)
            continue
        }

        egressRules := buildEgressRules(localIPs, ns, name, dstSetName)
        if _, err := iptables.SyncRules(chainOut, egressRules); err != nil {
            log.Printf("sync rules for %s: %v", chainOut, err)
            continue
        }
    }

    // 用最新的专用链列表重建 rootChain，避免历史残留链导致策略失效
    sort.Strings(desiredChainsIn)
    sort.Strings(desiredChainsOut)
    rootRulesIn := [][]string{}
    rootRulesOut := [][]string{}

    // 放行已建立/相关连接的返回流量，避免白名单误拦截回包
    establishedRule := []string{"-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT"}
    rootRulesOut = append(rootRulesOut, establishedRule)
    rootRulesIn = append(rootRulesIn, establishedRule)
    for _, chain := range desiredChainsIn {
        rootRulesIn = append(rootRulesIn, []string{"-j", chain})
    }
    for _, chain := range desiredChainsOut {
        rootRulesOut = append(rootRulesOut, []string{"-j", chain})
    }
    if _, err := iptables.SyncRules(rootChainIn, rootRulesIn); err != nil {
        log.Printf("sync rules for %s: %v", rootChainIn, err)
    }
    if _, err := iptables.SyncRules(rootChainOut, rootRulesOut); err != nil {
        log.Printf("sync rules for %s: %v", rootChainOut, err)
    }

    log.Printf("sync completed for node %s", c.nodeName)
    return nil
}
