package runtime

import (
	"fmt"
	"github.com/opencontainers/runc/libcontainer/cgroups"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/net/context"
	"google.golang.org/grpc"
	v1 "k8s.io/api/core/v1"
	criapi "k8s.io/cri-api/pkg/apis/runtime/v1"
	"k8s.io/klog"
	"k8s.io/kubectl/pkg/util/qos"

	"tkestack.io/gpu-manager/pkg/services/watchdog"
	"tkestack.io/gpu-manager/pkg/types"
	"tkestack.io/gpu-manager/pkg/utils"
	"tkestack.io/gpu-manager/pkg/utils/cgroup"
)

type ContainerRuntimeInterface interface {
	// Get pids in the given container id
	GetPidsInContainerById(containerID string) ([]int, error)
	// Get pids in the given container status
	GetPidsInContainerByStatus(containerStatus *criapi.ContainerStatus) ([]int, error)
	// InspectContainer returns the container information by the given name
	InspectContainer(containerID string) (*criapi.ContainerStatus, error)
	// RuntimeName returns the container runtime name
	RuntimeName() string
}

type containerRuntimeManager struct {
	cgroupDriver   string
	runtimeName    string
	requestTimeout time.Duration
	client         criapi.RuntimeServiceClient
}

var _ ContainerRuntimeInterface = (*containerRuntimeManager)(nil)

var (
	containerRoot = cgroup.NewCgroupName([]string{}, "kubepods")
)

func NewContainerRuntimeManager(cgroupDriver, endpoint string, requestTimeout time.Duration) (*containerRuntimeManager, error) {
	dialOptions := []grpc.DialOption{grpc.WithInsecure(), grpc.WithDialer(utils.UnixDial), grpc.WithBlock(), grpc.WithTimeout(time.Second * 5)}
	conn, err := grpc.Dial(endpoint, dialOptions...)
	if err != nil {
		return nil, err
	}

	client := criapi.NewRuntimeServiceClient(conn)

	m := &containerRuntimeManager{
		cgroupDriver:   cgroupDriver,
		client:         client,
		requestTimeout: requestTimeout,
	}

	ctx, cancel := context.WithTimeout(context.Background(), m.requestTimeout)
	defer cancel()
	resp, err := client.Version(ctx, &criapi.VersionRequest{Version: "0.1.0"})
	if err != nil {
		return nil, err
	}

	klog.V(2).Infof("Container runtime is %s", resp.RuntimeName)
	m.runtimeName = resp.RuntimeName

	return m, nil
}

func (m *containerRuntimeManager) GetPidsInContainerByStatus(containerStatus *criapi.ContainerStatus) ([]int, error) {
	ns := containerStatus.Labels[types.PodNamespaceLabelKey]
	podName := containerStatus.Labels[types.PodNameLabelKey]

	pod, err := watchdog.GetPod(ns, podName)
	if err != nil {
		klog.Errorf("can't get pod %s/%s, %v", ns, podName, err)
		return nil, err
	}
	// 获取容器cgroup路径
	oldVersion := false
outer:
	cgroupPath, err := m.getCgroupName(pod, containerStatus.Id, oldVersion)
	if err != nil {
		klog.Errorf("can't get cgroup parent, %v", err)
		return nil, err
	}

	if !strings.HasPrefix(cgroupPath, types.CGROUP_BASE) {
		cgroupPath = filepath.Join(types.CGROUP_MEMORY, cgroupPath)
	}
	baseDir := filepath.Clean(cgroupPath)
	// 校验cgroup文件路径是否存在
	if _, err := os.Stat(baseDir); os.IsNotExist(err) && !oldVersion {
		oldVersion = true
		goto outer
	}

	pids, err := cgroups.GetAllPids(baseDir)
	if err != nil {
		klog.Errorf("can't get container pids, containerId: %s, baseDir: %s, err: %v", containerStatus.Id, baseDir, err)
		return pids, err
	}
	// 遍历cgroup路径下每一个文件并执行 func
	//filepath.Walk(baseDir, func(path string, info os.FileInfo, err error) error {
	//	if info == nil {
	//		return nil
	//	}
	//	// 当文件名不为cgroup.procs 则跳过
	//	if info.IsDir() || info.Name() != types.CGROUP_PROCS {
	//		return nil
	//	}
	//	// 读取文件中记录的pid
	//	p, err := readProcsFile(path)
	//	if err == nil {
	//		pids = append(pids, p...)
	//	}
	//
	//	return nil
	//})

	return pids, nil
}

