# npd-node-replace v2 测试方案

## 测试环境要求

- Amazon EKS 集群（中国区 cn-north-1 或 cn-northwest-1）
- 至少 2 个 ASG 托管节点组的节点（用于 reboot/replace 测试）
- 至少 1 个 Karpenter 管理的节点（用于过滤测试）
- 至少 1 个 Fargate pod（用于过滤测试）
- node-problem-detector 已部署
- npd-node-replace v2 部署在 Fargate 上
- SNS Topic 已配置并订阅邮件
- ToleranceConfig CRD 和 NodeIssueReport CRD 已部署
- 可通过 SSH 登录到目标节点执行内核日志注入

## 事件注入方法

OOMKilling：
```bash
echo "Killed process 1234 (myapp) total-vm:102400kB, anon-rss:51200kB, file-rss:2048kB" | sudo tee /dev/kmsg
```

KernelOops：
```bash
echo "<1>BUG: unable to handle kernel NULL pointer dereference at 0x00000000" | sudo tee /dev/kmsg
```

ReadonlyFilesystem：
```bash
echo "EXT4-fs (sda1): Remounting filesystem read-only" | sudo tee /dev/kmsg
```

NTPProblem（需要 ntp-custom-plugin-monitor）：
```bash
sudo systemctl stop chronyd  # 或 ntpd
```

---

## 一、事件采集与过滤

### TC-1.1 NPD 事件正常采集

**前置条件**：目标节点属于 ASG 托管节点组，有匹配的 ToleranceConfig

**步骤**：
1. SSH 到目标节点，注入一次 OOMKilling 事件
2. 观察 npd-node-replace 日志

**预期结果**：
- EventController 日志显示 `recieved whole events` 并包含事件 JSON
- 创建或更新了对应节点的 NodeIssueReport 资源
- `kubectl get nodeissuereport -o yaml` 中 `nodeproblems.OOMKilling.messages` 包含该事件

### TC-1.2 Karpenter 节点事件过滤

**前置条件**：集群中有 Karpenter 管理的节点

**步骤**：
1. SSH 到 Karpenter 节点，注入 OOMKilling 事件
2. 观察日志

**预期结果**：
- 日志显示 `node xxx is handled by karpenter, thus ignore this event`
- 不会创建该节点的 NodeIssueReport

### TC-1.3 Fargate 节点事件过滤

**前置条件**：集群中有 Fargate 节点

**步骤**：
1. 观察日志中是否有 Fargate 节点的事件

**预期结果**：
- 日志显示 `node xxx is a fargate node, thus ignore this event`

### TC-1.4 控制器启动前事件过滤

**步骤**：
1. 重启 npd-node-replace pod
2. 观察是否处理了重启前已存在的事件

**预期结果**：
- 不处理 `controllerStartTime` 之前的事件

### TC-1.5 无匹配 ToleranceConfig 的节点

**前置条件**：目标节点的 label 不匹配任何 ToleranceConfig entry

**步骤**：
1. 注入 OOMKilling 事件

**预期结果**：
- EventController 创建 NIR，但 ScoreInBucket 为 0（无匹配规则，score 查不到）
- NIRController 日志显示 `no matching ToleranceConfig found for node, skip problem evaluation`

---

## 二、积分桶机制

### TC-2.1 分数正常累加

**前置条件**：
```yaml
- nodeLabel: "eks.amazonaws.com/nodegroup=test-ng"
  bucketSize: 80
  action: reboot
  allowOperation: true
  eventWindowInMinutes: 60
  eventScores:
    - eventName: OOMKilling
      score: 20
```

**步骤**：
1. 注入 1 次 OOMKilling
2. 检查 NIR 的 `scoreInBucket`

**预期结果**：`scoreInBucket = 20`

### TC-2.2 积分桶打满触发 action

**步骤**：
1. 连续注入 4 次 OOMKilling（4 × 20 = 80 >= bucketSize 80）
2. 观察日志和 NIR 状态

**预期结果**：
- 日志显示 `score bucket overflow for node xxx: 80 / 80, triggering action: reboot`
- NIR 的 `action` 变为 `reboot`，`scoreInBucket` 清零，`lastActionTime` 被设置

### TC-2.3 不同事件类型加权

**前置条件**：
```yaml
eventScores:
  - eventName: OOMKilling
    score: 20
  - eventName: KernelOops
    score: 40
```

**步骤**：
1. 注入 1 次 OOMKilling（score=20）
2. 注入 1 次 KernelOops（score=40）
3. 检查 NIR

**预期结果**：`scoreInBucket = 60`

### TC-2.4 事件时间窗口过期

**前置条件**：`eventWindowInMinutes: 2`（设置较短窗口便于测试）

