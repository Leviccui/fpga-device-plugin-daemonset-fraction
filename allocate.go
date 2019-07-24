package main

import (
	"fmt"
	_ "runtime/debug"
	"strconv"
	"time"

	log "github.com/Sirupsen/logrus"
	"golang.org/x/net/context"

	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	pluginapi "k8s.io/kubernetes/pkg/kubelet/apis/deviceplugin/v1beta1"
)

var (
	clientTimeout    = 30 * time.Second
	lastAllocateTime time.Time
)

// create docker client
func init() {
	kubeInit()
}

func Parser(fakeID string) (realID string) {
	fakeCounter := strconv.Itoa(BlockPerFPGA)
	return fakeID[:len(fakeID)-len(fakeCounter)-3]
}

func (m *FPGADevicePluginServer) GetDeviceByIndex(index uint) (dev Device, found bool) {
	if len(m.realDevices) == 0 {
		realDevicesByName := make(map[string]Device)
		for _, v := range m.devices {
			realDBDF := Parser(v.DBDF)
			if _, ok := realDevicesByName[realDBDF]; !ok {
				realDevicesByName[realDBDF] = Device{
					shellVer:  v.shellVer,
					timestamp: v.timestamp,
					DBDF:      realDBDF,
					deviceID:  v.deviceID,
					Healthy:   v.Healthy,
					Nodes:     v.Nodes,
				}
			}
		}

		j := uint(0)
		m.realDevices = make(map[uint]Device)
		for _, v := range realDevicesByName {
			m.realDevices[j] = v
			j++
		}
		log.Infof("Get realDevices: %v", m.realDevices)
	}

	if index >= 0 && index < uint(len(m.realDevices)) {
		return m.realDevices[index], true
	} else {
		return Device{}, false
	}
}

// Allocate which return list of devices.
func (m *FPGADevicePluginServer) Allocate(ctx context.Context, req *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	log.Debugf("In Allocate()")
	response := new(pluginapi.AllocateResponse)

	var (
		podReqFPGA uint
		found      bool
		assumePod  *v1.Pod
	)

	for _, req := range req.ContainerRequests {
		podReqFPGA += uint(len(req.DevicesIDs))
	}
	log.Infof("RequestPodFPGAs: %d", podReqFPGA)

	//m.Lock()
	//defer m.Unlock()
	log.Infof("enter getCandidatePod()")
	pods, err := getCandidatePods()
	log.Infof("exit getCandidatePod()")
	if err != nil {
		log.Infof("invalid allocation requst: Failed to find candidate pods due to %v", err)
		return nil, fmt.Errorf("getCandidatePods Error")
	}
	log.Debugln(pods)

	for _, pod := range pods {
		log.Debugf("Pod %s in ns %s request FPGAs %d with timestamp %v",
			pod.Name,
			pod.Namespace,
			getFPGALimitFromPodResource(pod),
			getAssumeTimeFromPodAnnotation(pod))
	}

	//find pod whosse required memory equals that of AllocateRequest
	//find the eailiest
	for _, pod := range pods {
		if getFPGALimitFromPodResource(pod) == podReqFPGA {
			log.Infof("Found Assumed FPGA Pod %s in ns %s with FPGA %d",
				pod.Name,
				pod.Namespace,
				podReqFPGA)
			assumePod = pod
			found = true
			break
		}
	}

	if found {
		//id is realID instead of fakeID
		id := getFPGAIDFromPodAnnotation(assumePod)
		if id < 0 {
			log.Infof("Failed to get the dev ", assumePod)
			return nil, fmt.Errorf("Failed to get the dev ", assumePod)
		}

		dev, ok := m.GetDeviceByIndex(uint(id))
		if !ok {
			log.Infof("Failed to find the dev for pod %v because it's not able to find dev with index %d", assumePod, id)
			return nil, fmt.Errorf("Failed to find the dev for pod %v because it's not able to find dev with index %d", assumePod, id)
		}

		log.Infoln(dev)

		// 1. Create container response
		for _, creq := range req.ContainerRequests {
			log.Debugf("Request IDs: %v", creq.DevicesIDs)
			cres := new(pluginapi.ContainerAllocateResponse)
			for i := 0; i < len(creq.DevicesIDs); i++ {
				// Before we have mgmt and user pf separated, we add both to the device cgroup.
				// It is still safe with mgmt pf assigned to container since xilinx device driver
				// makes sure flashing DSA(shell) through mgmt pf in container is denied.
				// This is not good. we will change that later, then only the user pf node is
				// required to be assigned to container(device cgroup of the container)
				//
				// When containers are on top of VM, it is possible only user PF is assigned
				// to VM, so the Mgmt is empty. Don't add it to cgroup in that case
				if dev.Nodes.Mgmt != "" {
					cres.Devices = append(cres.Devices, &pluginapi.DeviceSpec{
						HostPath:      dev.Nodes.Mgmt,
						ContainerPath: dev.Nodes.Mgmt,
						Permissions:   "rwm",
					})
					cres.Mounts = append(cres.Mounts, &pluginapi.Mount{
						HostPath:      dev.Nodes.Mgmt,
						ContainerPath: dev.Nodes.Mgmt,
						ReadOnly:      false,
					})
				}
				cres.Devices = append(cres.Devices, &pluginapi.DeviceSpec{
					HostPath:      dev.Nodes.User,
					ContainerPath: dev.Nodes.User,
					Permissions:   "rwm",
				})
				cres.Mounts = append(cres.Mounts, &pluginapi.Mount{
					HostPath:      dev.Nodes.User,
					ContainerPath: dev.Nodes.User,
					ReadOnly:      false,
				})
			}
			response.ContainerResponses = append(response.ContainerResponses, cres)
		}

		// 2. Update Pod spec
		newPod := updatePodAnnotations(assumePod)
		_, err = clientset.CoreV1().Pods(newPod.Namespace).Update(newPod)
		if err != nil {
			// the object has been modified; please apply your changes to the latest version and try again
			if err.Error() == OptimisticLockErrorMsg {
				// retry
				pod, err := clientset.CoreV1().Pods(assumePod.Namespace).Get(assumePod.Name, metav1.GetOptions{})
				if err != nil {
					log.Infof("Failed due to %v", err)
					return nil, fmt.Errorf("Failed due to %v", err)
				}
				newPod = updatePodAnnotations(pod)
				_, err = clientset.CoreV1().Pods(newPod.Namespace).Update(newPod)
				if err != nil {
					log.Infof("Failed due to %v", err)
					return nil, fmt.Errorf("Failed due to %v", err)
				}
			} else {
				log.Infof("Failed due to %v", err)
				return nil, fmt.Errorf("Failed due to %v", err)
			}
		}
	} else {
		log.Infof("invalid allocation requst: request FPGA %d can't be satisfied", podReqFPGA)
		return nil, fmt.Errorf("invalid allocation requst: request FPGA %d can't be satisfied", podReqFPGA)
	}
	log.Infof("allocate successfully")
	return response, nil
}
