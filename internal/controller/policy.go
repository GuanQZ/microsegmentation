package controller

import (
    "encoding/json"
    "os"
    "strings"
    "sync"
)

// PolicyConfig 表示外部管理端通过 HTTP API 下发的策略配置。
// 说明：
// - DefaultAction: 当某个 Deployment 未匹配到规则时的默认动作（建议: ALLOW 或 RETURN）。
// - Deployments: 针对每个 Deployment 的规则列表。
// 该结构用于反序列化管理端提交的 JSON 配置。
type PolicyConfig struct {
    DefaultAction string             `json:"defaultAction"`
    Deployments   []DeploymentPolicy `json:"deployments"`
}

// DeploymentPolicy 表示单个 Deployment 的访问控制策略。
// 变量说明：
// - Namespace / Name: 指定目标 Deployment 的命名空间与名称。
// - Rules: 该 Deployment 的规则列表。
type DeploymentPolicy struct {
    Namespace string `json:"namespace"`
    Name      string `json:"name"`
    // IngressFrom: 允许访问该 Deployment 的来源 Deployment 列表（白名单）。
    // 若为空，表示不限制来源（放行所有）。
    IngressFrom []DeploymentRef `json:"ingressFrom"`
    // EgressTo: 该 Deployment 允许访问的目标 Deployment 列表（白名单）。
    // 若为空，表示不限制去向（放行所有）。
    EgressTo   []DeploymentRef `json:"egressTo"`
    // Rules: 兼容历史策略（基于 CIDR/端口）。当 ingressFrom 未配置时仍可使用。
    Rules      []Rule          `json:"rules"`
}

// DeploymentRef 表示一个 Deployment 引用（命名空间 + 名称）。
// 用于白名单关联关系配置（谁能访问我 / 我能访问谁）。
type DeploymentRef struct {
    Namespace string `json:"namespace"`
    Name      string `json:"name"`
}

// Rule 表示一条访问控制规则。
// 变量说明：
// - Action: 动作，允许值示例：ALLOW/ACCEPT、DENY/DROP、REJECT、RETURN。
// - SrcCIDR: 源地址 CIDR，例如 "10.0.0.0/24"。为空时表示不限制来源。
// - Protocol: 协议，如 "tcp" / "udp" / "icmp"，为空时表示不限制协议。
// - Port: 目的端口，仅当 Protocol 为 tcp/udp 时有效；为 0 表示不限制端口。
type Rule struct {
    Action   string `json:"action"`
    SrcCIDR  string `json:"srcCIDR"`
    Protocol string `json:"protocol"`
    Port     int32  `json:"port"`
}

// PolicyStore 保存当前生效的策略（内存），可选地持久化到本地文件。
// 变量说明：
// - policy: 当前策略
// - filePath: 可选的本地文件路径，用于程序重启后恢复策略（为空则不落盘）
// - mu: 读写锁，保证并发访问安全
type PolicyStore struct {
    mu       sync.RWMutex
    policy   PolicyConfig
    filePath string
}

// NewPolicyStore 创建并返回 PolicyStore。
// 说明：若 filePath 非空，会尝试从该文件读取策略；若读取失败则使用默认策略。
func NewPolicyStore(filePath string) *PolicyStore {
    ps := &PolicyStore{filePath: filePath}
    ps.policy = PolicyConfig{DefaultAction: "ALLOW", Deployments: []DeploymentPolicy{}}
    if strings.TrimSpace(filePath) != "" {
        if raw, err := os.ReadFile(filePath); err == nil {
            var cfg PolicyConfig
            if json.Unmarshal(raw, &cfg) == nil && strings.TrimSpace(cfg.DefaultAction) != "" {
                ps.policy = cfg
            }
        }
    }
    return ps
}

// Get 返回当前策略的副本。
func (s *PolicyStore) Get() PolicyConfig {
    s.mu.RLock()
    defer s.mu.RUnlock()
    return s.policy
}

// Set 替换当前策略，并在可配置时落盘。
func (s *PolicyStore) Set(cfg PolicyConfig) error {
    if strings.TrimSpace(cfg.DefaultAction) == "" {
        cfg.DefaultAction = "ALLOW"
    }
    s.mu.Lock()
    s.policy = cfg
    s.mu.Unlock()

    if strings.TrimSpace(s.filePath) == "" {
        return nil
    }

    data, err := json.MarshalIndent(cfg, "", "  ")
    if err != nil {
        return err
    }
    return os.WriteFile(s.filePath, data, 0o600)
}
