# 简介
npd-node-replace 的主要功能如下：
1. 侦听来自 npd 的关于节点问题的报告事件
2. 将节点的问题事件记录在 NodeIssueReport 中
3. 根据用户配置的 [Tolerance 配置](https://github.com/normalzzz/npd-node-replace/blob/main/pkg/config/tolerance.json) 进行节点的自动替换或重启

## 整体架构：
![structure](./npd-node-replace.png)

# 部署
**npd-node-place 依赖 node-problem-detector 组件侦听事件，请先部署 [node-problem-detector](https://github.com/kubernetes/node-problem-detector?tab=readme-ov-file#installation)**

## 创建 service account、clusterole、clusterrolebinding
```bash
kubectl apply -f deploy/npd-node-replace-clusterrole.yaml
kubectl apply -f deploy/npd-node-replace-sa.yaml
kubectl apply -f deploy/npd-node-replace-clusterrolebinding.yaml
```
## 部署 deployment
```bash
kubectl apply -f deploy/npd-node-replace-deployment.yaml
```

## Tolerance 配置：
Tolerance 配置中可以配置对于某些问题发生问题的容忍次数，示例配置：
```json
{
  "tolerancecollection": {
    "OOMKilling": {
      "times": 2,
      "action": "reboot"
    },
    "KernelOops": {
      "times": 3,
      "action": "replace"
    }
  }
}
```
使用该配置，可以实现在触发两次 OOMKill 事件之后重启节点，在触发三次 KernelOops 事件之后，替换节点。
由于事件为 Node Problem Detector 组件发出，关于所有支持的事件类型，可以参考 [Node Problem Detector config](https://github.com/kubernetes/node-problem-detector/tree/master/config)

镜像地址：442337510176.dkr.ecr.cn-north-1.amazonaws.com.cn/zxxxx/npd-node-replace:v1