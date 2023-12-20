package watchdog

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/NVIDIA/go-nvml/pkg/nvml"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	v1core "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/klog"
	"strconv"
	"time"
	"tkestack.io/gpu-manager/pkg/config"
	"tkestack.io/gpu-manager/pkg/device/nvidia"
	"tkestack.io/gpu-manager/pkg/types"
	"tkestack.io/gpu-manager/pkg/utils"
)

type nodeAnnotator struct {
	config *config.Config
	client v1core.CoreV1Interface
}

type GPUInfo struct {
	Id         string `json:"id,omitempty"`
	Core       int    `json:"core,omitempty"`
	Memory     int64  `json:"memory,omitempty"`
	Type       string `json:"type,omitempty"`
	IsMig      bool   `json:"isMig"`
	Capability int    `json:"capability,omitempty"`
	Health     bool   `json:"health"`
}

func NewNodeAnnotator(client v1core.CoreV1Interface, config *config.Config) *nodeAnnotator {
	klog.V(2).Infof("Annotator for hostname %s", config.Hostname)
	return &nodeAnnotator{
		config: config,
		client: client,
	}
}

const (
	NodeAnnotationHeartbeat      = "tydic.io/node-gpu-heartbeat"
	NodeAnnotationDeviceRegister = "tydic.io/nvidia-device-register"
)

// TODO 开启协程异步 更新节点注解 心跳、检测设备是否开启mig 每30秒执行一次
func (nl *nodeAnnotator) Run() error {
	go wait.UntilWithContext(context.Background(), func(ctx context.Context) {
		annotations := nl.getMigAnnotations()
		annotations[NodeAnnotationHeartbeat] = fmt.Sprintf("%d", time.Now().UnixNano())
		patch := utils.NewPatchAnnotation(annotations)
		bytes, err := json.Marshal(patch)
		if err != nil {
			klog.Errorf("JSON serialization failed: %v", err)
			return
		}
		_, err = nl.client.Nodes().Patch(ctx, nl.config.Hostname, apitypes.StrategicMergePatchType, bytes, metav1.PatchOptions{})
		if err != nil {
			klog.V(2).Infof("patch node annotations heartbeat error, %v", err)
		}
	}, 30*time.Second)
	klog.V(2).Infof("Auto annotator is running")
	return nil
}

func (nl *nodeAnnotator) getMigAnnotations() map[string]string {
	annotations := make(map[string]string)
	if rs := nvml.Init(); rs != nvml.SUCCESS {
		klog.Warningf("Can't initialize nvml library, %s", nvml.ErrorString(rs))
	}
	defer nvml.Shutdown()
	count, rs := nvml.DeviceGetCount()
	if rs != nvml.SUCCESS {
		klog.Warningf("Can't get device count, %s", nvml.ErrorString(rs))
	}
	gpuInfos := make([]GPUInfo, count)
	for index := 0; index < count; index++ {
		gpuInfo := GPUInfo{Health: true}
		dev, r := nvml.DeviceGetHandleByIndex(index)
		if r != nvml.SUCCESS {
			gpuInfo.Health = false
			gpuInfos[index] = gpuInfo
			continue
		}
		name, r := dev.GetName()
		if r != nvml.SUCCESS {
			gpuInfo.Health = false
		}
		memInfo, r := dev.GetMemoryInfo_v2()
		if r != nvml.SUCCESS {
			gpuInfo.Health = false
		}
		uuid, r := dev.GetUUID()
		if r != nvml.SUCCESS {
			gpuInfo.Health = false
		}
		if !gpuInfo.Health {
			gpuInfos[index] = gpuInfo
			continue
		}
		major, minor, _ := dev.GetCudaComputeCapability()
		level := fmt.Sprintf("%d%d", major, minor)
		if capability, err := strconv.Atoi(level); err == nil {
			gpuInfo.Capability = capability
		}
		totalMemory := int64(nl.config.DeviceMemoryScaling * float64(memInfo.Total))
		gpuInfo.Id = uuid
		gpuInfo.Type = name
		gpuInfo.Core = 100
		gpuInfo.Memory = totalMemory / types.MemoryBlockSize
		gpuInfo.IsMig = nvidia.IsMig(index)
		gpuInfos[index] = gpuInfo
	}
	if bytes, err := json.Marshal(gpuInfos); err != nil {
		annotations[NodeAnnotationDeviceRegister] = "[]"
	} else {
		annotations[NodeAnnotationDeviceRegister] = string(bytes)
	}
	return annotations
}
