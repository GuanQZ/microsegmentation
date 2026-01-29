package iptables

import (
    "bytes"
    "fmt"
    "log"
    "os/exec"
    "strings"
    "time"
)

// RunCommand 在宿主机中执行一个命令并返回 stdout 的文本内容或错误（包含 stderr）。
// 说明：所有对 iptables 的调用均通过该方法执行，以便统一处理 stderr 并在出错时返回详细信息。
func RunCommand(name string, args ...string) (string, error) {
    cmd := exec.Command(name, args...)
    var out bytes.Buffer
    var stderr bytes.Buffer
    cmd.Stdout = &out
    cmd.Stderr = &stderr
    if err := cmd.Run(); err != nil {
        return "", fmt.Errorf("%v: %s", err, stderr.String())
    }
    return strings.TrimSpace(out.String()), nil
}

// EnsureChain 确保给定的 iptables 链存在；若不存在则创建。
// 细节：
// - 使用 `iptables -w` 等待 xtables 锁，避免与其他进程（例如 Calico）并发冲突时失败。
// - 通过 `-L` 检查链是否存在，若不存在则使用 `-N` 创建。
// - 该方法只创建属于本程序管理的自定义链，不会删除或修改其他链以避免与 CNI 冲突。
func EnsureChain(chain string) error {
    // -w to wait for xtables lock
    _, err := RunCommand("iptables", "-w", "-n", "-L", chain)
    if err == nil {
        return nil
    }
    _, err = RunCommand("iptables", "-w", "-N", chain)
    if err != nil {
        return err
    }
    log.Printf("created chain %s", chain)
    return nil
}

// EnsureJump 确保在 FORWARD 链上存在一条跳转到 rootChain 的规则。
// 参数说明：
// - position: "append" 表示追加到链末尾；"insert" 表示插入到链首。
// 目的：让 iptables 在处理转发流量时进入我们的自定义链，从而实现基于 Pod IP 的策略控制。
// 说明：
// - 追加（append）对 CNI 影响最小，但若 CNI 在前面已 ACCEPT，可能导致规则不生效。
// - 插入（insert）优先生效，但可能影响 CNI 规则优先级。
func EnsureJump(rootChain, position string) error {
    // 如果希望插入到链首，则先删除已有跳转（若存在）再插入，确保优先生效
    if position == "insert" {
        // 尝试删除已有跳转（忽略错误）
        _, _ = RunCommand("iptables", "-w", "-D", "FORWARD", "-j", rootChain)
        _, err := RunCommand("iptables", "-w", "-I", "FORWARD", "1", "-j", rootChain)
        return err
    }

    // 追加模式：若已存在则不重复添加
    _, err := RunCommand("iptables", "-w", "-C", "FORWARD", "-j", rootChain)
    if err == nil {
        return nil
    }
    _, err = RunCommand("iptables", "-w", "-A", "FORWARD", "-j", rootChain)
    return err
}

// SyncRules 用给定的规则集合替换指定链的内容。
// 参数：
// - chain: 目标链名
// - rules: 每一条规则为一个字符串切片，表示追加到链时的参数（不包含 -A chain 部分），例如 {"-s", "10.0.0.5", "-j", "ACCEPT"}
// 行为：
// - 先 `-F` 清空链（仅清空链本身，不删除跳转规则）。
// - 逐条 `-A` 添加规则。
// - 完成后通过日志记录同步时间，以便审计和排查。
// 返回值：changed 恒返回 true（目前每次直接替换）；如需差分更新可在后续实现中加入比较逻辑。
func SyncRules(chain string, rules [][]string) (changed bool, err error) {
    // flush chain
    if _, err := RunCommand("iptables", "-w", "-F", chain); err != nil {
        return false, err
    }

    for _, r := range rules {
        args := append([]string{"-A", chain}, r...)
        _, err := RunCommand("iptables", append([]string{"-w"}, args...)...)
        if err != nil {
            return false, err
        }
    }

    // 记录规则变更时间，用以审计和排查
    log.Printf("rules synced for chain %s at %s", chain, time.Now().Format(time.RFC3339))
    return true, nil
}

// MakeChainName 根据前缀、命名空间和名称生成合法的 iptables 链名。
// 说明：
// - iptables 链名长度通常受限（不同内核/iptables 版本略有差异，常见限制约为 28），因此这里对生成的链名做截断以保证兼容性。
// - 将非法字符（如 '/'、':'）替换为 '-'，并返回大写字符串以便可读性和一致性。
func MakeChainName(prefix, ns, name string) string {
    base := fmt.Sprintf("%s-%s-%s", prefix, ns, name)
    // iptables chain max length is usually 28; keep shortened
    if len(base) > 26 {
        base = base[:26]
    }
    // replace invalid chars
    base = strings.ReplaceAll(base, "/", "-")
    base = strings.ReplaceAll(base, ":", "-")
    return strings.ToUpper(base)
}

/* 关键常量与系统变量说明：
 - iptables 二进制：程序通过执行系统命令 `iptables` 来应用规则，容器镜像需包含该二进制并以具备操作主机网络命名空间的方式运行（例如 hostNetwork 或 NET_ADMIN 特权）。
 - xtables 锁（-w 标志）：当多个进程同时操作 iptables 时会发生锁竞争，`-w` 命令选项会在获取锁失败时等待，减少并发失败风险。
 - 常用 iptables 参数说明：
     - `-C <chain> <rule>`: 检查规则是否已存在（返回 0 表示存在，非 0 表示不存在或错误）。
     - `-I <chain> <pos> <rule>`: 在指定位置插入规则（常用于将跳转插入到链的第一条，保证较高优先级）。
     - `-A <chain> <rule>`: 在链末尾追加规则。
     - `-F <chain>`: 清空链中所有规则（不删除链本身）。
     - `-N <chain>`: 新建链。
 - 链名长度限制：iptables 链名在不同内核/iptables 版本中存在长度限制（常见约 28 字符），因此 `MakeChainName` 对生成的名称做了截断以保证兼容性。
 - 权限要求：执行 iptables 修改通常需要 root 权限或具备 `NET_ADMIN` 能力的进程。
 - Pod IP 变量：代码使用 `Pod.Status.PodIP` 作为规则中的 IP，需注意该字段在 Pod 尚未分配 IP 或尚未就绪时可能为空字符串，逻辑中会跳过空 IP。
*/