**步骤**：
1. 注入 2 次 OOMKilling（score=40）
2. 等待 3 分钟
3. 注入 1 次 OOMKilling
4. 检查 NIR

**预期结果**：
- 前 2 次事件已过期，`scoreInBucket` 仅为 20（只有第 3 次的分数）

### TC-2.5 lastActionTime 下界过滤

**步骤**：
1. 触发 reboot（积分桶打满）
2. reboot 完成后，NIR 重置为 PhaseNone
3. 检查 `lastActionTime` 已设置
4. 注入新事件
5. 检查 `scoreInBucket`

**预期结果**：
- 只有 `lastActionTime` 之后的事件被计分
- reboot 之前的事件不会被重复计分

### TC-2.6 未配置 score 的事件类型

**步骤**：
1. 注入一个 ToleranceConfig 中未配置 score 的事件类型（如 TaskHung）

**预期结果**：
- 事件被记录到 NIR 的 `nodeproblems` 中
- 但 `scoreInBucket` 不增加

---

## 三、Reboot 操作

### TC-3.1 正常 reboot 流程

**步骤**：
1. 积分桶打满，触发 reboot
2. 观察完整流程

**预期结果**：
1. NIR Phase 变为 `phasereboot`
2. 节点被 cordon + drain
3. EC2 RebootInstances API 被调用
4. Phase 变为 `phaserebooted`
5. 等待 2 分钟 grace period
6. 等待节点恢复 Ready
7. 节点被 uncordon
8. SNS 通知发送（subject 包含 "Node REBOOTED"）
9. NIR 重置为 Phase=PhaseNone, Action=None（不删除）

### TC-3.2 Reboot grace period 等待

**步骤**：
1. 触发 reboot
2. 在 reboot 后立即检查日志

**预期结果**：
- 日志显示 `waiting for reboot to take effect, Xs since reboot, grace period 2m0s`
- 不会在 2 分钟内 uncordon

### TC-3.3 Reboot 后节点未恢复

**步骤**：
1. 触发 reboot
2. 在节点上阻止 kubelet 启动（模拟 reboot 失败）

**预期结果**：
- NIR 持续在 PhaseRebooted，日志反复显示 `node is not ready yet, waiting for it to recover`
- NodeController 的 double check 最终会触发 replace（如果 grace time 到期）

---

## 四、Replace 操作

### TC-4.1 正常 replace 流程

**前置条件**：ToleranceConfig action 设为 `replace`，或通过 NodeController 触发

**步骤**：
1. 触发 replace
2. 观察完整流程

**预期结果**：
1. Phase 变为 `phasereplace`
2. 从 ASG detach 实例
3. Phase 变为 `phasedetached`
4. 等待新节点加入（同 nodegroup）
5. Phase 变为 `phaseneejoined`
6. drain 旧节点
7. Phase 变为 `phasedrained`
8. 删除旧节点 + 删除 NIR
9. SNS 通知发送（subject 包含 "Node REPLACED"）

### TC-4.2 NodeController 触发 replace（节点 Unknown）

**步骤**：
1. SSH 到目标节点，停止 kubelet：`sudo systemctl stop kubelet`
2. 等待节点变为 Unknown
3. 等待 double check grace time 到期

**预期结果**：
- NodeController 创建 NIR，设置 `Action=replace`
- NIRController 执行 replace（Phase=PhaseReplace，不是 PhaseReboot）
- 日志显示 `[action dispatch] node xxx, action=replace`

### TC-4.3 NodeController 不覆盖活跃 Phase

**步骤**：
1. 通过积分桶触发 reboot（Phase=PhaseReboot）
2. 在 reboot 进行中，节点变为 NotReady
3. NodeController double check 到期

**预期结果**：
- 日志显示 `NIR is already in active phase phasereboot, skip setting replace action`
- 不会覆盖正在进行的 reboot

---

## 五、Escalation

### TC-5.1 Cooldown 内再次打满触发 escalation

**前置条件**：
```yaml
action: reboot
cooldownTimeInMinutes: 30
escalateOperation: replace
```

**步骤**：
1. 积分桶打满 → reboot
2. reboot 完成，NIR 重置
3. 在 30 分钟内再次注入事件，积分桶再次打满

**预期结果**：
- 日志显示 `[escalation] score bucket overflow within cooldown`
- NIR 的 `escalated` 变为 true
- 执行 replace 而不是 reboot

### TC-5.2 Cooldown 过期后正常触发（非 escalation）

**步骤**：
1. 积分桶打满 → reboot
2. 等待 cooldown 过期
3. 再次注入事件，积分桶打满

**预期结果**：
- 不触发 escalation，执行正常的 reboot
- `escalated` 保持 false

### TC-5.3 Escalation 到 paging

**前置条件**：`escalateOperation: paging`

