# 简介
npd-node-replace 的主要功能如下：
1. 侦听来自 npd 的关于节点问题的报告事件
2. 根据用户配置的 [Tolerance 配置](https://github.com/normalzzz/npd-node-replace/blob/main/pkg/config/tolerance.json) 进行节点的自动替换或重启

# 部署
**npd-node-place 依赖 node-problem-detector 组件侦听事件，请先部署 [node-problem-detector](https://github.com/kubernetes/node-problem-detector?tab=readme-ov-file#installation)**

## 创建 service account、clusterole、clusterrolebinding
kubectl apply -f deploy/npd-node-replace-clusterrole.yaml
kubectl apply -f deploy/npd-node-replace-sa.yaml
kubectl apply -f deploy/npd-node-replace-clusterrolebinding.yaml

## 部署 deployment
kubectl apply -f deploy/npd-node-replace-deployment.yaml

镜像地址：442337510176.dkr.ecr.cn-north-1.amazonaws.com.cn/zxxxx/npd-node-replace:v1