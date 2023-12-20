/*
 * Tencent is pleased to support the open source community by making TKEStack available.
 *
 * Copyright (C) 2012-2019 Tencent. All Rights Reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License"); you may not use
 * this file except in compliance with the License. You may obtain a copy of the
 * License at
 *
 * https://opensource.org/licenses/Apache-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
 * WARRANTIES OF ANY KIND, either express or implied.  See the License for the
 * specific language governing permissions and limitations under the License.
 */

package nvidia

import (
	"context"
	"fmt"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"tkestack.io/gpu-manager/pkg/config"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
)

func parseToGpuTopologyLevel(str string) nvml.GpuTopologyLevel {
	switch str {
	case "PIX":
		return nvml.TOPOLOGY_SINGLE
	case "PXB":
		return nvml.TOPOLOGY_MULTIPLE
	case "PHB":
		return nvml.TOPOLOGY_HOSTBRIDGE
	case "SOC":
		return nvml.TOPOLOGY_CPU
	}

	if strings.HasPrefix(str, "GPU") {
		return nvml.TOPOLOGY_INTERNAL
	}

	return 60
}

const (
	NvidiaCtlDevice    = "/dev/nvidiactl"
	NvidiaUVMDevice    = "/dev/nvidia-uvm"
	NvidiaFullpathRE   = `^/dev/nvidia([0-9]*)$`
	NvidiaDevicePrefix = "/dev/nvidia"
)

func B2S(bs []int8) string {
	b := make([]byte, len(bs))
	for i, v := range bs {
		b[i] = byte(v)
	}
	return string(b)
}

// 获取占用的设备
func GetInUseDevice(k8sClient kubernetes.Interface, config *config.Config) map[int]bool {

	inUsedDev, err := GetNvidiaDevice(k8sClient, config.Hostname)
	if err != nil {
		fmt.Println("GetNvidiaDevice err", err)
	}
	fmt.Println(" GetNvidiaDevice in use device", inUsedDev)

	devUsage := make(map[int]bool)
	for _, dev := range inUsedDev {
		index, err := strconv.Atoi(dev)
		if err != nil {
			fmt.Println(err)
		}
		devUsage[index] = true
	}
	fmt.Printf("in ues device %v", devUsage)
	return devUsage

}

// TODO 如果nvml查询出错则返回true
func IsMig(index int) bool {
	if rs := nvml.Init(); rs != nvml.SUCCESS {
		klog.Errorf("nvml lib init err: %s", nvml.ErrorString(rs))
		return true
	}
	defer nvml.Shutdown()

	device, rs := nvml.DeviceGetHandleByIndex(index)
	if rs != nvml.SUCCESS {
		klog.Errorf("DeviceGetHandleByIndex index %d err: %s", index, nvml.ErrorString(rs))
		return true
	}
	currentMode, PendingMode, rs := device.GetMigMode()
	klog.V(4).Infof("currentMode: %d, PendingMode: %d, Return: %d", currentMode, PendingMode, rs)
	if rs != nvml.SUCCESS {
		klog.Errorf("GetMigMode index %d err: %s", index, nvml.ErrorString(rs))
		return false
	}
	return currentMode == nvml.DEVICE_MIG_ENABLE
}

func GetNvidiaDevice(client kubernetes.Interface, hostname string) ([]string, error) {
	// 获取当前节点下正在运行的pod
	allPods, err := getPodsOnNode(client, hostname, string(v1.PodRunning))
	if err != nil {
		return nil, err
	}
	//gpuModKey := fmt.Sprintf("inspur.com/gpu-mod-idx-%d", containerId)
	//gpuIdxKey := fmt.Sprintf("inspur.com/gpu-index-idx-%d", containerId)
	//gpuPciKey := fmt.Sprintf("inspur.com/gpu-gpuPcieId-idx-%d", containerId)

	devMap := make(map[string]struct{}, 0)
	for _, pod := range allPods {
		for i, _ := range pod.Spec.Containers {

			if idxStr, ok := pod.ObjectMeta.Annotations[fmt.Sprintf("inspur.com/gpu-index-idx-%d", i)]; ok {
				idxList := strings.Split(idxStr, "-")
				for _, idx := range idxList {
					if _, err := strconv.Atoi(idx); err != nil {
						return nil, fmt.Errorf("predicate idx %s invalid for pod %s ", idxStr, pod.UID)
					}
					devStr := NvidiaDevicePrefix + idxStr
					if !IsValidGPUPath(devStr) {
						return nil, fmt.Errorf("predicate idx %s invalid", devStr)
					}
					if _, ok := devMap[idxStr]; !ok {
						devMap[idxStr] = struct{}{}
					}
				}
			}
		}
	}
	devList := []string{}
	for dev, _ := range devMap {
		devList = append(devList, dev)
	}
	return devList, nil
}
func getPodsOnNode(client kubernetes.Interface, hostname string, phase string) ([]v1.Pod, error) {
	if len(hostname) == 0 {
		hostname, _ = os.Hostname()
	}
	var (
		selector fields.Selector
		pods     []v1.Pod
	)

	if phase != "" {
		selector = fields.SelectorFromSet(fields.Set{"spec.nodeName": hostname, "status.phase": phase})
	} else {
		selector = fields.SelectorFromSet(fields.Set{"spec.nodeName": hostname})
	}
	var (
		podList *v1.PodList
		err     error
	)
	err = wait.PollUntilContextTimeout(context.Background(), time.Second, time.Minute, true,
		func(ctx context.Context) (bool, error) {
			podList, err = client.CoreV1().Pods(v1.NamespaceAll).List(ctx, metav1.ListOptions{
				FieldSelector: selector.String(),
				LabelSelector: labels.Everything().String(),
			})
			if err != nil {
				return false, err
			}
			return true, nil
		})
	if err != nil {
		return pods, fmt.Errorf("failed to get Pods on node %s because: %v", hostname, err)
	}

	klog.V(9).Infof("all pods on this node: %v", podList.Items)
	for _, pod := range podList.Items {
		pods = append(pods, pod)
	}

	return pods, nil
}

// IsValidGPUPath checks if path is valid Nvidia GPU device path
func IsValidGPUPath(path string) bool {
	return regexp.MustCompile(NvidiaFullpathRE).MatchString(path)
}

func GetClientAndHostName() (*kubernetes.Clientset, string, error) {
	// 1. 获取client
	config, err := rest.InClusterConfig()
	if err != nil {
		fmt.Println("get incluster config err")
		return &kubernetes.Clientset{}, "", err
	}
	k8sclient, err := kubernetes.NewForConfig(config)
	if err != nil {
		fmt.Println("getConfig err ", err)
		return &kubernetes.Clientset{}, "", err
	}
	hostname, _ := os.Hostname()
	return k8sclient, hostname, nil

}
