# npd-node-replace v1 vs v2 对比

## 概览

| 维度 | v1 | v2 |
|------|----|----|
| 容忍策略配置 | ConfigMap（tolerance.json） | ToleranceConfig 自定义资源（Cluster scope） |
| 触发机制 | 事件次数阈值（N 次触发） | 积分桶模型（加权分数累加） |
| 节点差异化 | 不支持，所有节点共用一套配置 | 通过 node label 匹配，每类节点独立配置 |
| 白名单机制 | 节点标签 `npd-node-replace-enabled=true` | ToleranceConfig 中 `allowOperation` 字段 |
| Escalation | 不支持 | 支持，cooldown 窗口内再次触发自动升级操作 |
| 事件时间窗口 | 固定 `timewindowinminutes` 按事件类型配置 | 统一 `eventWindowInMinutes` + `lastActionTime` 下界过滤 |
| 节点类型过滤 | 仅过滤 Karpenter 节点 | 过滤 Karpenter + Fargate 节点 |
| Reboot 后处理 | 立即 uncordon + 删除 NIR | 等待 grace period + 节点恢复 Ready 后 uncordon，保留 NIR 用于 escalation |
| NIR 生命周期 | 操作完成后立即删除 | reboot/paging 后保留，cooldown 过期后定时清理 |
| 并发控制 | 不支持 | `maxConcurrentActions` 限制同规则下并发操作数 |
| Dry-run | 不支持 | `dryRun: true` 走完判定逻辑但不执行操作 |
| 可观测性 | 仅日志 | Prometheus Metrics（积分桶水位、操作计数、事件计数、活跃 NIR 数） |
| 通知 | SNS，统一主题 | SNS，按场景区分主题（reboot/replace/paging/escalation/dry-run/cooldown-expired 等） |
| Leader Election | 不支持 | 支持，通过 Lease 实现 |
| 配置热更新 | 需要重启 Pod | ToleranceConfig CR 变更后自动生效 |

## 容忍策略对比

### v1：基于次数的简单阈值

```json
{
  "tolerancecollection": {
    "OOMKilling": {
      "times": 2,
      "action": "reboot",
      "timewindowinminutes": 30
    }
  }
}
```

含义：30 分钟内发生 2 次 OOMKilling → reboot。所有节点共用这一套配置。

局限：
- 不同事件类型无法加权（1 次 KernelOops 和 1 次 OOMKilling 被同等对待）
- 无法为不同节点类型设置不同策略
- 无 escalation，reboot 后如果问题持续，只能再次 reboot

### v2：基于积分桶的加权模型

```yaml
apiVersion: nodeissuereporter.xingzhan.io/v1alpha1
kind: ToleranceConfig
metadata:
  name: cluster-tolerance-policy
spec:
  configs:
    - nodeLabel: "eks.amazonaws.com/nodegroup=gpu-nodes"
      bucketSize: 100
      action: reboot
      allowOperation: true
      cooldownTimeInMinutes: 30
      eventWindowInMinutes: 60
      escalateOperation: replace
      maxConcurrentActions: 1
      eventScores:
        - eventName: OOMKilling
          score: 30
        - eventName: KernelOops
          score: 50
        - eventName: ReadonlyFilesystem
          score: 100
```

含义：
- 仅匹配 `eks.amazonaws.com/nodegroup=gpu-nodes` 标签的节点
- 60 分钟滑动窗口内，OOMKilling 计 30 分，KernelOops 计 50 分，ReadonlyFilesystem 直接 100 分（一次就触发）
- 累计达到 100 分 → reboot
- reboot 后 30 分钟内如果再次打满 → escalate 到 replace
- 同时最多处理 1 个 GPU 节点

## 白名单机制对比

### v1
需要在每个节点上手动打标签：
```bash
kubectl label nodes <node-name> npd-node-replace-enabled=true
```
没有标签的节点只监控不操作。

### v2
在 ToleranceConfig 中通过 `allowOperation` 字段控制：
- `allowOperation: true` → 允许执行操作
- `allowOperation: false` → 仅通知管理员

未匹配到任何 ToleranceConfig 规则的节点完全不被处理（不创建 NIR、不计分、不通知）。

## NodeIssueReport 生命周期对比

### v1
```
事件触发 → 创建/更新 NIR → 次数达到阈值 → 执行 action → 删除 NIR
```
操作完成后 NIR 立即被删除，无法评估后续是否需要 escalation。

### v2
```
事件触发 → 创建/更新 NIR（累加分数）→ 积分桶打满 → 执行 action
  → reboot/paging 完成 → 重置 NIR（Phase=None, Action=None, 保留 lastActionTime）
    → cooldown 内再次打满 → escalation
    → cooldown 过期无新 action → 定时清理删除 NIR
  → replace 完成 → 删除 NIR（终态）
```

## Reboot 安全性对比

### v1
```
drain → RebootInstances API 返回 → 立即 uncordon → 删除 NIR
```
问题：EC2 reboot 是异步的，API 返回时节点还没真正重启。立即 uncordon 后节点突然断开，pod 变成 ContainerStatusUnknown。

### v2
```
drain → RebootInstances API 返回 → Phase=PhaseRebooted
  → 等待 2 分钟 grace period（确保 reboot 生效）
  → 等待节点恢复 Ready
  → uncordon → 重置 NIR
```

## 通知对比

### v1
两种 SNS 主题：
- 执行了操作："Your node has been REBOOTED/REPLACED"
- 未执行操作："Your node has issues detected, but we do not do any action on it"

### v2
按场景细分：

| 场景 | 邮件主题 |
|------|---------|
| reboot | `[npd-node-replace] Node REBOOTED due to persistent issues` |
| replace | `[npd-node-replace] Node REPLACED due to persistent issues` |
| paging | `[npd-node-replace] Node issues detected - admin notification (paging)` |
| allowOperation=false | `[npd-node-replace] Node issues detected - auto-action disabled, notify only` |
| cooldown 过期 | `[npd-node-replace] Node issue report cleanup - cooldown expired, no escalation triggered` |
| escalation paging | `[npd-node-replace] ESCALATION - repeated issues after action, admin notification` |
| 并发限制 | `[npd-node-replace] Action DELAYED - max concurrent actions reached, waiting for capacity` |
| dry-run | `[npd-node-replace] DRY-RUN: would execute reboot, but dry-run mode is enabled` |

## 升级注意事项

1. v2 的 ToleranceConfig 是全新的 CRD，需要先 apply CRD 定义
2. v2 的 NodeIssueReport CRD 新增了 `scoreInBucket`、`lastActionTime`、`lastUpdateTime`、`escalated` 字段，需要更新 CRD
3. v2 的 ClusterRole 需要新增 `toleranceconfigs` 资源的 get/list/watch 权限
4. v1 的 tolerance ConfigMap 和节点标签 `npd-node-replace-enabled=true` 在 v2 中不再使用
5. 如果使用 Helm，v2 chart 名称为 `npd-node-replace-v2`，与 v1 的 `npd-node-replace` 互不影响，可以先部署 v2 测试，再卸载 v1
