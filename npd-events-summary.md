# Node Problem Detector 支持的事件与指标完整列表

## 一、问题事件（Events & Conditions）

| EventSource.Component | Monitor 类型 | 事件/条件名 (Reason) | 事件类型 | 关联 Condition | 匹配方式 |
|---|---|---|---|---|---|
| kernel-monitor | SystemLogMonitor | OOMKilling | temporary | — | 正则匹配 `/dev/kmsg` |
| kernel-monitor | SystemLogMonitor | TaskHung | temporary | — | 正则匹配 `/dev/kmsg` |
| kernel-monitor | SystemLogMonitor | UnregisterNetDevice | temporary | — | 正则匹配 `/dev/kmsg` |
| kernel-monitor | SystemLogMonitor | KernelOops (NULL pointer) | temporary | — | 正则匹配 `/dev/kmsg` |
| kernel-monitor | SystemLogMonitor | KernelOops (divide error) | temporary | — | 正则匹配 `/dev/kmsg` |
| kernel-monitor | SystemLogMonitor | Ext4Error | temporary | — | 正则匹配 `/dev/kmsg` |
| kernel-monitor | SystemLogMonitor | Ext4Warning | temporary | — | 正则匹配 `/dev/kmsg` |
| kernel-monitor | SystemLogMonitor | IOError | temporary | — | 正则匹配 `/dev/kmsg` |
| kernel-monitor | SystemLogMonitor | MemoryReadError | temporary | — | 正则匹配 `/dev/kmsg` |
| kernel-monitor | SystemLogMonitor | CperHardwareErrorCorrected | temporary | — | 正则匹配 `/dev/kmsg` |
| kernel-monitor | SystemLogMonitor | CperHardwareErrorRecoverable | temporary | — | 正则匹配 `/dev/kmsg` |
| kernel-monitor | SystemLogMonitor | CperHardwareErrorFatal | permanent | CperHardwareErrorFatal | 正则匹配 `/dev/kmsg` |
| kernel-monitor | SystemLogMonitor | XfsHasShutdown | permanent | XfsShutdown | 正则匹配 `/dev/kmsg` |
| kernel-monitor | SystemLogMonitor | DockerHung | permanent | KernelDeadlock | 正则匹配 `/dev/kmsg` |
| kernel-monitor | CustomPluginMonitor | UnregisterNetDevice | permanent | FrequentUnregisterNetDevice | log-counter 脚本 |
| docker-monitor | SystemLogMonitor | CorruptDockerImage | temporary | — | 正则匹配 journald |
| docker-monitor | SystemLogMonitor | DockerContainerStartupFailure | temporary | — | 正则匹配 journald |
| docker-monitor | SystemLogMonitor | CorruptDockerOverlay2 | permanent | CorruptDockerOverlay2 | 正则匹配 journald |
| docker-monitor | CustomPluginMonitor | CorruptDockerOverlay2 | permanent | CorruptDockerOverlay2 | log-counter 脚本 |
| systemd-monitor | SystemLogMonitor | KubeletStart | temporary | — | 正则匹配 journald |
| systemd-monitor | SystemLogMonitor | DockerStart | temporary | — | 正则匹配 journald |
| systemd-monitor | SystemLogMonitor | ContainerdStart | temporary | — | 正则匹配 journald |
| systemd-monitor | CustomPluginMonitor | FrequentKubeletRestart | permanent | FrequentKubeletRestart | log-counter 脚本 |
| systemd-monitor | CustomPluginMonitor | FrequentDockerRestart | permanent | FrequentDockerRestart | log-counter 脚本 |
| systemd-monitor | CustomPluginMonitor | FrequentContainerdRestart | permanent | FrequentContainerdRestart | log-counter 脚本 |
| readonly-monitor | SystemLogMonitor | FilesystemIsReadOnly | permanent | ReadonlyFilesystem | 正则匹配 `/dev/kmsg` |
| disk-monitor | SystemLogMonitor | DiskBadBlock | permanent | DiskBadBlock | 正则匹配 `/var/log/messages` |
| abrt-adaptor | SystemLogMonitor | CCPPCrash | temporary | — | 正则匹配 journald |
| abrt-adaptor | SystemLogMonitor | UncaughtException | temporary | — | 正则匹配 journald |
| abrt-adaptor | SystemLogMonitor | XorgCrash | temporary | — | 正则匹配 journald |
| abrt-adaptor | SystemLogMonitor | VMcore | temporary | — | 正则匹配 journald |
| abrt-adaptor | SystemLogMonitor | KernelOops | temporary | — | 正则匹配 journald |
| network-custom-plugin-monitor | CustomPluginMonitor | ConntrackFull | temporary | — | 自定义脚本 |
| network-custom-plugin-monitor | CustomPluginMonitor | DNSUnreachable | temporary | — | 自定义脚本 |
| iptables-mode-monitor | CustomPluginMonitor | IPTablesVersionsMismatch | temporary | — | 自定义脚本 |
| ntp-custom-plugin-monitor | CustomPluginMonitor | NTPIsDown | temporary | — | 自定义脚本 |
| ntp-custom-plugin-monitor | CustomPluginMonitor | NTPIsDown | permanent | NTPProblem | 自定义脚本 |
| health-checker | CustomPluginMonitor | KubeletUnhealthy | permanent | KubeletUnhealthy | health-checker 二进制 |
| health-checker | CustomPluginMonitor | ContainerdUnhealthy | permanent | ContainerRuntimeUnhealthy | health-checker 二进制 |
| health-checker | CustomPluginMonitor | DockerUnhealthy | permanent | ContainerRuntimeUnhealthy | health-checker 二进制 |
| windows-defender-custom-plugin-monitor | CustomPluginMonitor | WindowsDefenderThreatsDetected | temporary | — | PowerShell 脚本 |
| containerd | SystemLogMonitor | ContainerCreationFailed | temporary | — | 正则匹配 filelog |
| containerd | SystemLogMonitor | CorruptContainerImageLayer | temporary | — | 正则匹配 filelog |
| containerd | SystemLogMonitor | HCSEmptyLayerchain | temporary | — | 正则匹配 filelog |
| health-checker | CustomPluginMonitor | KubeletUnhealthy | permanent | KubeletUnhealthy | health-checker.exe |
| health-checker | CustomPluginMonitor | ContainerdUnhealthy | permanent | ContainerRuntimeUnhealthy | health-checker.exe |
| health-checker | CustomPluginMonitor | DockerUnhealthy | permanent | ContainerRuntimeUnhealthy | health-checker.exe |
| health-checker | CustomPluginMonitor | KubeProxyUnhealthy | permanent | KubeProxyUnhealthy | health-checker.exe |