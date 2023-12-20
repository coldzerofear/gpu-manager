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

package watchdog

import (
	"context"
	"encoding/json"
	"github.com/NVIDIA/go-nvml/pkg/nvml"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"os"
	"regexp"
	"strings"
	"time"
	"tkestack.io/gpu-manager/pkg/utils"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	v1core "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/klog"
)

const (
	gpuModelLabel = "gaia.nvidia.com/gpu-model"
)

type labelFunc interface {
	GetLabel() string
}

type nodeLabeler struct {
	hostName    string
	client      v1core.CoreV1Interface
	labelMapper map[string]labelFunc
}

type modelFunc struct{}
type stringFunc string

var modelFn = modelFunc{}

// 获取节点设备名称
// TODO 设想宿主机安装的同一种设备比如 4张 A00,且每张显存一致, 不会出现gpu混用的情况
func (m modelFunc) GetLabel() (model string) {
	if rs := nvml.Init(); rs != nvml.SUCCESS {
		klog.Warningf("Can't initialize nvml library, %s", nvml.ErrorString(rs))
		return
	}
	defer nvml.Shutdown()
	count, rs := nvml.DeviceGetCount()
	if rs != nvml.SUCCESS {
		klog.Warningf("Can't get device count, %s", nvml.ErrorString(rs))
		return
	}
	// 使用 set 用于去重
	gpuTypes := sets.NewString()
	for index := 0; index < count; index++ {
		dev, rs := nvml.DeviceGetHandleByIndex(index)
		if rs != nvml.SUCCESS {
			klog.Warningf("Can't get device %d information, %s", index, nvml.ErrorString(rs))
			continue
		}
		rawName, rs := dev.GetName()
		if rs != nvml.SUCCESS {
			klog.Warningf("Can't get device %d name, %s", index, nvml.ErrorString(rs))
			continue
		}
		if typeName := getTypeName(rawName); len(typeName) > 0 {
			gpuTypes.Insert(typeName)
		}
	}

	typeNames := strings.Join(gpuTypes.List(), ",")

	klog.V(4).Infof("GPU name: %s", typeNames)

	return typeNames
}

func (s stringFunc) GetLabel() string {
	return string(s)
}

var modelNameSplitPattern = regexp.MustCompile("\\s+")

func getTypeName(name string) string {
	// 以空格作为分隔符切割
	splits := modelNameSplitPattern.Split(name, -1)

	if len(splits) >= 2 {
		return splits[1]
	}

	klog.V(4).Infof("GPU name splits: %v", splits)

	return ""
}

// NewNodeLabeler returns a new nodeLabeler
func NewNodeLabeler(client v1core.CoreV1Interface, hostname string, labels map[string]string) *nodeLabeler {
	if len(hostname) == 0 {
		hostname, _ = os.Hostname()
	}

	klog.V(2).Infof("Labeler for hostname %s", hostname)

	labelMapper := make(map[string]labelFunc)
	for k, v := range labels {
		if k == gpuModelLabel {
			labelMapper[k] = modelFn
		} else {
			labelMapper[k] = stringFunc(v)
		}
	}

	return &nodeLabeler{
		hostName:    hostname,
		client:      client,
		labelMapper: labelMapper,
	}
}

func (nl *nodeLabeler) Run() error {
	// TODO 更改为patch模式，增强性能
	err := wait.PollUntilContextTimeout(context.Background(), time.Second, time.Minute, true,
		func(ctx context.Context) (bool, error) {
			labels := make(map[string]string)
			for k, fn := range nl.labelMapper {
				l := fn.GetLabel()
				if len(l) == 0 {
					klog.Warningf("Empty label for %s", k)
					continue
				}

				klog.V(2).Infof("Label %s %s=%s", nl.hostName, k, l)
				labels[k] = l
			}
			// 只有label存在才有更新的意义
			if len(labels) > 0 {
				patch := utils.NewPatchLabel(labels)
				bytes, err := json.Marshal(patch)
				if err != nil {
					return false, err
				}
				_, patchErr := nl.client.Nodes().Patch(ctx, nl.hostName, types.StrategicMergePatchType, bytes, metav1.PatchOptions{})
				// 更新节点label
				if patchErr != nil {
					if errors.IsConflict(patchErr) {
						return false, nil
					}
					return true, patchErr
				}
			}
			return true, nil
		},
	)

	if err != nil {
		return err
	}

	klog.V(2).Infof("Auto label is running")

	return nil
}
