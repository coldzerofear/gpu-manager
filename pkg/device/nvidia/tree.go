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
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"tkestack.io/gpu-manager/pkg/config"
	"tkestack.io/gpu-manager/pkg/device"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
	"k8s.io/klog"
)

const (
	//MaxProcess is the Maximum number of process in one device.
	MaxProcess = 64
	//NamePattern is the name pattern of nvidia device.
	NamePattern = "/dev/nvidia%d"
	one         = uint32(1)
	levelStep   = 10
	//HundredCore represents 100 virtual cores.
	HundredCore = 100
)

// LevelMap is a map stores NvidiaNode on each level.
type LevelMap map[nvml.GpuTopologyLevel][]*NvidiaNode

// NvidiaTree represents a Nvidia GPU in a tree.
type NvidiaTree struct {
	sync.Mutex

	root   *NvidiaNode
	leaves []*NvidiaNode

	realMode     bool
	query        map[string]*NvidiaNode
	index        int
	samplePeriod time.Duration
}

func init() {
	device.Register("nvidia", NewNvidiaTree)
}

// NewNvidiaTree returns a new NvidiaTree.
func NewNvidiaTree(cfg *config.Config) device.GPUTree {
	tree := newNvidiaTree(cfg)

	return tree
}

func newNvidiaTree(cfg *config.Config) *NvidiaTree {
	tree := &NvidiaTree{
		query: make(map[string]*NvidiaNode),
		index: 0,
	}

	if cfg != nil {
		tree.samplePeriod = cfg.SamplePeriod
	}

	return tree
}

// Init a NvidiaTree.
// Will try to use nvml first, fallback to input string if
// parseFromLibrary() failed.
func (t *NvidiaTree) Init(input string) {
	// 尝试调用nvml库
	err := t.parseFromLibrary()
	if err == nil {
		t.realMode = true
		return
	}

	klog.V(2).Infof("Can't use nvidia library, err %s. Use text parser", err)
	// 调用nvml失败则从input中解析读取
	if err := t.parseFromString(input); err != nil {
		klog.Fatalf("Can not initialize nvidia tree, err %s", err)
	}
}

// Update NvidiaTree by info getting from GPU devices.
// Return immediately if real GPU device is not available.
func (t *NvidiaTree) Update() {
	if !t.realMode {
		return
	}

	if rs := nvml.Init(); rs != nvml.SUCCESS {
		return
	}
	defer nvml.Shutdown()

	klog.V(4).Infof("Update device information")

	t.Lock()
	defer t.Unlock()

	for i := range t.Leaves() {
		node := t.updateNode(i)

		if node.pendingReset && node.AllocatableMeta.Cores == HundredCore {
			resetGPUFeature(node, t.realMode)

			if !node.pendingReset {
				t.freeNode(node)
			}
		}

		klog.V(4).Infof("node %d, pid: %+v, memory: %+v, utilization: %+v, pendingReset: %+v",
			i, node.Meta.Pids, node.Meta.UsedMemory, node.Meta.Utilization, node.pendingReset)

		node = node.Parent
		for node != nil {
			node.Meta.Pids = make([]uint, 0)
			node.Meta.UsedMemory = 0
			node.Meta.TotalMemory = 0

			for _, child := range node.Children {
				node.Meta.Pids = append(node.Meta.Pids, child.Meta.Pids...)
				node.Meta.UsedMemory += child.Meta.UsedMemory
				node.Meta.TotalMemory += child.Meta.TotalMemory
			}

			node = node.Parent
		}
	}
}

func (t *NvidiaTree) allocateNode(index int) *NvidiaNode {
	node := NewNvidiaNode(t)

	node.ntype = nvml.TOPOLOGY_INTERNAL
	node.Meta.ID = index
	node.Mask = one << uint(index)

	return node
}

func (t *NvidiaTree) addNode(node *NvidiaNode) {
	t.query[node.MinorName()] = node
	t.leaves[node.Meta.ID] = node
}

