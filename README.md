Try using [vgpu-manager](https://github.com/coldzerofear/vgpu-manager) to solve the scheduling and allocation problems of VGPU. It will solve some existing problems and add new features that you may be interested in.

# GPU Manager

GPU管理器用于管理Kubernetes集群中的英伟达GPU设备。它实现了`DevicePlugin`接口。

与`nvidia docker`和`nvidia-k8s-plugin`的组合解决方案相比，GPU管理器将在不修改容器运行时配置的情况下使用`runc`。此外，还集成了指标采集。

要正确调度GPU有效负载，`gpu manager`需要结合使用`gpu admission`, 它是一个kubernetes调度器插件。

GPU管理器支持GPU设备的细粒度分配，如请求0.1卡和100MiB 设备内存。

If you want this kind feature, please refer to [vcuda-controller](https://github.com/tkestack/vcuda-controller) project.

## 构建

**1.** 构建二进制包

- 必要条件
   - 宿主机安装CUDA toolkit
   
```bash
make
```

**2.** 构建镜像

- 必要条件
    - 安装Docker或其他符合OCI规范的容器运行时、例如：containerd 或 cri-o

```bash
# 构建x86架构镜像
make img
# 构建arm64架构镜像
make img-arm
```

## 预构建镜像

Prebuilt image can be found at `registry.tydic.com/gpu-manager`

## 部署

GPU管理器作为`daemonset`部署运行，需要执行以下步骤进行安装

- 在需要运行`gpu-manager`的节点上配置标签 `nvidia-device-enable=enable`

```bash
kubectl label node <node> nvidia-device-enable=enable
```

- 根据节点情况修改配置文件

`gpu-manager`允许集群内节点配置差异化

> 注意: 现已支持自动识别cgroupDriver和runtimeEndpoint, 可以不用额外配置容器环境变量或gpu-manager-config。
> runtimeEndpoint的自动识别目前兼容docker和containerd, 其他的容器运行时需要后续适配。
> 当自动识别不准确造成错误时，可以在gpu-manager-config中手动指定不同节点的差异配置。

```yaml
vim ./deploy/configmap.yaml
-----------------------------
apiVersion: v1
kind: ConfigMap
metadata:
  name: gpu-manager-config
  namespace: kube-system
data:
  config.json: |
    {
        "nodeConfig": [
            {
                "name": "k8s01", ## 需要匹配配置的节点名称，没有配置节点的将按默认配置执行
                "deviceMemoryScaling": 1, ## 设备内存缩放比，目前只支持0-1之间的小数，例如0.5会使该节点上的gpu设备保留50%的显存，默认为1
                "containerRuntimeEndpoint": "/var/run/containerd/containerd.sock", ## 容器运行时接口套接字, 默认自动检测docker、containerd
                "cgroupDriver": "systemd" ## 配置节点cgroup驱动：systemd、cgroupfs
            },{
                "name": "k8s02",
                "deviceMemoryScaling": 1,
                "containerRuntimeEndpoint": "/var/run/containerd/containerd.sock",
                "cgroupDriver": "systemd"
            }
        ]
    }
----------------------------
kubectl create -f ./deploy/configmap.yaml
```

- 部署gpu-manager

```bash
kubectl create -f ./deploy/gpu-manager.yaml
```

## Pod模板示例

GPU Manager会将一张GPU注册为100个资源便于细粒度算力分配, 会将GPU显存按照实际大小以MB为单位注册到node上。
相关资源名：`nvidia.com/vcuda-core`、`vidia.com/vcuda-memory`

由于Kubelet设备插件的限制，为了支持GPU的算力软硬配置限制，需要在Pod的annotations中添加key`nvidia.com/vcuda-core-limit`

> 注意：`nvidia.com/vcuda-core` 可以是小于100的任何整数和100的倍数。例如：100、200或20是有效值，150或250是无效值，
100相当于以独占模式占用整个显卡，200则是独占两个卡（gpu-manager支持多卡拓扑感知），小于100代表共享模式只占用部分显卡算力和内存。

- 提交一个GPU利用率限制为0.5、显存限制为2gb的Pod

```
apiVersion: v1
kind: Pod
metadata:
  name: vcuda
  annotations:
    nvidia.com/vcuda-core-limit: 50
spec:
  restartPolicy: Never
  containers:
  - image: <test-image>
    name: nvidia
    command:
    - /usr/local/nvidia/bin/nvidia-smi
    - pmon
    - -d
    - 10
    resources:
      requests:
        nvidia.com/vcuda-core: 50
        nvidia.com/vcuda-memory: 2048
      limits:
        nvidia.com/vcuda-core: 50
        nvidia.com/vcuda-memory: 2048
```

- 提交一个拥有两张gpu的pod

以独占模式提交的pod无需指定`nvidia.com/vcuda-memory`, 此时gpu-manager会将分配的gpu完全提供给Pod不做任何限制。

```
apiVersion: v1
kind: Pod
metadata:
  name: vcuda
spec:
  restartPolicy: Never
  containers:
  - image: <test-image>
    name: nvidia
    command:
    - /usr/local/nvidia/bin/nvidia-smi
    - pmon
    - -d
    - 10
    resources:
      requests:
        nvidia.com/vcuda-core: 200
      limits:
        nvidia.com/vcuda-core: 200
```

## FAQ

If you have some questions about this project, you can first refer to [FAQ](./docs/faq.md) to find a solution.
