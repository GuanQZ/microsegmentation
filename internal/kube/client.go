package kube

import (
    "flag"
    "os"

    "k8s.io/client-go/kubernetes"
    "k8s.io/client-go/rest"
    "k8s.io/client-go/tools/clientcmd"
)

// NewClient 返回一个 Kubernetes Clientset。优先使用 InClusterConfig，若在本地运行则回退到 KUBECONFIG。
// 详细说明：
// - 当程序在 Kubernetes 集群内作为 Pod 运行时，应使用 `rest.InClusterConfig()` 获取 ServiceAccount 的凭据和集群信息。
// - 为了方便本地开发与调试，若 InClusterConfig 失败，则尝试从环境变量 `KUBECONFIG` 指定的文件加载配置，
//   若未设置则使用默认的 `~/.kube/config` 路径（`clientcmd.RecommendedHomeFile`）。
// - 该函数返回一个 `kubernetes.Clientset`，供上层控制器调用 list/watch 等 API。
func NewClient() (*kubernetes.Clientset, error) {
    // 尝试使用集群内配置（Pod 内运行场景）
    config, err := rest.InClusterConfig()
    if err != nil {
        // 回退到 kubeconfig，以便本地调试
        // 环境变量说明：
        // - KUBECONFIG: 指定本地 kubeconfig 文件路径（可用于在开发机器上访问集群）。
        //   如果未设置，`clientcmd.RecommendedHomeFile`（通常为 ~/.kube/config）会被使用作为默认路径。
        // 注意：在容器内运行时通常不需要设置 KUBECONFIG，InClusterConfig 会使用 Pod 的 ServiceAccount。
        kubeconfig := os.Getenv("KUBECONFIG")
        if kubeconfig == "" {
            kubeconfig = clientcmd.RecommendedHomeFile
        }
        // 如果程序通过命令行传入 Kubernetes 相关标志（例如用于测试），`flag.Parse()` 可解析这些标志。
        flag.Parse()
        // 通过 kubeconfig 文件构建 client 配置
        config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
        if err != nil {
            return nil, err
        }
    }
    return kubernetes.NewForConfig(config)
}
