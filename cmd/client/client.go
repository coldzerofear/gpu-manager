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

package main

import (
	"bufio"
	"context"
	goflag "flag"
	"github.com/spf13/pflag"
	"google.golang.org/grpc"
	"k8s.io/klog"
	"os"
	"strings"
	"time"
	vcudaapi "tkestack.io/gpu-manager/pkg/api/runtime/vcuda"
	"tkestack.io/gpu-manager/pkg/flags"
	"tkestack.io/gpu-manager/pkg/logs"
	"tkestack.io/gpu-manager/pkg/utils"
)

var (
	addr, busID, podUID, contName, contID, cgroupPath string
)

const TimeOut = 10 * time.Second
const UUIDLength = 32 + 4

func main() {
	cmdFlags := pflag.CommandLine

	cmdFlags.StringVar(&addr, "addr", "", "RPC address location for dial")
	cmdFlags.StringVar(&busID, "bus-id", "", "GPU card bus id of caller")
	cmdFlags.StringVar(&podUID, "pod-uid", "", "Pod UID of caller")
	cmdFlags.StringVar(&contName, "cont-name", "", "Container name of caller")
	cmdFlags.StringVar(&contID, "cont-id", "", "Container id of calller")
	cmdFlags.StringVar(&cgroupPath, "cgroup-path", "", "Cgroup Path of calller")

	flags.InitFlags()
	goflag.CommandLine.Parse([]string{})
	logs.InitLogs()
	defer logs.FlushLogs()

	if len(addr) == 0 {
		klog.Fatal("The rpc address cannot be empty")
	}

	if len(cgroupPath) == 0 {
		if len(podUID) == 0 || (len(contName) == 0 && len(contID) == 0) {
			klog.Fatalf("argument is empty, current: %s", cmdFlags.Args())
		}
	} else {
		//从cgroupPath提取 pod uid和container id
		memoryLine, err := readCgroupFileMemoryLine(cgroupPath)
		if err != nil {
			klog.Fatalf("read cgroup file failed, path: %s, err: %v", cgroupPath, err)
		}
		if len(memoryLine) == 0 {
			if len(podUID) == 0 || (len(contName) == 0 && len(contID) == 0) {
				klog.Fatalf("argument is empty, current: %s", cmdFlags.Args())
			}
			goto outer
		}
		containerId := ""
		if podUID, containerId = extract(memoryLine); len(podUID) == 0 || (len(contName) == 0 && len(containerId) == 0) {
			klog.Fatalf("parse cgroup file is failed, current: %s", cmdFlags.Args())
		}
		if len(contID) != 0 && contID != containerId {
			klog.Fatalf("Error parsing container id: src: %s, dest: %s", contID, containerId)
		}
		contID = containerId
	}
outer:
	// TODO 添加默认超时时间
	// https://github.com/tkestack/gpu-manager/commit/dd0e574a6aa416db2fdfcbfc6f8affac694c2a07
	options := append(utils.DefaultDialOptions, grpc.WithTimeout(TimeOut))
	conn, err := grpc.Dial(addr, options...)
	if err != nil {
		klog.Fatalf("can't dial %s, error %v", addr, err)
	}
	defer conn.Close()

	client := vcudaapi.NewVCUDAServiceClient(conn)

	req := &vcudaapi.VDeviceRequest{
		BusId:         busID,
		PodUid:        podUID,
		ContainerName: contName,
	}

	if len(contID) > 0 {
		req.ContainerName = ""
		req.ContainerId = contID
	}

	ctx, cancel := context.WithTimeout(context.Background(), TimeOut)
	defer cancel()

	_, err = client.RegisterVDevice(ctx, req)
	if err != nil {
		klog.Fatalf("fail to get response from manager, error %v", err)
	}
}

//func main() {
//	//str1 := "5:memory:/system.slice/containerd.service/kubepods-besteffort-pod95831d1c_3379_4239_8731_5d2e0f96cfca.slice:cri-containerd:0adf2e3d054e09ec61c2cf96dc6ade411954c18d1eaf2588c82994fb29f92a9f"
//	//str2 := "10:memory:/kubepods.slice/kubepods-besteffort.slice/kubepods-besteffort-pod2fa2470a_2263_4642_b28a_a4ac28efbcc8.slice/81da5b2eccf65ba293e06f496b8af89d61940b2df9f3a7910a9113ede82902f3"
//	//str3 := "11:memory:/kubepods.slice/kubepods-besteffort.slice/kubepods-besteffort-pode6fd5916_6562_42e6_b4cf_a99d5dcce655.slice/docker-963617c549dc6a7cc7157b326061a2ea25a1b9fcc843eddd6b8bc40c5de027fe.scope"
//	//uid, id := extract(str3)
//	//fmt.Println("podUid: ", uid, " containerId: ", id)
//	//line, err := readCgroupFileMemoryLine("C:\\Users\\COLDZEROFEAR\\.kube\\cgroup")
//	//if err != nil {
//	//	fmt.Println("读取文件失败")
//	//	os.Exit(1)
//	//}
//	//uid, id := extract(line)
//	//fmt.Println("podUid: ", uid, " containerId: ", id)
//
//	//str4 := "/" + path.Join("kubepods", "besteffort", "podb39963e8-cc41-4d44-912a-ed5394b6d4d5", "000000000000000000000000000000000")
//	//fmt.Println(str4)
//	//uid, id := extract(str4)
//	//fmt.Println("podUid: ", uid, " containerId: ", id)
//
//	str5 := "10:memory:/kubepods.slice/kubepods-besteffort.slice/kubepods-besteffort-podd98c80af_2009_4eef_9311_71beb2a1a577.slice/cri-containerd-8134e620c37afff34535d04db616284d8b14d659ef5a9fd3f5f6f12988bdfa21.scope"
//	uid, id := extract(str5)
//	fmt.Println("podUid: ", uid, " containerId: ", id)
//}

