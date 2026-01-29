package controller

import (
    "log"
    "strconv"
    "strings"
)

// buildIngressRules 根据策略为指定 Deployment 生成“入向”规则。
// 规则逻辑（白名单）：
// - 未配置 ingressFrom：放行所有（ACCEPT）。
// - 配置 ingressFrom：仅允许来自指定 Deployment 的 Pod IP，其他来源丢弃（DROP）。
// - 兼容历史 rules：当 ingressFrom 为空且 rules 非空时，按旧规则生成。
func buildIngressRules(podIPs []string, policy *PolicyConfig, ns, name string, srcSetName string) [][]string {
    rules := [][]string{}
    depPolicy := findDeploymentPolicy(policy, ns, name)

    if depPolicy == nil {
        // 无策略 => 放行所有
        for _, ip := range podIPs {
            if strings.TrimSpace(ip) == "" {
                continue
            }
            rules = append(rules, []string{"-d", ip, "-j", "ACCEPT"})
        }
        return rules
    }

    // 若未配置 ingressFrom，但存在 legacy rules，则沿用旧规则
    if len(depPolicy.IngressFrom) == 0 && len(depPolicy.Rules) > 0 {
        return buildLegacyIngressRules(podIPs, policy, depPolicy, ns, name)
    }

    // 未配置 ingressFrom => 放行所有
    if len(depPolicy.IngressFrom) == 0 {
        for _, ip := range podIPs {
            if strings.TrimSpace(ip) == "" {
                continue
            }
            rules = append(rules, []string{"-d", ip, "-j", "ACCEPT"})
        }
        return rules
    }

    // 白名单：允许来源 -> ACCEPT（使用 ipset）
    for _, dstIP := range podIPs {
        if strings.TrimSpace(dstIP) == "" {
            continue
        }
        if strings.TrimSpace(srcSetName) != "" {
            rules = append(rules, []string{"-m", "set", "--match-set", srcSetName, "src", "-d", dstIP, "-j", "ACCEPT"})
        }
        // 未命中白名单的来源全部拒绝
        rules = append(rules, []string{"-d", dstIP, "-j", "DROP"})
    }

    return rules
}

// buildEgressRules 根据策略为指定 Deployment 生成“出向”规则。
// 规则逻辑（白名单）：
// - 未配置 egressTo：放行所有（RETURN）。
// - 配置 egressTo：仅允许访问指定 Deployment 的 Pod IP，其他去向丢弃（DROP）。
// 说明：出向链使用 RETURN 作为放行动作，以便继续进入入向链做校验。
func buildEgressRules(podIPs []string, ns, name string, dstSetName string) [][]string {
    rules := [][]string{}
    if strings.TrimSpace(dstSetName) == "" {
        // 无配置 => 放行所有
        for _, ip := range podIPs {
            if strings.TrimSpace(ip) == "" {
                continue
            }
            rules = append(rules, []string{"-s", ip, "-j", "RETURN"})
        }
        return rules
    }

    for _, srcIP := range podIPs {
        if strings.TrimSpace(srcIP) == "" {
            continue
        }
        rules = append(rules, []string{"-m", "set", "--match-set", dstSetName, "dst", "-s", srcIP, "-j", "RETURN"})
        // 未命中白名单的去向全部拒绝
        rules = append(rules, []string{"-s", srcIP, "-j", "DROP"})
    }
    return rules
}

// buildLegacyIngressRules 保持历史规则行为（基于 CIDR/端口）。
func buildLegacyIngressRules(podIPs []string, policy *PolicyConfig, depPolicy *DeploymentPolicy, ns, name string) [][]string {
    rules := [][]string{}
    for _, ip := range podIPs {
        if strings.TrimSpace(ip) == "" {
            continue
        }
        for _, r := range depPolicy.Rules {
            action := normalizeAction(r.Action)
            if action == "" {
                action = normalizeAction(policy.DefaultAction)
            }

            args := []string{"-d", ip}
            if strings.TrimSpace(r.SrcCIDR) != "" {
                args = append(args, "-s", r.SrcCIDR)
            }

            if proto := strings.TrimSpace(r.Protocol); proto != "" {
                args = append(args, "-p", strings.ToLower(proto))
                if r.Port > 0 {
                    args = append(args, "--dport", strconv.Itoa(int(r.Port)))
                }
            } else if r.Port > 0 {
                log.Printf("policy rule ignored port without protocol for %s/%s", ns, name)
            }

            args = append(args, "-j", action)
            rules = append(rules, args)
        }
    }
    return rules
}

// collectPeerIPs 将 DeploymentRef 列表展开为唯一的 Pod IP 列表。
func collectPeerIPs(refs []DeploymentRef, depPodIPsAll map[DeploymentKey][]string) []string {
    uniq := map[string]struct{}{}
    for _, ref := range refs {
        key := DeploymentKey{Namespace: ref.Namespace, Name: ref.Name}
        for _, ip := range depPodIPsAll[key] {
            if strings.TrimSpace(ip) == "" {
                continue
            }
            uniq[ip] = struct{}{}
        }
    }

    out := make([]string, 0, len(uniq))
    for ip := range uniq {
        out = append(out, ip)
    }
    return out
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