**步骤**：
1. 触发 reboot → cooldown 内再次打满

**预期结果**：
- 执行 paging（仅通知）
- SNS subject 包含 "ESCALATION"
- NIR 被删除（escalated paging 是终态）

---

## 六、NIR 生命周期

### TC-6.1 Reboot 后 NIR 保留

**步骤**：
1. 触发 reboot，等待完成

**预期结果**：
- NIR 未被删除
- Phase=PhaseNone, Action=None
- `lastActionTime` 已设置

### TC-6.2 Cooldown 过期自动清理

**前置条件**：`cooldownTimeInMinutes: 2`（设置较短便于测试）

**步骤**：
1. 触发 reboot，等待完成
2. 等待 cooldown 过期（2 分钟）
3. 等待清理任务执行（每分钟一次）

**预期结果**：
- 日志显示 `[lifecycle cleanup] cooldown expired for node xxx, cleaning up NIR`
- SNS 通知发送（subject 包含 "cooldown expired"）
- NIR 被删除

### TC-6.3 Replace 后 NIR 立即删除

**步骤**：
1. 触发 replace，等待完成

**预期结果**：
- NIR 在 PhaseDrained 后被删除

---

## 七、Paging 操作

### TC-7.1 Action=paging 仅通知

**前置条件**：ToleranceConfig `action: paging`

**步骤**：
1. 积分桶打满

**预期结果**：
- SNS 通知发送（subject 包含 "admin notification (paging)"）
- 不执行 reboot/replace
- NIR 重置为 PhaseNone（保留用于 escalation）

### TC-7.2 allowOperation=false 仅通知

**前置条件**：`allowOperation: false`

**步骤**：
1. 积分桶打满

**预期结果**：
- SNS 通知发送（subject 包含 "auto-action disabled"）
- NIR 重置，不执行操作

---

## 八、Dry-run 模式

### TC-8.1 Dry-run reboot

**前置条件**：`dryRun: true`, `action: reboot`

**步骤**：
1. 积分桶打满

**预期结果**：
- 日志显示 `[dry-run] would execute reboot on node xxx, but dry-run is enabled`
- SNS 通知发送（subject 包含 "DRY-RUN: would execute reboot"）
- 节点未被 reboot
- NIR 重置为 PhaseNone, Action=None

### TC-8.2 Dry-run replace

**前置条件**：`dryRun: true`, `action: replace`

**步骤**：
1. 积分桶打满

**预期结果**：
- SNS 通知包含 "DRY-RUN: would execute replace"
- 节点未被 replace

---

## 九、并发控制

### TC-9.1 MaxConcurrentActions 限制

**前置条件**：
```yaml
maxConcurrentActions: 1
```
集群中有 2 个匹配该规则的节点

**步骤**：
1. 在节点 A 上触发 reboot（积分桶打满）
2. 在节点 B 上也触发 reboot（积分桶打满）

**预期结果**：
- 节点 A 开始 reboot
- 节点 B 日志显示 `[concurrency] max concurrent actions reached, requeueing node`
- 节点 A reboot 完成后，节点 B 开始处理

### TC-9.2 MaxConcurrentActions=0 不限制

**前置条件**：`maxConcurrentActions: 0` 或未设置

**步骤**：
1. 同时在 2 个节点触发 reboot

**预期结果**：
- 两个节点同时开始 reboot

---

## 十、节点状态监控（NodeController）

### TC-10.1 节点 NotReady → double check → replace

**步骤**：
1. 停止目标节点的 kubelet
2. 等待节点变为 NotReady
3. 等待 double check grace time 到期

**预期结果**：
- NodeController 创建 NIR
- grace time 后 double check，节点仍 NotReady → 设置 Action=replace
- NIRController 执行 replace

### TC-10.2 节点 NotReady → 恢复 Ready

**步骤**：
1. 停止 kubelet
2. 在 grace time 内重启 kubelet

**预期结果**：
- double check 时节点已恢复 Ready
- NIR 被删除（无问题记录时）或更新 NodeStatus=Ready（有问题记录时）

### TC-10.3 Karpenter 节点状态变化过滤

**步骤**：
1. 观察 Karpenter 节点状态变化

**预期结果**：
- 日志显示 `Node xxx is managed by Karpenter, skip it`
- 不创建 NIR

### TC-10.4 Fargate 节点状态变化过滤

**预期结果**：
- 日志显示 `Node xxx is a Fargate node, skip it`

---

## 十一、Prometheus Metrics

### TC-11.1 Metrics endpoint 可访问

**步骤**：
1. `kubectl port-forward <pod> 9090:9090`
2. `curl localhost:9090/metrics`

**预期结果**：
- 返回 Prometheus 格式的 metrics
- 包含 `npd_node_replace_score_bucket_current`、`npd_node_replace_actions_total`、`npd_node_replace_events_total`、`npd_node_replace_nir_active`

