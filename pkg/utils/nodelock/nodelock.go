package nodelock

import (
	"context"
	"fmt"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	NodeLockTime = "tydic.io/node-lock"
	MaxLockRetry = 5
)

func setNodeLock(nodeName string, client kubernetes.Interface, ctx context.Context) error {
	node, err := client.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if _, ok := node.ObjectMeta.Annotations[NodeLockTime]; ok {
		return fmt.Errorf("node %s is locked", nodeName)
	}
	newNode := node.DeepCopy()
	newNode.ObjectMeta.Annotations[NodeLockTime] = fmt.Sprintf("%d", time.Now().UnixNano())
	_, err = client.CoreV1().Nodes().Update(ctx, newNode, metav1.UpdateOptions{})
	for i := 0; i < MaxLockRetry && err != nil; i++ {
		klog.ErrorS(err, "Failed to update node", "node", nodeName, "retry", i)
		time.Sleep(100 * time.Millisecond)
		node, err = client.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		if err != nil {
			klog.ErrorS(err, "Failed to get node when retry to update", "node", nodeName)
			continue
		}
		newNode := node.DeepCopy()
		newNode.ObjectMeta.Annotations[NodeLockTime] = fmt.Sprintf("%d", time.Now().UnixNano())
		_, err = client.CoreV1().Nodes().Update(ctx, newNode, metav1.UpdateOptions{})
	}
	if err != nil {
		return fmt.Errorf("setNodeLock exceeds retry count %d", MaxLockRetry)
	}
	klog.InfoS("Node lock set", "node", nodeName)
	return nil
}

// 释放节点锁 默认重试5次
func ReleaseNodeLock(nodeName string, client kubernetes.Interface) error {
	ctx := context.Background()
	node, err := client.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if _, ok := node.ObjectMeta.Annotations[NodeLockTime]; !ok {
		klog.InfoS("Node lock not set", "node", nodeName)
		return nil
	}
	newNode := node.DeepCopy()
	delete(newNode.ObjectMeta.Annotations, NodeLockTime)
	_, err = client.CoreV1().Nodes().Update(ctx, newNode, metav1.UpdateOptions{})
	for i := 0; i < MaxLockRetry && err != nil; i++ {
		klog.ErrorS(err, "Failed to update node", "node", nodeName, "retry", i)
		time.Sleep(100 * time.Millisecond)
		node, err = client.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		if err != nil {
			klog.ErrorS(err, "Failed to get node when retry to update", "node", nodeName)
			continue
		}
		newNode := node.DeepCopy()
		delete(newNode.ObjectMeta.Annotations, NodeLockTime)
		_, err = client.CoreV1().Nodes().Update(ctx, newNode, metav1.UpdateOptions{})
	}
	if err != nil {
		return fmt.Errorf("releaseNodeLock exceeds retry count %d", MaxLockRetry)
	}
	klog.InfoS("Node lock released", "node", nodeName)
	return nil
}

// 当前节点加锁
func LockNode(nodeName string, client kubernetes.Interface) error {
	ctx := context.Background()
	node, err := client.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	// 判断注解的锁标记是否存在，不存在则给node加锁
	nanoStr, ok := node.ObjectMeta.Annotations[NodeLockTime]
	if !ok {
		return setNodeLock(nodeName, client, ctx)
	}
	// 存在锁标记则解析锁
	nano, err := strconv.ParseInt(nanoStr, 10, 64)
	if err != nil {
		// 时间戳解析失败报错
		return err
	}
	lockTime := time.Unix(0, nano)
	// 计算锁时间超过5分钟则释放
	if time.Since(lockTime) > time.Minute*5 {
		klog.InfoS("Node lock expired", "node", nodeName, "lockTime", lockTime)
		// 释放节点上的锁
		if err = ReleaseNodeLock(nodeName, client); err != nil {
			klog.ErrorS(err, "Failed to release node lock", "node", nodeName)
			return err
		}
		// 释放后再加锁
		return setNodeLock(nodeName, client, ctx)
	}
	return fmt.Errorf("node %s has been locked within 5 minutes", nodeName)
}
