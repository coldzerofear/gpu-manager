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
	"strings"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
	"k8s.io/klog"
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
