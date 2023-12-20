# 部署流程

## requirement

- k8s、containerd
- 安装英伟达显卡驱动和cuda（下载CUDA Toolkit即可）

​		ref：https://developer.nvidia.com/cuda-toolkit-archive

## 1、

``````
kubectl create sa gpu-manager -n kube-system
kubectl create clusterrolebinding gpu-manager-role --clusterrole=cluster-admin --serviceaccount=kube-system:gpu-manager
kubectl label node <你的GPU节点> nvidia-device-enable=enable
``````

## 2、修改kube-scheduler.yaml文件

- 备份kube-scheduler.yaml文件

``````
cp /etc/kubernetes/kube-scheduler.yaml ./
``````

- command处增加

``````
- command:
  - --config=/etc/kubernetes/scheduler-extender.yaml
``````

- volumeMounts处增加

``````
volumeMounts:
- mountPath: /etc/localtime
      name: localtime
      readOnly: true
- mountPath: /etc/kubernetes/scheduler-extender.yaml
      name: extender
      readOnly: true
- mountPath: /etc/kubernetes/scheduler-policy-config.json
      name: extender-policy
      readOnly: true
``````

- volumes处增加

``````
volumes:
- hostPath:
      path: /etc/localtime
      type: File
    name: localtime
- hostPath:
      path: /etc/kubernetes/scheduler-extender.yaml
      type: FileOrCreate
    name: extender
- hostPath:
      path: /etc/kubernetes/scheduler-policy-config.json
      type: FileOrCreate
    name: extender-policy
``````

## 3、

``````
chmod +x gpuDeploy.sh
./gpuDeploy.sh
``````


## 额外配置文件

extra-config.json

通过启动命令指定 加载外部配置的文件路径
```yaml
          env:
            - name: EXTRA_FLAGS
              value: "--extra-config=/etc/config/extra-config.json"
```

extra-config.json 配置示例
```json
{
  "default": {
      "devices": []
  }
}
```

作用：在调度器分配阶段，如果按上面方式配置了extra-config，会取default中的设备列表，将其分配给容器，也就是为容器分配一个默认设备，如果不配置则没有