func (t *NvidiaTree) parseFromLibrary() error {
	// 初始化nvml
	if rs := nvml.Init(); rs != nvml.SUCCESS {
		return errors.New(nvml.ErrorString(rs))
	}

	defer nvml.Shutdown()
	// 获取设备数
	num, rs := nvml.DeviceGetCount()
	if rs != nvml.SUCCESS {
		return errors.New(nvml.ErrorString(rs))
	}

	klog.V(2).Infof("Detect %d gpu cards", num)

	nodes := make(LevelMap)
	t.leaves = make([]*NvidiaNode, num)
	// 装填nvidia node 信息
	for i := 0; i < num; i++ {
		// 更具设备index获取设备
		dev, _ := nvml.DeviceGetHandleByIndex(i)
		// 获取设备内存使用信息
		memInfo, _ := dev.GetMemoryInfo_v2()
		// 获取设备pci信息
		pciInfo, _ := dev.GetPciInfo()
		// 获取设备编号
		minorID, _ := dev.GetMinorNumber()
		// 获取设备uuid
		uuid, _ := dev.GetUUID()
		// 设备名称/显卡类型
		name, _ := dev.GetName()
		// 创建NvidiaNode
		n := t.allocateNode(i)
		// vcudaCores初始化100
		n.AllocatableMeta.Cores = HundredCore
		// 初始化设备总内存
		n.AllocatableMeta.Memory = int64(memInfo.Total)
		n.Meta.TotalMemory = memInfo.Total
		n.Meta.BusId = B2S(pciInfo.BusId[:])
		n.Meta.MinorID = minorID
		n.Meta.UUID = uuid
		n.Meta.Name = name
		t.addNode(n)
	}
	// 装填设备拓扑信息，用于多设备分配最近的
	for cardA := 0; cardA < num; cardA++ {
		// 获取设备a
		devA, _ := nvml.DeviceGetHandleByIndex(cardA)
		for cardB := cardA + 1; cardB < num; cardB++ {
			// 获取设备b
			devB, _ := nvml.DeviceGetHandleByIndex(cardB)
			// 获取设备a、设备b的拓扑公共祖先
			ntype, rs := nvml.DeviceGetTopologyCommonAncestor(devA, devB)
			if rs != nvml.SUCCESS {
				return errors.New(nvml.ErrorString(rs))
			}

			multi, rs := devA.GetMultiGpuBoard()
			if rs != nvml.SUCCESS {
				return errors.New(nvml.ErrorString(rs))
			}

			if multi > 0 && ntype == nvml.TOPOLOGY_INTERNAL {
				ntype = nvml.TOPOLOGY_SINGLE
			}

			if newNode := t.join(nodes, ntype, cardA, cardB); newNode != nil {
				klog.V(2).Infof("New node, type %d, mask %b", int(ntype), newNode.Mask)
				nodes[ntype] = append(nodes[ntype], newNode)
			}
		}
	}

	for t, ns := range nodes {
		klog.V(2).Infof("type: %d, len %d", int(t), len(ns))
	}

	t.buildTree(nodes)

	return nil
}

func (t *NvidiaTree) parseFromString(input string) error {
	if input == "" {
		return fmt.Errorf("no input")
	}

	scanner := bufio.NewScanner(strings.NewReader(input))
	count := -1
	nodes := make(LevelMap)
	splitter := regexp.MustCompile("[ \t]+")

	// Example:
	//       GPU0 GPU1 GPU2 GPU3
	// GPU0   X   PIX  PHB  PHB
	// ...
	for scanner.Scan() {
		count++
		text := scanner.Text()

		// Create all card nodes
		if count == 0 {
			num := len(trimEmpty(splitter.Split(text, -1)))
			t.leaves = make([]*NvidiaNode, num)

			for i := 0; i < int(num); i++ {
				n := t.allocateNode(i)
				n.Meta.MinorID = i

				t.addNode(n)
			}

			continue
		}

		cardA := count - 1

		// According to the link type, join nodes together
		for i, str := range trimEmpty(splitter.Split(text, -1)) {
			if i == 0 || i == count {
				continue
			}

			cardB := i - 1
			ntype := parseToGpuTopologyLevel(str)
			if newNode := t.join(nodes, ntype, cardA, cardB); newNode != nil {
				nodes[ntype] = append(nodes[ntype], newNode)
			}
		}
	}

	t.buildTree(nodes)

	return nil
}

