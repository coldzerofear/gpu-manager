package runtime

import (
	criapi "k8s.io/cri-api/pkg/apis/runtime/v1"
)

type containerRuntimeManagerStub struct {
}

var _ ContainerRuntimeInterface = (*containerRuntimeManagerStub)(nil)

func NewContainerRuntimeManagerStub() *containerRuntimeManagerStub {
	return &containerRuntimeManagerStub{}
}

func (m *containerRuntimeManagerStub) GetPidsInContainerById(containerID string) ([]int, error) {
	return nil, nil
}

func (m *containerRuntimeManagerStub) GetPidsInContainerByStatus(containerStatus *criapi.ContainerStatus) ([]int, error) {
	return nil, nil
}

func (m *containerRuntimeManagerStub) InspectContainer(containerID string) (*criapi.ContainerStatus, error) {
	return nil, nil
}

func (m *containerRuntimeManagerStub) RuntimeName() string { return "" }