### TC-11.2 事件计数递增

**步骤**：
1. 注入 OOMKilling 事件
2. 查询 `npd_node_replace_events_total{event_type="OOMKilling"}`

**预期结果**：计数 +1

### TC-11.3 积分桶水位更新

**步骤**：
1. 注入事件
2. 查询 `npd_node_replace_score_bucket_current{node="xxx"}`

**预期结果**：值等于 NIR 的 `scoreInBucket`

### TC-11.4 操作计数

**步骤**：
1. 触发 reboot
2. 查询 `npd_node_replace_actions_total{action="reboot",escalated="false"}`

**预期结果**：计数 +1

### TC-11.5 活跃 NIR 数量

**步骤**：
1. 触发事件创建 NIR
2. 查询 `npd_node_replace_nir_active`

**预期结果**：值等于集群中 NIR 资源数量

---

## 十二、SNS 通知

### TC-12.1 各场景通知主题验证

对以下每个场景触发操作，验证收到的邮件主题：

| 场景 | 预期 Subject |
|------|-------------|
| reboot 完成 | `[npd-node-replace] Node REBOOTED due to persistent issues` |
| replace 完成 | `[npd-node-replace] Node REPLACED due to persistent issues` |
| paging | `[npd-node-replace] Node issues detected - admin notification (paging)` |
| allowOperation=false | `[npd-node-replace] Node issues detected - auto-action disabled, notify only` |
| cooldown 过期 | `[npd-node-replace] Node issue report cleanup - cooldown expired, no escalation triggered` |
| escalation paging | `[npd-node-replace] ESCALATION - repeated issues after action, admin notification` |
| dry-run | `[npd-node-replace] DRY-RUN: would execute {action}, but dry-run mode is enabled` |

### TC-12.2 通知内容包含完整信息

**步骤**：
1. 触发任意操作
2. 检查邮件内容

**预期结果**：
- 包含 NodeName、NodeStatus、Issues Detected、Action、Escalated、ScoreInBucket、Full NodeIssueReport JSON

---

## 十三、Leader Election

### TC-13.1 多副本只有一个 leader 工作

**前置条件**：replicas=2

**步骤**：
1. 检查两个 pod 的日志

**预期结果**：
- 一个 pod 显示 `became leader, starting controllers`
- 另一个 pod 显示 `new leader elected: xxx`，不启动 controller

### TC-13.2 Leader 故障切换

**步骤**：
1. 删除当前 leader pod
2. 观察另一个 pod 的日志

**预期结果**：
- 另一个 pod 获取 lease，显示 `became leader, starting controllers`
- 继续处理事件

---

## 十四、ToleranceConfig 热更新

### TC-14.1 修改 ToleranceConfig 后生效

**步骤**：
1. 修改 ToleranceConfig 的 bucketSize（如从 80 改为 40）
2. `kubectl apply` 更新
3. 注入事件验证新的阈值

**预期结果**：
- 无需重启 pod
- 新的 bucketSize 立即生效

### TC-14.2 删除 ToleranceConfig

**步骤**：
1. 删除 ToleranceConfig
2. 注入事件

**预期结果**：
- 日志显示 `no ToleranceConfig resources found in cluster`
- 不执行任何操作

---

## 十五、边界场景

### TC-15.1 节点同时匹配多条规则

**前置条件**：节点同时有 `instance-type=c4.large` 和 `nodegroup=test-ng` 标签，两条规则都匹配

**预期结果**：
- 使用第一个匹配到的规则（遍历顺序）

### TC-15.2 ToleranceConfig 中 nodeLabel 格式错误

**前置条件**：设置 `nodeLabel: "invalid-format"`（无等号）

**预期结果**：
- 日志显示 `invalid nodeLabel format in ToleranceConfig`
- 该规则被跳过

### TC-15.3 SNS Topic 不可用

**前置条件**：设置错误的 SNS_TOPIC_ARN

**预期结果**：
- 操作不会因为 SNS 失败而中断（reboot/replace 仍然执行）
- 日志记录 SNS 发送失败

### TC-15.4 Pod 滚动更新期间的行为

**步骤**：
1. 在有活跃 NIR（Phase != PhaseNone）时滚动更新 pod

**预期结果**：
- 新 pod 启动后，informer 同步 NIR 状态
- 处于活跃 Phase 的 NIR 继续被处理（通过 informer 的 Add 事件触发）
- delayqueue 中的任务丢失（已知限制）

### TC-15.5 节点被删除后 NIR 处理

**步骤**：
1. 创建 NIR 后手动删除节点

**预期结果**：
- NIRController 日志显示 `node object not found, may be already deleted`
- 不会 panic 或无限重试
