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
	"sort"
	"tkestack.io/gpu-manager/pkg/types"
	"tkestack.io/gpu-manager/pkg/utils"

	"k8s.io/klog"

	"tkestack.io/gpu-manager/pkg/device/nvidia"
)

type linkMode struct {
	tree *nvidia.NvidiaTree
}

//NewLinkMode returns a new linkMode struct.
//
//Evaluate() of linkMode returns nodes with minimum connection overhead
//of each other.

// linkMode的Evaluate（）返回彼此连接开销最小的节点。
func NewLinkMode(t *nvidia.NvidiaTree) *linkMode {
	return &linkMode{t}
}

func (al *linkMode) Evaluate(cores int64, memory int64, pod *v1.Pod) []*nvidia.NvidiaNode {
	var (
		sorter = linkSort(
			// 基于拓扑等级排序，从小到大，期望找到距离最近的显卡设备
			nvidia.ByType,
			//
			nvidia.ByAvailable,
			// 根据可分配内存从小到大排序
			nvidia.ByAllocatableMemory,
			// 根据节点上运行的PID长度比较两个NvidiaNode
			nvidia.ByPids,
			// 更具设备index排序，从小到大
			nvidia.ByMinorID,
		)
		tmpStore = make(map[int]*nvidia.NvidiaNode)
		root     = al.tree.Root()
		nodes    = make([]*nvidia.NvidiaNode, 0)
		num      = int(cores / nvidia.HundredCore)
	)

	// 遍历叶子节点
	for _, node := range al.tree.Leaves() {
		for node != root {
			klog.V(2).Infof("Test %d mask %b", node.Meta.ID, node.Mask)
			if node.Available() < num {
				node = node.Parent
				continue
			}

			tmpStore[node.Meta.ID] = node
			klog.V(2).Infof("Choose %d mask %b", node.Meta.ID, node.Mask)
			break
		}
	}

	if len(tmpStore) == 0 {
		tmpStore[-1] = root
	}

	candidates := make([]*nvidia.NvidiaNode, 0)
	for _, n := range tmpStore {
		candidates = append(candidates, n)
	}

	sorter.Sort(candidates)

	for _, node := range candidates[0].GetAvailableLeaves() {
		if num == 0 {
			break
		}

		// TODO 添加设备类型指定功能
		if !utils.CheckDeviceType(pod.Annotations, node.Meta.Name) {
			klog.V(2).Infof("current gpu device %d name %s non compliant annotation[%s], skip device",
				node.Meta.MinorID, node.Meta.Name, fmt.Sprintf("%s or %s", types.PodAnnotationUseGpuType, types.PodAnnotationUnUseGpuType))
			continue
		}

		klog.V(2).Infof("Pick up %d mask %b", node.Meta.ID, node.Mask)
		nodes = append(nodes, node)
		num--
	}

	if num > 0 {
		return nil
	}

	return nodes
}

type linkPriority struct {
	data []*nvidia.NvidiaNode
	less []nvidia.LessFunc
}

func linkSort(less ...nvidia.LessFunc) *linkPriority {
	return &linkPriority{
		less: less,
	}
}

func (lp *linkPriority) Sort(data []*nvidia.NvidiaNode) {
	lp.data = data
	sort.Sort(lp)
}

func (lp *linkPriority) Len() int {
	return len(lp.data)
}

func (lp *linkPriority) Swap(i, j int) {
	lp.data[i], lp.data[j] = lp.data[j], lp.data[i]
}

func (lp *linkPriority) Less(i, j int) bool {
	var k int

	for k = 0; k < len(lp.less)-1; k++ {
		less := lp.less[k]
		switch {
		case less(lp.data[i], lp.data[j]):
			return true
		case less(lp.data[j], lp.data[i]):
			return false
		}
	}

	return lp.less[k](lp.data[i], lp.data[j])
}
