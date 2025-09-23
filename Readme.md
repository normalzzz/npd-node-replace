# 简介
npd-node-replace 组件用于缓解当前 Amazon EKS 在中国区未上线的 Auto Repire 导致 EKS 集群中的问题节点无法被及时处理的痛点，仅用于中国区 Amazon EKS，对于 global 区域建议使用 [node auto repair](https://docs.aws.amazon.com/eks/latest/userguide/node-health.html)
npd-node-replace 的主要功能如下：
1. 侦听来自 npd 的关于节点问题的报告事件
2. 将节点的问题事件记录在 NodeIssueReport 中
3. 根据用户配置的 [Tolerance 配置](https://github.com/normalzzz/npd-node-replace/blob/main/pkg/config/tolerance.json) 进行节点的自动替换或重启
4. 通知集群 Admin 节点上发生的历史问题事件
**重要**： 
- npd-node-replace 组件仅侦听 EKS 托管节点组中节点的事件， 不会处理 Karpenter 节点问题
- npd-node-replace 组件属于个人项目，并不属于 Amazon Web Service 中国区，如果遇到问题请提交 [Issue](https://github.com/normalzzz/npd-node-replace/issues), issue 会被及时处理
- npd-node-replace 组件当前仍处于测试开发阶段，请在详尽的测试和考虑之后使用在 EKS 集群中。

## 整体架构：
![structure](./npd-node-replace.png)

## 镜像构建：
您可以使用如下命令进行镜像构建，并将镜像推送到镜像仓库，例如 [Amazon ECR](https://docs.amazonaws.cn/AmazonECR/latest/userguide/docker-push-ecr-image.html) 
```bash
docker build -t npd-node-replace .
```

# 部署
**npd-node-place 依赖 node-problem-detector 组件侦听事件，请先部署 [node-problem-detector](https://github.com/kubernetes/node-problem-detector?tab=readme-ov-file#installation)**

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

根据您的 Tolerance 配置需要修改 [tolerance configmap](https://github.com/normalzzz/npd-node-replace/blob/main/deploy/tolerance-configmap.yaml)

### 权限配置：
npd-node-replace 组件需要结合 Amazon EC2、Amazon Autoscaling group 、Amazon SNS 服务，您需要为其配置权限。 
建议通过 [IRSA](https://docs.amazonaws.cn/eks/latest/userguide/iam-roles-for-service-accounts.html) 的方式为 npd-node-replace pod 赋予 Amazon Web Services 权限。
修改 sevice account 配置清单，添加与 IAM role 的关联，如下，您需要将  <irsa iam role arn> 部分替换为 IRSA role arn。
```
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
[AmazonEC2FullAccess](https://docs.aws.amazon.com/zh_cn/aws-managed-policy/latest/reference/AmazonEC2FullAccess.html)
[AmazonSNSFullAccess](https://docs.aws.amazon.com/zh_cn/aws-managed-policy/latest/reference/AmazonSNSFullAccess.html)
[AutoScalingFullAccess](https://docs.aws.amazon.com/zh_cn/aws-managed-policy/latest/reference/AutoScalingFullAccess.html)

IRSA 的创建方式您可以参考： https://docs.amazonaws.cn/eks/latest/userguide/iam-roles-for-service-accounts.html


### npd-node-replace-deployment.yaml 配置：
#### 环境变量配置：
在 [npd-node-replace-deployment.yaml](https://github.com/normalzzz/npd-node-replace/blob/main/deploy/npd-node-replace-deployment.yaml) 中您需要在 Deployment.spec.template.spec.env 的如下环境变量中添加 SNS Topic ARN:
```yaml
        - name: SNS_TOPIC_ARN
          value: <amazon sns topic arn>
```

#### image url 配置：
需要在如下 Deployment.spec.template.containers["npd-node-replace"].image 部分替换为您之前创建的容器镜像 URL：
```yaml
      containers:
      - image: <image_url>
        name: npd-node-replace
```

### 部署模板：
1. 部署 CRD：
```bash
kubectl apply -f config/crd/nodeissuereporter.xingzhan.io_nodeissuereports.yaml
```
2. 使用 kubectl apply 模板：
```bash
kubectl apply -f deploy/npd-node-replace-clusterrole.yaml
kubectl apply -f deploy/npd-node-replace-clusterrolebinding.yaml
kubectl apply -f deploy/npd-node-replace-sa.yaml
kubectl apply -f deploy/tolerance-configmap.yaml
kubectl apply -f deploy/npd-node-replace-deployment.yaml
```
TODO： Helm 部署


# 测试：
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

根据仓库中的[示例配置](https://github.com/normalzzz/npd-node-replace/blob/main/deploy/tolerance-configmap.yaml)，在使用上述方式触发两次 OOMKilling 事件之后，会发生节点重启。 触发三次 KernelOops 事件之后，会发生节点替换。且在节点重启和替换之后，在 [npd-node-replace-deployment.yaml](https://github.com/normalzzz/npd-node-replace/blob/main/deploy/npd-node-replace-deployment.yaml) 中配置的 SNS topic 会受到邮件提醒，通知节点发生过的历史问题。