func (m *containerRuntimeManager) GetPidsInContainerById(containerID string) ([]int, error) {
	containerStatus, err := m.InspectContainer(containerID)
	if err != nil {
		klog.Errorf("can't get container %s status, %v", containerID, err)
		return nil, err
	}
	return m.GetPidsInContainerByStatus(containerStatus)
}

//func readProcsFile(file string) ([]int, error) {
//	f, err := os.Open(file)
//	if err != nil {
//		klog.Errorf("can't read %s, %v", file, err)
//		return nil, nil
//	}
//	defer f.Close()
//
//	scanner := bufio.NewScanner(f)
//	pids := make([]int, 0)
//	for scanner.Scan() {
//		line := scanner.Text()
//		if pid, err := strconv.Atoi(line); err == nil {
//			pids = append(pids, pid)
//		}
//	}
//
//	klog.V(4).Infof("Read from %s, pids: %v", file, pids)
//	return pids, nil
//}

/*
*
Ubuntu20.04.2 LTS  k8s 1.27, containerd 路径：
/sys/fs/cgroup/memory/system.slice/containerd.service/kubepods-burstable-pod00b50989_1d00_4d7c_904f_84bcbc28d719.slice:cri-containerd:99b40d83503dd15f7f68348b43d8a310641357635358debfb6a8dcb8b571cba5

Ubuntu20.04.2 LTS  k8s 1.27, docker 路径：
/sys/fs/cgroup/memory/kubepods.slice/kubepods-besteffort.slice/kubepods-besteffort-pod0086d247_7fe3_4d93_a3fb_976a918797d7.slice/8f5fc244fa3dd18ff8739fcdefa060d05d0da4725a6f2be4f2d40f8934d7151a

CentOS7  k8s 1.22, docker 路径：
/sys/fs/cgroup/memory/kubepods.slice/kubepods-besteffort.slice/kubepods-besteffort-pod7a1b3765_65c8_4382_9555_552f8f3ac63c.slice/docker-8a485a37020ed0f9226d5deb47e5e9570bca62b73adf75617ec2c776b84a713b.scope
*/

// 1.27 containerd
// 1.27 docker
// 1.22 docker
//
//5:memory:/system.slice/containerd.service/kubepods-besteffort-pod95831d1c_3379_4239_8731_5d2e0f96cfca.slice:cri-containerd: 0adf2e3d054e09ec61c2cf96dc6ade411954c18d1eaf2588c82994fb29f92a9f
//10:memory:/kubepods.slice/kubepods-besteffort.slice/kubepods-besteffort-pod2fa2470a_2263_4642_b28a_a4ac28efbcc8.slice/81da5b2eccf65ba293e06f496b8af89d61940b2df9f3a7910a9113ede82902f3
//11:memory:/kubepods.slice/kubepods-besteffort.slice/kubepods-besteffort-pode6fd5916_6562_42e6_b4cf_a99d5dcce655.slice/docker-963617c549dc6a7cc7157b326061a2ea25a1b9fcc843eddd6b8bc40c5de027fe.scope
// 1.26 containerd
//10:memory:/kubepods.slice/kubepods-besteffort.slice/kubepods-besteffort-podd98c80af_2009_4eef_9311_71beb2a1a577.slice/cri-containerd-8134e620c37afff34535d04db616284d8b14d659ef5a9fd3f5f6f12988bdfa21.scope

