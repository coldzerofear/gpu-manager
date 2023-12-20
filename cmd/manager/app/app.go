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

package app

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tkestack.io/gpu-manager/cmd/manager/options"
	"tkestack.io/gpu-manager/pkg/config"
	"tkestack.io/gpu-manager/pkg/server"
	"tkestack.io/gpu-manager/pkg/types"
	"tkestack.io/gpu-manager/pkg/utils"

	"github.com/fsnotify/fsnotify"
	"k8s.io/klog"
)

// #lizard forgives
func Run(opt *options.Options) error {
	cfg := &config.Config{
		Driver:       opt.Driver,
		QueryPort:    opt.QueryPort,
		QueryAddr:    opt.QueryAddr,
		KubeConfig:   opt.KubeConfigFile,
		SamplePeriod: time.Duration(opt.SamplePeriod) * time.Second,
		// vcuda 请求队列，由一个信道构成，在设备分配完毕之后，容器启动前的阶段执行
		VCudaRequestsQueue:       make(chan *types.VCudaRequest, 10),
		DevicePluginPath:         opt.DevicePluginPath,
		VirtualManagerPath:       opt.VirtualManagerPath,
		VolumeConfigPath:         opt.VolumeConfigPath,
		EnableShare:              opt.EnableShare,
		AllocationCheckPeriod:    time.Duration(opt.AllocationCheckPeriod) * time.Second,
		CheckpointPath:           opt.CheckpointPath,
		ContainerRuntimeEndpoint: opt.ContainerRuntimeEndpoint,
		CgroupDriver:             opt.CgroupDriver,
		RequestTimeout:           opt.RequestTimeout,
		DeviceMemoryScaling:      opt.DeviceMemoryScaling,
		Hostname:                 opt.HostnameOverride,
		ExtraConfigPath:          opt.ExtraPath,
	}

	cfg.NodeLabels = make(map[string]string)
	for _, item := range strings.Split(opt.NodeLabels, ",") {
		if len(item) > 0 {
			kvs := strings.SplitN(item, "=", 2)
			if len(kvs) == 2 {
				cfg.NodeLabels[kvs[0]] = kvs[1]
			} else {
				klog.Warningf("malformed node labels: %v", kvs)
			}
		}
	}

	// TODO 加载额外配置文件，为不同节点配置差异信息
	if err := readFromConfigFile(cfg); err != nil {
		return err
	}
	// TODO 校验配置信息
	if err := checkConfig(cfg); err != nil {
		return err
	}

	srv := server.NewManager(cfg)
	go srv.Run()
	// 根据配置创建计时器
	waitTimer := time.NewTimer(opt.WaitTimeout)
	// 等待捆绑服务运行成功
	for !srv.Ready() {
		select {
		case <-waitTimer.C:
			// 等待超时,返回码1重启服务
			klog.Warningf("Wait too long for server ready, restarting")
			os.Exit(1)
		default:
			klog.Infof("Wait for internal server ready")
		}
		time.Sleep(time.Second)
	}
	waitTimer.Stop()

	if err := srv.RegisterToKubelet(); err != nil {
		return err
	}

	devicePluginSocket := filepath.Join(cfg.DevicePluginPath, types.KubeletSocket)
	watcher, err := utils.NewFSWatcher(cfg.DevicePluginPath)
	if err != nil {
		log.Println("Failed to created FS watcher.")
		os.Exit(1)
	}
	defer watcher.Close()

	for {
		select {
		case event := <-watcher.Events:
			if event.Name == devicePluginSocket && event.Op&fsnotify.Create == fsnotify.Create {
				time.Sleep(time.Second)
				klog.Fatalf("inotify: %s created, restarting.", devicePluginSocket)
			}
		case err := <-watcher.Errors:
			klog.Fatalf("inotify: %s", err)
		}
	}
	return nil
}

func checkConfig(cfg *config.Config) error {
	// 校验hostname
	if len(cfg.Hostname) == 0 {
		return fmt.Errorf("config hostname cannot be empty, please check if environment variables are set {HOST_NAME} or set start command --hostname-override")
	}
	// 校验容器运行时
	if len(cfg.ContainerRuntimeEndpoint) > 0 {
		endpoint := cfg.ContainerRuntimeEndpoint
		conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: endpoint, Net: "unix"})
		if err != nil {
			klog.Errorf("verify runtime endpoint connection failed, endpoint: %s, err : %v", endpoint, err)
			return err
		}
		defer func() {
			if err := conn.Close(); err != nil {
				klog.Warningf("closing runtime connection failed, endpoint: %s, err : %v", endpoint, err)
				_ = conn.Close()
			}
		}()
	} else {
		return fmt.Errorf("no matching found container runtime endpoint")
	}

	// 校验cgroup驱动
	if err := checkCgroupDriver(cfg.CgroupDriver); err != nil {
		klog.Warningf("verify cgroup driver failed, err : %v", err)
		return err
	}

	// TODO 未来升级虚拟显存实现后可以去掉限制
	if cfg.DeviceMemoryScaling > 1 {
		return fmt.Errorf("device memory hyperallocation is not yet supported")
	} else if cfg.DeviceMemoryScaling < 0 {
		return fmt.Errorf("device memory scaling only supports any number between 0 and 1")
	}
	return nil
}

func checkCgroupDriver(cgroupDriver string) error {
	switch strings.ToLower(cgroupDriver) {
	case "cgroupfs", "systemd":
		return nil
	default:
		return fmt.Errorf("unknown cgroup driver %s, only support [ cgroupfs | systemd ]", cgroupDriver)
	}
}

func readFromConfigFile(cfg *config.Config) error {
	jsonByte, err := os.ReadFile(options.DefaultDeviceConfig)
	if err != nil {
		if os.IsNotExist(err) {
			klog.V(2).Info("device config.json not found use default config, path: %s", options.DefaultDeviceConfig)
			return nil
		}
		return err
	}
	var deviceConfigs config.NodeConfigs
	if err = json.Unmarshal(jsonByte, &deviceConfigs); err != nil {
		klog.V(4).Infof("Deserialization config.json failed, err: %v", err)
		return err
	}
	klog.V(2).Info("load node config: ", deviceConfigs)
	for _, val := range deviceConfigs.NodeConfig {
		if strings.Compare(os.Getenv("NODE_NAME"), val.Name) == 0 {
			klog.V(2).Info("reading node config from file ", val.Name)
			if len(val.CgroupDriver) > 0 {
				cfg.CgroupDriver = val.CgroupDriver
			}
			if len(val.ContainerRuntimeEndpoint) > 0 {
				cfg.ContainerRuntimeEndpoint = val.ContainerRuntimeEndpoint
			}
			if val.DeviceMemoryScaling > 0 {
				cfg.DeviceMemoryScaling = val.DeviceMemoryScaling
			}
		}
	}
	return nil
}
