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
## 部署 
```bash
kubectl apply -f deploy/npd-node-replace-deployment.yaml
```
镜像地址：442337510176.dkr.ecr.cn-north-1.amazonaws.com.cn/zxxxx/npd-node-replace:v1
### Tolerance 配置：
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

根据您的 Tolerance 配置需要修改 [tolerance configmap](https://github.com/normalzzz/npd-node-replace/blob/main/deploy/tolerance-configmap.yaml)

### 权限配置：
您需要创建 [IRSA](https://docs.amazonaws.cn/eks/latest/userguide/iam-roles-for-service-accounts.html) 的方式为 npd-node-replace pod 赋予 Amazon Web Services 权限。
修改 sevice account 配置清单，添加与 IAM role 的关联，如下，您需要将  <irsa iam role arn> 部分替换为 IRSA role arn。
```json
apiVersion: v1
kind: ServiceAccount
metadata:
  annotations:
    eks.amazonaws.com/role-arn: <irsa iam role arn>
  creationTimestamp: null
  name: npd-node-replace-sa
  namespace: kube-system
```
IAM role 的权限配置：您可以使用如下 Managed Policy：
AmazonEC2FullAccess
AmazonSNSFullAccess
AutoScalingFullAccess

### 部署模板：
- 使用 kubectl apply 模板：
```bash
kubectl apply -f deploy/npd-node-replace-clusterrole.yaml
kubectl apply -f deploy/npd-node-replace-clusterrolebinding.yaml
kubectl apply -f deploy/npd-node-replace-deployment.yaml
kubectl apply -f deploy/npd-node-replace-sa.yaml
kubectl apply -f deploy/tolerance-configmap.yaml
```

## 测试：
可以使用如下方式注入实例系统问题：
OOMKilling 问题模拟：
```bash
echo "Killed process 1234 (myapp) total-vm:102400kB, anon-rss:51200kB, file-rss:2048kB" | sudo tee /dev/kmsg
```

KernelOops 问题模拟：
```bash
echo "<1>BUG: unable to handle kernel NULL pointer dereference at 0x00000000" | sudo tee /dev/kmsg
echo "<1>divide error: 0000 [#1] SMP" | sudo tee /dev/kmsg
```