func (t *NvidiaTree) buildTree(nodes LevelMap) {
	// Create connections
	for _, cur := range t.leaves {
		level := int(nvml.TOPOLOGY_SINGLE)
		self := cur

		for {
			for _, upperNode := range nodes[nvml.GpuTopologyLevel(level)] {
				if (upperNode.Mask & self.Mask) != 0 {
					self.setParent(upperNode)
					self = upperNode
					break
				}
			}

			level += levelStep

			if level > int(nvml.TOPOLOGY_SYSTEM) {
				break
			}
		}
	}

	// Find the root level
	var firstLevel []*NvidiaNode
	level := int(nvml.TOPOLOGY_SYSTEM)

	t.root = NewNvidiaNode(t)
	t.root.Parent = nil
	for level > 0 {
		if len(nodes[nvml.GpuTopologyLevel(level)]) == 0 {
			level -= levelStep
			continue
		}

		firstLevel = nodes[nvml.GpuTopologyLevel(level)]
		break
	}

	if len(firstLevel) == 0 {
		klog.Errorf("No topology level found at %d", level)

		if len(t.leaves) == 1 {
			klog.Infof("Only one card topology")
			t.root.Mask |= t.leaves[0].Mask
			t.leaves[0].setParent(t.root)

			t.root.Children = append(t.root.Children, t.leaves[0])
			return
		}

		klog.Fatalf("Should not reach here")
	}

	for _, n := range firstLevel {
		t.root.Mask |= n.Mask
		n.setParent(t.root)
	}

	// Transform vchildren to children
	for _, n := range t.leaves {
		cur := n.Parent

		for cur != nil {
			if len(cur.Children) == 0 {
				cur.Children = make([]*NvidiaNode, 0)

				for _, child := range cur.vchildren {
					cur.Children = append(cur.Children, child)
				}
			}

			cur = cur.Parent
		}
	}
}

func trimEmpty(splits []string) []string {
	var data []string

	for _, s := range splits {
		if len(s) != 0 {
			data = append(data, s)
		}
	}

	return data
}

func (t *NvidiaTree) join(nodes LevelMap, ntype nvml.GpuTopologyLevel, indexA, indexB int) *NvidiaNode {
	klog.V(5).Infof("Join %d and %d in type %d", indexA, indexB, int(ntype))
	nodeA, nodeB := t.leaves[indexA], t.leaves[indexB]
	mask := nodeA.Mask | nodeB.Mask
	list := nodes[ntype]

	for _, n := range list {
		if (n.Mask & mask) != 0 {
			n.Mask |= mask
			klog.V(5).Infof("Join to mask %b", n.Mask)
			return nil
		}
	}

	newNode := NewNvidiaNode(t)

	newNode.Mask = mask
	newNode.ntype = ntype

	return newNode
}

// Available returns number of available leaves of this tree.
func (t *NvidiaTree) Available() int {
	t.Lock()
	defer t.Unlock()

	return t.root.Available()
}

// MarkFree updates a NvidiaNode by freeing request cores and memory.
// If request cores < HundredCore, plus available cores and memory with request value.
// If request cores >= HundredCore, set available cores and memory to total,
// and update mask of all parents of this node.
func (t *NvidiaTree) MarkFree(node *NvidiaNode, util int64, memory int64) {
	t.Lock()
	defer t.Unlock()

	n, ok := t.query[node.MinorName()]
	if !ok {
		klog.V(2).Infof("Can not find node with name(%s)", node.MinorName())
		return
	}

	klog.V(2).Infof("Free %s with %d %d", n.MinorName(), util, memory)
	// exclusive mode
	if util >= HundredCore {
		klog.V(2).Infof("%s cores %d->%d", n.MinorName(), n.AllocatableMeta.Cores, HundredCore)
		n.AllocatableMeta.Cores = HundredCore
		klog.V(2).Infof("%s memory %d->%d", n.MinorName(), n.AllocatableMeta.Memory, n.Meta.TotalMemory)
		n.AllocatableMeta.Memory = int64(n.Meta.TotalMemory)
	} else {
		klog.V(2).Infof("%s cores %d->%d", n.MinorName(), n.AllocatableMeta.Cores, n.AllocatableMeta.Cores+util)
		n.AllocatableMeta.Cores += util
		if n.AllocatableMeta.Cores > HundredCore {
			n.AllocatableMeta.Cores = HundredCore
		}

		n.AllocatableMeta.Memory += memory
		klog.V(2).Infof("%s memory %d->%d", n.MinorName(), n.AllocatableMeta.Memory, n.AllocatableMeta.Memory+memory)
		if n.AllocatableMeta.Memory > int64(n.Meta.TotalMemory) {
			n.AllocatableMeta.Memory = int64(n.Meta.TotalMemory)
		}
	}

	if n.AllocatableMeta.Cores == HundredCore {
		if t.realMode {
			n.pendingReset = true
			// We need to clear user settings
			if err := resetGPUFeature(n, t.realMode); err != nil {
				klog.Warningf("can't reset GPU %s, %v", n.Meta.BusId, err)
			}

			if n.pendingReset {
				klog.Warningf("GPU %s has some functional error, waiting for reset", n.Meta.BusId)
				return
			}
		}

		klog.V(2).Infof("Free %s, mask %b", n.MinorName(), n.Mask)
		t.freeNode(n)
	}
}