func convertCgroupPathBySystemd(runtimeName, containerId string, cgroupName cgroup.CgroupName, oldVersion bool) string {
	switch runtimeName {
	case "containerd":
		if oldVersion {
			return fmt.Sprintf("%s/%s-%s.scope", cgroupName.ToSystemd(), cgroup.SystemdPathPrefixOfRuntime(runtimeName), containerId)
		}
		return fmt.Sprintf("system.slice/%s.service/%s:%s:%s", runtimeName, cgroupName.ToSystemd2(), cgroup.SystemdPathPrefixOfRuntime(runtimeName), containerId)
	case "docker":
		if oldVersion {
			return fmt.Sprintf("%s/%s-%s.scope", cgroupName.ToSystemd(), cgroup.SystemdPathPrefixOfRuntime(runtimeName), containerId)
		}
		return fmt.Sprintf("%s/%s", cgroupName.ToSystemd(), containerId)
	default:
		return fmt.Sprintf("%s/%s-%s.scope", cgroupName.ToSystemd(), cgroup.SystemdPathPrefixOfRuntime(runtimeName), containerId)
	}
}

func (m *containerRuntimeManager) getCgroupName(pod *v1.Pod, containerID string, oldVersion bool) (string, error) {
	podQos := pod.Status.QOSClass
	if len(podQos) == 0 {
		podQos = qos.GetPodQOS(pod)
	}

	var parentContainer cgroup.CgroupName
	switch podQos {
	case v1.PodQOSGuaranteed:
		parentContainer = cgroup.NewCgroupName(containerRoot)
	case v1.PodQOSBurstable:
		parentContainer = cgroup.NewCgroupName(containerRoot, strings.ToLower(string(v1.PodQOSBurstable)))
	case v1.PodQOSBestEffort:
		parentContainer = cgroup.NewCgroupName(containerRoot, strings.ToLower(string(v1.PodQOSBestEffort)))
	}
	// {"kubepods", "besteffort", "podb39963e8-cc41-4d44-912a-ed5394b6d4d5"}
	podContainer := types.PodCgroupNamePrefix + string(pod.UID)
	cgroupName := cgroup.NewCgroupName(parentContainer, podContainer)

	switch m.cgroupDriver {
	case "systemd":
		// <kubepods.slice/kubepods-burstable.slice/kubepods-burstable-podb39963e8_cc41_4d44_912a_ed5394b6d4d5.slice>/cri-containerd-12da3d97e9069757d06fa9862b9e3f8d8555ff62281877333326a205ff283b50.scope
		return convertCgroupPathBySystemd(m.runtimeName, containerID, cgroupName, oldVersion), nil
	case "cgroupfs":
		// /kubepods/besteffort/podb39963e8-cc41-4d44-912a-ed5394b6d4d5/12da3d97e9069757d06fa9862b9e3f8d8555ff62281877333326a205ff283b50
		return fmt.Sprintf("%s/%s", cgroupName.ToCgroupfs(), containerID), nil
	default:
	}

	return "", fmt.Errorf("unsupported cgroup driver")
}

func (m *containerRuntimeManager) InspectContainer(containerID string) (*criapi.ContainerStatus, error) {
	//	containerID = m.sanitizeContainerID(containerID)
	req := &criapi.ContainerStatusRequest{
		ContainerId: containerID,
	}

	ctx, cancel := context.WithTimeout(context.Background(), m.requestTimeout)
	defer cancel()
	// 调用cri获取容器状态
	resp, err := m.client.ContainerStatus(ctx, req)
	if err != nil {
		return nil, err
	}

	return resp.Status, nil
}

func (m *containerRuntimeManager) RuntimeName() string { return m.runtimeName }

// TODO 修复容器崩溃
// https://github.com/paragor/gpu-manager/commit/33d09cd22d9488b6a2abc66a0dec8786c427a757
//func (m *containerRuntimeManager) sanitizeContainerID(containerID string) string {
//	if strings.HasPrefix(containerID, "containerd-") {
//		containerID = strings.TrimPrefix(containerID, "containerd-")
//	} else if strings.HasPrefix(containerID, "cri-containerd-") {
//		containerID = strings.TrimPrefix(containerID, "cri-containerd-")
//	}
//	return containerID
//}
