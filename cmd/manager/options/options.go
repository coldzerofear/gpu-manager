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

package options

import (
	"k8s.io/klog"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
	"os"
	"strings"
	"time"

	"github.com/spf13/pflag"
)

const (
	DefaultDriver                = "nvidia"
	DefaultQueryPort             = 5678
	DefaultSamplePeriod          = 1
	DefaultDeviceMemoryScaling   = 1
	DefaultVirtualManagerPath    = "/etc/gpu-manager/vm"
	DefaultDeviceConfig          = "/etc/gpu-manager/config/config.json"
	DefaultAllocationCheckPeriod = 30
	DefaultCheckpointPath        = "/etc/gpu-manager/checkpoint"

	DefaultKubeletConfig = "/var/lib/kubelet/config.yaml"

	DockerShimRuntimeEndpoint = "/var/run/dockershim.sock"
	DockerCriRuntimeEndpoint  = "/var/run/cri-dockerd.sock"
	ContainerdRuntimeEndpoint = "/var/run/containerd/containerd.sock"
)

// TODO 不断添加兼容列表
var ContainerRuntimeCompatibilityList = []string{
	DockerShimRuntimeEndpoint,
	DockerCriRuntimeEndpoint,
	ContainerdRuntimeEndpoint,
}

// Options contains plugin information
type Options struct {
	Driver                   string
	ExtraPath                string
	VolumeConfigPath         string
	QueryPort                int
	QueryAddr                string
	KubeConfigFile           string
	SamplePeriod             int
	NodeLabels               string
	HostnameOverride         string
	VirtualManagerPath       string
	DevicePluginPath         string
	EnableShare              bool
	AllocationCheckPeriod    int
	DeviceMemoryScaling      float64
	CheckpointPath           string
	ContainerRuntimeEndpoint string
	CgroupDriver             string
	RequestTimeout           time.Duration
	WaitTimeout              time.Duration
}

// NewOptions gives a default options template.
func NewOptions() *Options {
	return &Options{
		Driver:                   DefaultDriver,
		QueryPort:                DefaultQueryPort,
		QueryAddr:                "localhost",
		SamplePeriod:             DefaultSamplePeriod,
		VirtualManagerPath:       DefaultVirtualManagerPath,
		AllocationCheckPeriod:    DefaultAllocationCheckPeriod,
		DeviceMemoryScaling:      DefaultDeviceMemoryScaling,
		CheckpointPath:           DefaultCheckpointPath,
		ContainerRuntimeEndpoint: getDefaultRuntimeEndpoint(),
		CgroupDriver:             getDefaultCgroupDriver(),
		RequestTimeout:           time.Second * 5,
		WaitTimeout:              time.Minute,
		DevicePluginPath:         pluginapi.DevicePluginPath,
		HostnameOverride:         os.Getenv("NODE_NAME"),
	}
}

// TODO 兼容containerd配置, 当找不到 dockershim.sock, 默认值改为containerd.sock默认路径
// https://github.com/tkestack/gpu-manager/commit/42599a6880513e6c683bbdd40022fffc8973a634
func getDefaultRuntimeEndpoint() string {
	for _, endpoint := range ContainerRuntimeCompatibilityList {
		if _, err := os.Stat(endpoint); os.IsNotExist(err) {
			klog.Warning(endpoint, " is not exist, skip it")
		} else {
			klog.Info("automatically recognized endpoint %s", endpoint)
			return endpoint
		}
	}
	return ""
}

func getDefaultCgroupDriver() string {
	if fileContext, err := os.ReadFile(DefaultKubeletConfig); err != nil {
		klog.Warning("read ", DefaultDeviceConfig, " file failed: %s", err.Error())
	} else {
		kubeletConfig := string(fileContext)
		if strings.LastIndex(kubeletConfig, "cgroupDriver:") > 0 {
			content := strings.ToLower(kubeletConfig)
			if strings.Contains(content, "systemd") {
				return "systemd"
			}
			if strings.Contains(content, "cgroupfs") {
				return "cgroupfs"
			}
		}
	}
	klog.Warning("cgroup driver not automatically recognized")
	return ""
}

// AddFlags add some commandline flags.
func (opt *Options) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&opt.Driver, "driver", opt.Driver, "The driver name for manager")
	fs.StringVar(&opt.ExtraPath, "extra-config", opt.ExtraPath, "The extra config file location")
	fs.StringVar(&opt.VolumeConfigPath, "volume-config", opt.VolumeConfigPath, "The volume config file location")
	fs.IntVar(&opt.QueryPort, "query-port", opt.QueryPort, "port for query statistics information")
	fs.StringVar(&opt.QueryAddr, "query-addr", opt.QueryAddr, "address for query statistics information")
	fs.StringVar(&opt.KubeConfigFile, "kubeconfig", opt.KubeConfigFile, "Path to kubeconfig file with authorization information (the master location is set by the master flag).")
	fs.IntVar(&opt.SamplePeriod, "sample-period", opt.SamplePeriod, "Sample period for each card, unit second")
	fs.StringVar(&opt.NodeLabels, "node-labels", opt.NodeLabels, "automated label for this node, if empty, node will be only labeled by gpu model")
	fs.StringVar(&opt.HostnameOverride, "hostname-override", opt.HostnameOverride, "If non-empty, will use this string as identification instead of the actual hostname.")
	fs.StringVar(&opt.VirtualManagerPath, "virtual-manager-path", opt.VirtualManagerPath, "configuration path for virtual manager store files")
	fs.StringVar(&opt.DevicePluginPath, "device-plugin-path", opt.DevicePluginPath, "the path for kubelet receive device plugin registration")
	fs.StringVar(&opt.CheckpointPath, "checkpoint-path", opt.CheckpointPath, "configuration path for checkpoint store file")
	fs.BoolVar(&opt.EnableShare, "share-mode", opt.EnableShare, "enable share mode allocation")
	fs.IntVar(&opt.AllocationCheckPeriod, "allocation-check-period", opt.AllocationCheckPeriod, "allocation check period, unit second")
	fs.Float64Var(&opt.DeviceMemoryScaling, "device-memory-scaling", opt.DeviceMemoryScaling, "define device memory scaling ratio")
	fs.StringVar(&opt.ContainerRuntimeEndpoint, "container-runtime-endpoint", opt.ContainerRuntimeEndpoint, "container runtime endpoint")
	fs.StringVar(&opt.CgroupDriver, "cgroup-driver", opt.CgroupDriver, "Driver that the kubelet uses to manipulate cgroups on the host.  "+
		"Possible values: 'cgroupfs', 'systemd'")
	fs.DurationVar(&opt.RequestTimeout, "runtime-request-timeout", opt.RequestTimeout,
		"request timeout for communicating with container runtime endpoint")
	fs.DurationVar(&opt.WaitTimeout, "wait-timeout", opt.WaitTimeout, "wait timeout for resource server ready")
}