func (t *NvidiaTree) freeNode(n *NvidiaNode) {
	for p := n.Parent; p != nil; p = p.Parent {
		klog.V(2).Infof("Free %s parent %b", n.MinorName(), p.Mask)
		p.Mask |= n.Mask
	}
}

// MarkOccupied updates a NvidiaNode by adding request cores and memory.
// Mask of all parents of this node will be updated.
// If request cores < HundredCore, minus available cores and memory with request value.
// If request cores >= HundredCore, set available cores and memory to 0,
func (t *NvidiaTree) MarkOccupied(node *NvidiaNode, util int64, memory int64) {
	t.Lock()
	defer t.Unlock()

	n, ok := t.query[node.MinorName()]
	if !ok {
		klog.V(2).Infof("Can not find node with name(%s)", node.MinorName())
		return
	}

	klog.V(2).Infof("Occupy %s with %d %d, mask %b", n.MinorName(), util, memory, n.Mask)
	t.occupyNode(n)

	// exclusive mode
	if util >= HundredCore {
		klog.V(2).Infof("%s cores %d->%d", n.MinorName(), n.AllocatableMeta.Cores, 0)
		klog.V(2).Infof("%s memory %d->%d", n.MinorName(), n.AllocatableMeta.Memory, 0)
		n.AllocatableMeta.Cores = 0
		n.AllocatableMeta.Memory = 0
	} else {
		klog.V(2).Infof("%s cores %d->%d", n.MinorName(), n.AllocatableMeta.Cores, n.AllocatableMeta.Cores-util)
		n.AllocatableMeta.Cores -= util
		if n.AllocatableMeta.Cores < 0 {
			n.AllocatableMeta.Cores = 0
		}

		klog.V(2).Infof("%s memory %d->%d", n.MinorName(), n.AllocatableMeta.Memory, n.AllocatableMeta.Memory-memory)
		n.AllocatableMeta.Memory -= memory
		if n.AllocatableMeta.Memory < 0 {
			n.AllocatableMeta.Memory = 0
		}
	}
}

func (t *NvidiaTree) occupyNode(n *NvidiaNode) {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Mask&n.Mask == n.Mask {
			klog.V(2).Infof("Occupy %s parent %b", n.MinorName(), p.Mask)
			p.Mask ^= n.Mask
		}
	}
}

// Leaves returns leaves of tree
func (t *NvidiaTree) Leaves() []*NvidiaNode {
	return t.leaves
}

// Leaves returns leaves of tree
func (t *NvidiaTree) GetLeaveMaxTotalMemory() uint64 {
	maxTotalMemory := uint64(0)
	for _, node := range t.leaves {
		maxTotalMemory = Max(maxTotalMemory, node.Meta.TotalMemory)
	}
	return maxTotalMemory
}

func Max(a, b uint64) uint64 {
	if a >= b {
		return a
	}
	return b
}

// Total returns count of leaves
func (t *NvidiaTree) Total() int {
	return len(t.leaves)
}

// Root returns root node of tree
func (t *NvidiaTree) Root() *NvidiaNode {
	return t.root
}

// Query tries to find node by name, return nil if not found
func (t *NvidiaTree) Query(name string) *NvidiaNode {
	n, ok := t.query[name]
	if !ok {
		klog.V(5).Infof("Can not find node with name(%s)", name)
		return nil
	}

	return n
}

// PrintGraph returns the details of tree as string
func (t *NvidiaTree) PrintGraph() string {
	var (
		buf bytes.Buffer
	)

	buf.WriteString(fmt.Sprintf("%s:%d\n", t.root.String(), t.root.Available()))
	printIter(&buf, t.root, int(nvml.TOPOLOGY_INTERNAL))

	output := buf.String()

	return output
}

