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

type fragmentMode struct {
	tree *nvidia.NvidiaTree
}

//NewFragmentMode returns a new fragmentMode struct.
//
//Evaluate() of fragmentMode returns nodes with minimum available cores
//whiluate() of shareMode returns one node with minimum available cores which fullfil the request.Share mode means multiple application may share one GPU node which uses GPU more efficiently.ch fullfil the request.
//
//Fragment mode means to allocate cores on fragmented nodes first, which
//helps link mode work better.

// fragmentMode的Evaluate（）返回具有最小可用内核的节点，这些内核用于填充请求。
// 碎片模式意味着首先在碎片节点上分配核心，这有助于link模式更好地工作。
func NewFragmentMode(t *nvidia.NvidiaTree) *fragmentMode {
	return &fragmentMode{t}
}

func (al *fragmentMode) Evaluate(cores int64, _ int64, pod *v1.Pod) []*nvidia.NvidiaNode {
	var (
		candidate = al.tree.Root()
		next      *nvidia.NvidiaNode
		sorter    = fragmentSort(
			nvidia.ByAvailable,
			nvidia.ByAllocatableMemory,
			nvidia.ByPids,
			nvidia.ByMinorID,
		)
		nodes = make([]*nvidia.NvidiaNode, 0)
		num   = int(cores / nvidia.HundredCore)
	)

	for next != candidate {
		next = candidate
		// 节点排序
		sorter.Sort(candidate.Children)

		for _, node := range candidate.Children {
			if len(node.Children) == 0 || node.Available() < num {
				continue
			}

			candidate = node
			klog.V(2).Infof("Choose id %d, mask %b", candidate.Meta.ID, candidate.Mask)
			break
		}
	}

	for _, node := range candidate.GetAvailableLeaves() {
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

type fragmentPriority struct {
	data []*nvidia.NvidiaNode
	less []nvidia.LessFunc
}

func fragmentSort(less ...nvidia.LessFunc) *fragmentPriority {
	return &fragmentPriority{
		less: less,
	}
}

func (fp *fragmentPriority) Sort(data []*nvidia.NvidiaNode) {
	fp.data = data
	sort.Sort(fp)
}

func (fp *fragmentPriority) Len() int {
	return len(fp.data)
}

func (fp *fragmentPriority) Swap(i, j int) {
	fp.data[i], fp.data[j] = fp.data[j], fp.data[i]
}

func (fp *fragmentPriority) Less(i, j int) bool {
	var k int

	for k = 0; k < len(fp.less)-1; k++ {
		less := fp.less[k]
		switch {
		case less(fp.data[i], fp.data[j]):
			return true
		case less(fp.data[j], fp.data[i]):
			return false
		}
	}

	return fp.less[k](fp.data[i], fp.data[j])
}
