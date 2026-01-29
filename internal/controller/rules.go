package controller

import (
    "log"
    "strconv"
    "strings"
)

// buildRulesForDeployment 根据策略配置为指定 Deployment 生成 iptables 规则。
// 规则生成逻辑：
// - 针对每个 Pod IP 生成规则，使用 "-d <PodIP>" 限定目的地址。
// - 每条策略规则可额外指定源 CIDR（-s）、协议（-p）与端口（--dport）。
// - 若该 Deployment 无匹配规则，则使用全局 DefaultAction 作为兜底动作。
// 注意：该方法仅生成规则参数（不包含 "-A <chain>"），由上层 Sync 负责写入链。
func buildRulesForDeployment(podIPs []string, policy *PolicyConfig, ns, name string) [][]string {
    rules := [][]string{}

    // 查找对应 Deployment 的策略
    depPolicy := findDeploymentPolicy(policy, ns, name)

    // 对每个 Pod IP 生成规则
    for _, ip := range podIPs {
        if strings.TrimSpace(ip) == "" {
            continue
        }

        if depPolicy == nil || len(depPolicy.Rules) == 0 {
            // 没有专用规则，使用默认动作
            action := normalizeAction(policy.DefaultAction)
            rules = append(rules, []string{"-d", ip, "-j", action})
            continue
        }

        for _, r := range depPolicy.Rules {
            action := normalizeAction(r.Action)
            if action == "" {
                action = normalizeAction(policy.DefaultAction)
            }

            // 基础规则：目标为 Pod IP
            args := []string{"-d", ip}

            // 源 CIDR
            if strings.TrimSpace(r.SrcCIDR) != "" {
                args = append(args, "-s", r.SrcCIDR)
            }

            // 协议 + 端口
            if proto := strings.TrimSpace(r.Protocol); proto != "" {
                args = append(args, "-p", strings.ToLower(proto))
                if r.Port > 0 {
                    args = append(args, "--dport", strconv.Itoa(int(r.Port)))
                }
            } else if r.Port > 0 {
                // 没有协议却指定端口时给出提示
                log.Printf("policy rule ignored port without protocol for %s/%s", ns, name)
            }

            args = append(args, "-j", action)
            rules = append(rules, args)
        }
    }

    return rules
}

// findDeploymentPolicy 查找匹配命名空间与名称的 DeploymentPolicy。
func findDeploymentPolicy(policy *PolicyConfig, ns, name string) *DeploymentPolicy {
    if policy == nil {
        return nil
    }
    for i := range policy.Deployments {
        if policy.Deployments[i].Namespace == ns && policy.Deployments[i].Name == name {
            return &policy.Deployments[i]
        }
    }
    return nil
}

// normalizeAction 将动作归一化为 iptables 可接受的目标名。
// 支持：ALLOW/ACCEPT、DENY/DROP、REJECT、RETURN。
func normalizeAction(action string) string {
    a := strings.ToUpper(strings.TrimSpace(action))
    switch a {
    case "ALLOW":
        return "ACCEPT"
    case "ACCEPT":
        return "ACCEPT"
    case "DENY":
        return "DROP"
    case "DROP":
        return "DROP"
    case "REJECT":
        return "REJECT"
    case "RETURN":
        return "RETURN"
    default:
        return ""
    }
}