func (t *NvidiaTree) updateNode(idx int) *NvidiaNode {
	nvml.Init()
	defer nvml.Shutdown()

	dev, _ := nvml.DeviceGetHandleByIndex(idx)
	lastUtilizationTimestamp := uint64(time.Now().Add(-1 * t.samplePeriod).UnixMicro())
	processes, _ := dev.GetProcessUtilization(lastUtilizationTimestamp)

	//proceInfo, _ := dev.GetComputeRunningProcesses()
	//util, _ := dev.GetAverageGPUUsage(t.samplePeriod)

	node := t.leaves[idx]

	node.Meta.Pids = make([]uint, 0)
	node.Meta.UsedMemory = 0

	for _, process := range processes {
		node.Meta.Pids = append(node.Meta.Pids, uint(process.Pid))
		node.Meta.UsedMemory += uint64(process.MemUtil)
		node.Meta.Utilization += uint(process.SmUtil)
	}
	return node
}

func printIter(w *bytes.Buffer, node *NvidiaNode, level int) {
	for i := int(nvml.TOPOLOGY_INTERNAL) + levelStep; i < level; i += levelStep {
		w.WriteString("|   ")
	}

	if level > 0 {
		w.WriteString(fmt.Sprintf("|---"))
		w.WriteString(printNode(node))
	}

	PrintSorter.Sort(node.Children)

	for _, next := range node.Children {
		printIter(w, next, level+levelStep)
	}
}

func printNode(node *NvidiaNode) string {
	if node.ntype != nvml.TOPOLOGY_INTERNAL {
		return fmt.Sprintf("%s (aval: %d, pids: %+v, usedMemory: %d, totalMemory: %d, allocatableCores: %d, allocatableMemory: %d)\n",
			node.String(), node.Available(), node.Meta.Pids, node.Meta.UsedMemory, node.Meta.TotalMemory,
			node.AllocatableMeta.Cores, node.AllocatableMeta.Memory)
	}

	return fmt.Sprintf("%s (pids: %+v, usedMemory: %d, totalMemory: %d, allocatableCores: %d, allocatableMemory: %d)\n",
		node.String(), node.Meta.Pids, node.Meta.UsedMemory, node.Meta.TotalMemory,
		node.AllocatableMeta.Cores, node.AllocatableMeta.Memory)
}

func resetGPUFeature(node *NvidiaNode, realMode bool) error {
	if !node.pendingReset {
		return nil
	}

	if !realMode {
		node.pendingReset = false
		return nil
	}

	// skip reset if we have running processes
	if len(node.Meta.Pids) > 0 || node.Meta.UsedMemory > 0 {
		node.pendingReset = false
		return nil
	}

	if rs := nvml.Init(); rs != nvml.SUCCESS {
		return errors.New(nvml.ErrorString(rs))
	}

	defer nvml.Shutdown()

	// GPU in the real world has a BusId
	if len(node.Meta.BusId) > 0 {
		dev, _ := nvml.DeviceGetHandleByIndex(node.Meta.ID)
		if rs := dev.SetComputeMode(nvml.COMPUTEMODE_DEFAULT); rs != nvml.SUCCESS {
			err := errors.New(nvml.ErrorString(rs))
			klog.V(3).Infof("can't set compute mode to default for %s, %v", node.Meta.BusId, err)
			return err
		}

		curMode, _, rs := dev.GetEccMode()
		if rs != nvml.SUCCESS {
			err := errors.New(nvml.ErrorString(rs))
			// If we got Not Supported error, that means this GPU card is not enabled for ECC
			if strings.Contains(err.Error(), "Not Supported") {
				node.pendingReset = false
				return nil
			}

			klog.V(3).Infof("can't get ecc mode for %s, %v", node.Meta.BusId, err)
			return err
		}

		if curMode == nvml.FEATURE_ENABLED {
			if rs = dev.ClearEccErrorCounts(nvml.VOLATILE_ECC); rs != nvml.SUCCESS {
				err := errors.New(nvml.ErrorString(rs))
				klog.V(3).Infof("can't clear volatile ecc for %s, %v", node.Meta.BusId, err)
				return err
			}
			if rs = dev.ClearEccErrorCounts(nvml.AGGREGATE_ECC); rs != nvml.SUCCESS {
				err := errors.New(nvml.ErrorString(rs))
				klog.V(3).Infof("can't clear volatile ecc for %s, %v", node.Meta.BusId, err)
				return err
			}
		}
	}

	node.pendingReset = false

	return nil
}
