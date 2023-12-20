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
	"fmt"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"sort"
	"tkestack.io/gpu-manager/pkg/config"
	"tkestack.io/gpu-manager/pkg/types"
	"tkestack.io/gpu-manager/pkg/utils"

	"k8s.io/klog"

	"tkestack.io/gpu-manager/pkg/device/nvidia"
)

type shareMode struct {
	tree   *nvidia.NvidiaTree
	client kubernetes.Interface
	config *config.Config
}

//NewShareMode returns a new shareMode struct.
//
//Evaluate() of shareMode returns one node with minimum available cores
//which fullfil the request.
//
//Share mode means multiple application may share one GPU node which uses
//GPU more efficiently.

// shareMode的Evaluate（）返回一个具有最小可用内核的节点，以完成请求。
// shareMode 意味着多个应用程序可以共享一个GPU节点，从而更有效地使用GPU。
func NewShareMode(t *nvidia.NvidiaTree, k8sClient kubernetes.Interface, config *config.Config) *shareMode {
	return &shareMode{t, k8sClient, config}
}

func (al *shareMode) Evaluate(cores int64, memory int64, pod *v1.Pod) []*nvidia.NvidiaNode {
	var (
		nodes    []*nvidia.NvidiaNode
		tmpStore = make([]*nvidia.NvidiaNode, al.tree.Total())
		sorter   = shareModeSort(
			// 按可分配核心数
			nvidia.ByAllocatableCores,
			// 按可分配显存
			nvidia.ByAllocatableMemory,
			// 按pid
			nvidia.ByPids,
			// 按设备index
			nvidia.ByMinorID)
	)

	for i := 0; i < al.tree.Total(); i++ {
		tmpStore[i] = al.tree.Leaves()[i]
	}
	// 节点排序
	sorter.Sort(tmpStore)

	for _, node := range tmpStore {
		// 当可用核心、显存 大于等于请求
		if node.AllocatableMeta.Cores >= cores && node.AllocatableMeta.Memory >= memory {
			// TODO 排除掉开启了mig的设备
			if nvidia.IsMig(node.Meta.ID) {
				klog.V(2).Infof("current gpu device %d enabled mig mode", node.Meta.ID)
				continue
			}

			// TODO 添加设备类型指定功能
			if !utils.CheckDeviceType(pod.Annotations, node.Meta.Name) {
				klog.V(2).Infof("current gpu device %d name %s non compliant annotation[%s], skip device",
					node.Meta.MinorID, node.Meta.Name, fmt.Sprintf("%s or %s", types.PodAnnotationUseGpuType, types.PodAnnotationUnUseGpuType))
				continue
			}

			klog.V(2).Infof("Pick up %d mask %b, cores: %d, memory: %d", node.Meta.ID, node.Mask, node.AllocatableMeta.Cores, node.AllocatableMeta.Memory)
			nodes = append(nodes, node)
			break
		}
	}

	return nodes
}

type shareModePriority struct {
	data []*nvidia.NvidiaNode
	less []nvidia.LessFunc
}

func shareModeSort(less ...nvidia.LessFunc) *shareModePriority {
	return &shareModePriority{
		less: less,
	}
}

func (smp *shareModePriority) Sort(data []*nvidia.NvidiaNode) {
	smp.data = data
	sort.Sort(smp)
}

func (smp *shareModePriority) Len() int {
	return len(smp.data)
}

func (smp *shareModePriority) Swap(i, j int) {
	smp.data[i], smp.data[j] = smp.data[j], smp.data[i]
}

func (smp *shareModePriority) Less(i, j int) bool {
	var k int

	for k = 0; k < len(smp.less)-1; k++ {
		less := smp.less[k]
		switch {
		case less(smp.data[i], smp.data[j]):
			return true
		case less(smp.data[j], smp.data[i]):
			return false
		}
	}

	return smp.less[k](smp.data[i], smp.data[j])
}