// TODO 根据k8s版本、容器运行时、cgroup驱动的不同可能会有变化，持续关注更新
func extract(memoryLine string) (podUid, containerId string) {
	// 1.27 containerd
	//5:memory:/system.slice/containerd.service/kubepods-besteffort-pod95831d1c_3379_4239_8731_5d2e0f96cfca.slice:cri-containerd: 0adf2e3d054e09ec61c2cf96dc6ade411954c18d1eaf2588c82994fb29f92a9f

	//10:memory:/kubepods.slice/kubepods-besteffort.slice/kubepods-besteffort-podd98c80af_2009_4eef_9311_71beb2a1a577.slice/cri-containerd-8134e620c37afff34535d04db616284d8b14d659ef5a9fd3f5f6f12988bdfa21.scope
	// 1.27 docker
	//10:memory:/kubepods.slice/kubepods-besteffort.slice/kubepods-besteffort-pod2fa2470a_2263_4642_b28a_a4ac28efbcc8.slice/81da5b2eccf65ba293e06f496b8af89d61940b2df9f3a7910a9113ede82902f3
	// 1.22 docker
	//11:memory:/kubepods.slice/kubepods-besteffort.slice/kubepods-besteffort-pode6fd5916_6562_42e6_b4cf_a99d5dcce655.slice/docker-963617c549dc6a7cc7157b326061a2ea25a1b9fcc843eddd6b8bc40c5de027fe.scope
	isSystemd := strings.Contains(memoryLine, ".slice")
	memoryLine = strings.TrimSpace(strings.ReplaceAll(memoryLine, ":", "/"))
	split := strings.Split(memoryLine, "/")
	for i, str := range split {
		if i == len(split)-1 {
			containerId = str
			containerId = strings.TrimSpace(containerId)
			containerId = strings.TrimSuffix(containerId, ".scope")
			if index := strings.LastIndex(containerId, "-"); index >= 0 {
				containerId = containerId[index+1:]
			}
		} else if isSystemd {
			if index := strings.Index(str, "-pod"); index >= 0 {
				str = str[index+4:]
				str = strings.TrimSuffix(str, ".slice")
				if len(str) == UUIDLength {
					podUid = strings.Replace(str, "_", "-", -1)
				}
			}
		} else if strings.HasPrefix(str, "pod") {
			// /kubepods/besteffort/podb39963e8-cc41-4d44-912a-ed5394b6d4d5/12da3d97e9069757d06fa9862b9e3f8d8555ff62281877333326a205ff283b50
			str = str[3:]
			if len(str) == UUIDLength {
				podUid = str
			}
		}
	}
	return
}

//func main() {
//	str5 := "10:memory:/kubepods.slice/kubepods-besteffort.slice/kubepods-besteffort-podd98c80af_2009_4eef_9311_71beb2a1a577.slice/cri-containerd-8134e620c37afff34535d04db616284d8b14d659ef5a9fd3f5f6f12988bdfa21.scope"
//	fmt.Println("containerId: ", getContainerByCgroup(str5))
//}

//func getContainerByCgroup(memoryLine string) string {
//	memoryLine = strings.TrimSpace(strings.ReplaceAll(memoryLine, ":", "/"))
//	split := strings.Split(memoryLine, "/")
//	containerId := ""
//	containerId = split[len(split)-1]
//	containerId = strings.TrimSpace(containerId)
//	containerId = strings.TrimSuffix(containerId, ".scope")
//	if index := strings.LastIndex(containerId, "-"); index >= 0 {
//		containerId = containerId[index+1:]
//	}
//	return containerId
//}

func readCgroupFileMemoryLine(filePath string) (string, error) {
	f, err := os.Open(filePath)
	memoryLine := ""
	if err != nil {
		klog.Errorf("can't read %s, %v", filePath, err)
		return memoryLine, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "memory") {
			memoryLine = line
			break
		}
	}
	return memoryLine, nil
}
