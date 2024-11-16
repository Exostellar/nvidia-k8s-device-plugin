/*
 * Copyright (c) 2019, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package plugin

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	cdiapi "tags.cncf.io/container-device-interface/pkg/cdi"

	spec "github.com/NVIDIA/k8s-device-plugin/api/config/v1"
	"github.com/NVIDIA/k8s-device-plugin/cmd/mps-control-daemon/mps"
	"github.com/NVIDIA/k8s-device-plugin/internal/cdi"
	"github.com/NVIDIA/k8s-device-plugin/internal/rm"

	"github.com/google/uuid"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

// Constants for use by the 'volume-mounts' device list strategy
const (
	deviceListAsVolumeMountsHostPath          = "/dev/null"
	deviceListAsVolumeMountsContainerPathRoot = "/var/run/nvidia-container-devices"
)

// NvidiaDevicePlugin implements the Kubernetes device plugin API
type NvidiaDevicePlugin struct {
	rm                   rm.ResourceManager
	config               *spec.Config
	deviceListEnvvar     string
	deviceListStrategies spec.DeviceListStrategies
	socket               string

	cdiHandler          cdi.Interface
	cdiEnabled          bool
	cdiAnnotationPrefix string

	server *grpc.Server
	health chan *rm.Device
	stop   chan interface{}

	mpsDaemon   *mps.Daemon
	mpsHostRoot mps.Root
}

// NewNvidiaDevicePlugin returns an initialized NvidiaDevicePlugin
func NewNvidiaDevicePlugin(config *spec.Config, resourceManager rm.ResourceManager, cdiHandler cdi.Interface) (*NvidiaDevicePlugin, error) {
	_, name := resourceManager.Resource().Split()

	deviceListStrategies, _ := spec.NewDeviceListStrategies(*config.Flags.Plugin.DeviceListStrategy)

	pluginName := "nvidia-" + name
	pluginPath := filepath.Join(pluginapi.DevicePluginPath, pluginName)

	var mpsDaemon *mps.Daemon
	var mpsHostRoot mps.Root
	if config.Sharing.SharingStrategy() == spec.SharingStrategyMPS {
		// TODO: It might make sense to pull this logic into a resource manager.
		for _, device := range resourceManager.Devices() {
			if device.IsMigDevice() {
				return nil, errors.New("sharing using MPS is not supported for MIG devices")
			}
		}
		mpsDaemon = mps.NewDaemon(resourceManager, mps.ContainerRoot)
		mpsHostRoot = mps.Root(*config.Flags.CommandLineFlags.MpsRoot)
	}

	plugin := NvidiaDevicePlugin{
		rm:                   resourceManager,
		config:               config,
		deviceListEnvvar:     "NVIDIA_VISIBLE_DEVICES",
		deviceListStrategies: deviceListStrategies,
		socket:               pluginPath + ".sock",
		cdiHandler:           cdiHandler,
		cdiAnnotationPrefix:  *config.Flags.Plugin.CDIAnnotationPrefix,

		mpsDaemon:   mpsDaemon,
		mpsHostRoot: mpsHostRoot,

		// These will be reinitialized every
		// time the plugin server is restarted.
		server: nil,
		health: nil,
		stop:   nil,
	}
	return &plugin, nil
}

func (plugin *NvidiaDevicePlugin) initialize() {
	plugin.server = grpc.NewServer([]grpc.ServerOption{}...)
	plugin.health = make(chan *rm.Device)
	plugin.stop = make(chan interface{})
}

func (plugin *NvidiaDevicePlugin) cleanup() {
	close(plugin.stop)
	plugin.server = nil
	plugin.health = nil
	plugin.stop = nil
}

// Devices returns the full set of devices associated with the plugin.
func (plugin *NvidiaDevicePlugin) Devices() rm.Devices {
	return plugin.rm.Devices()
}

// Start starts the gRPC server, registers the device plugin with the Kubelet,
// and starts the device healthchecks.
func (plugin *NvidiaDevicePlugin) Start() error {
	plugin.initialize()

	if err := plugin.waitForMPSDaemon(); err != nil {
		return fmt.Errorf("error waiting for MPS daemon: %w", err)
	}

	err := plugin.Serve()
	if err != nil {
		klog.Infof("Could not start device plugin for '%s': %s", plugin.rm.Resource(), err)
		plugin.cleanup()
		return err
	}
	klog.Infof("Starting to serve '%s' on %s", plugin.rm.Resource(), plugin.socket)

	err = plugin.Register()
	if err != nil {
		klog.Infof("Could not register device plugin: %s", err)
		return errors.Join(err, plugin.Stop())
	}
	klog.Infof("Registered device plugin for '%s' with Kubelet", plugin.rm.Resource())

	go func() {
		// TODO: add MPS health check
		err := plugin.rm.CheckHealth(plugin.stop, plugin.health)
		if err != nil {
			klog.Infof("Failed to start health check: %v; continuing with health checks disabled", err)
		}
	}()

	return nil
}

func (plugin *NvidiaDevicePlugin) waitForMPSDaemon() error {
	if plugin.config.Sharing.SharingStrategy() != spec.SharingStrategyMPS {
		return nil
	}
	// TODO: Check the .ready file here.
	// TODO: Have some retry strategy here.
	if err := plugin.mpsDaemon.AssertHealthy(); err != nil {
		return fmt.Errorf("error checking MPS daemon health: %w", err)
	}
	klog.InfoS("MPS daemon is healthy", "resource", plugin.rm.Resource())
	return nil
}

// Stop stops the gRPC server.
func (plugin *NvidiaDevicePlugin) Stop() error {
	if plugin == nil || plugin.server == nil {
		return nil
	}
	klog.Infof("Stopping to serve '%s' on %s", plugin.rm.Resource(), plugin.socket)
	plugin.server.Stop()
	if err := os.Remove(plugin.socket); err != nil && !os.IsNotExist(err) {
		return err
	}
	plugin.cleanup()
	return nil
}

// Serve starts the gRPC server of the device plugin.
func (plugin *NvidiaDevicePlugin) Serve() error {
	os.Remove(plugin.socket)
	sock, err := net.Listen("unix", plugin.socket)
	if err != nil {
		return err
	}

	pluginapi.RegisterDevicePluginServer(plugin.server, plugin)

	go func() {
		lastCrashTime := time.Now()
		restartCount := 0

		for {
			// quite if it has been restarted too often
			// i.e. if server has crashed more than 5 times and it didn't last more than one hour each time
			if restartCount > 5 {
				// quit
				klog.Fatalf("GRPC server for '%s' has repeatedly crashed recently. Quitting", plugin.rm.Resource())
			}

			klog.Infof("Starting GRPC server for '%s'", plugin.rm.Resource())
			err := plugin.server.Serve(sock)
			if err == nil {
				break
			}

			klog.Infof("GRPC server for '%s' crashed with error: %v", plugin.rm.Resource(), err)

			timeSinceLastCrash := time.Since(lastCrashTime).Seconds()
			lastCrashTime = time.Now()
			if timeSinceLastCrash > 3600 {
				// it has been one hour since the last crash.. reset the count
				// to reflect on the frequency
				restartCount = 0
			} else {
				restartCount++
			}
		}
	}()

	// Wait for server to start by launching a blocking connection
	conn, err := plugin.dial(plugin.socket, 5*time.Second)
	if err != nil {
		return err
	}
	conn.Close()

	return nil
}

// Register registers the device plugin for the given resourceName with Kubelet.
func (plugin *NvidiaDevicePlugin) Register() error {
	conn, err := plugin.dial(pluginapi.KubeletSocket, 5*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()

	client := pluginapi.NewRegistrationClient(conn)
	reqt := &pluginapi.RegisterRequest{
		Version:      pluginapi.Version,
		Endpoint:     path.Base(plugin.socket),
		ResourceName: string(plugin.rm.Resource()),
		Options: &pluginapi.DevicePluginOptions{
			GetPreferredAllocationAvailable: true,
		},
	}

	_, err = client.Register(context.Background(), reqt)
	if err != nil {
		return err
	}
	return nil
}

// GetDevicePluginOptions returns the values of the optional settings for this plugin
func (plugin *NvidiaDevicePlugin) GetDevicePluginOptions(context.Context, *pluginapi.Empty) (*pluginapi.DevicePluginOptions, error) {
	options := &pluginapi.DevicePluginOptions{
		GetPreferredAllocationAvailable: true,
	}
	return options, nil
}

// ListAndWatch lists devices and update that list according to the health status
func (plugin *NvidiaDevicePlugin) ListAndWatch(e *pluginapi.Empty, s pluginapi.DevicePlugin_ListAndWatchServer) error {
	if err := s.Send(&pluginapi.ListAndWatchResponse{Devices: plugin.apiDevices()}); err != nil {
		return err
	}

	for {
		select {
		case <-plugin.stop:
			return nil
		case d := <-plugin.health:
			// FIXME: there is no way to recover from the Unhealthy state.
			d.Health = pluginapi.Unhealthy
			klog.Infof("'%s' device marked unhealthy: %s", plugin.rm.Resource(), d.ID)
			if err := s.Send(&pluginapi.ListAndWatchResponse{Devices: plugin.apiDevices()}); err != nil {
				return nil
			}
		}
	}
}

// func (plugin *NvidiaDevicePlugin) GetPreferredAllocation(ctx context.Context, r *pluginapi.PreferredAllocationRequest) (*pluginapi.PreferredAllocationResponse, error) {
//     klog.Info("GetPreferredAllocation called")
//     response := &pluginapi.PreferredAllocationResponse{}

//     // Loop through each container's allocation request
//     for _, req := range r.ContainerRequests {
//         klog.Infof("Processing ContainerPreferredAllocationRequest: AvailableDeviceIDs=%v, MustIncludeDeviceIDs=%v, AllocationSize=%d",
//             req.AvailableDeviceIDs, req.MustIncludeDeviceIDs, req.AllocationSize)

//         // Extract pod information from context or set defaults
//         podNamespace := "default"
//         podName := ctx.Value("podName").(string) // Ensure podName is passed in context
//         klog.Infof("Fetching pod metadata for PodName=%s, Namespace=%s", podName, podNamespace)

//         // Fetch the pod object from Kubernetes API
//         pod, err := plugin.kubeClient.CoreV1().Pods(podNamespace).Get(ctx, podName, metav1.GetOptions{})
//         if err != nil {
//             klog.Errorf("Failed to fetch pod metadata: %v", err)
//             return nil, fmt.Errorf("failed to fetch pod metadata: %v", err)
//         }

//         // Log fetched pod annotations
//         klog.Infof("Pod Annotations: %v", pod.Annotations)

//         // Parse custom parameter from annotations
//         numberOfRealGPUs := 1 // Default value
//         if val, ok := pod.Annotations["gpu.allocation/number-of-real-gpus"]; ok {
//             parsedValue, parseErr := strconv.Atoi(val)
//             if parseErr != nil {
//                 klog.Errorf("Invalid number-of-real-gpus annotation value: %s, error: %v", val, parseErr)
//             } else {
//                 numberOfRealGPUs = parsedValue
//                 klog.Infof("Parsed numberOfRealGPUs from annotations: %d", numberOfRealGPUs)
//             }
//         } else {
//             klog.Warning("No number-of-real-gpus annotation found. Using default value of 1")
//         }

//         // Use custom parameter in preferred allocation logic
//         devices, err := plugin.rm.GetPreferredAllocation(req.AvailableDeviceIDs, req.MustIncludeDeviceIDs, numberOfRealGPUs)
//         if err != nil {
//             klog.Errorf("Error during preferred allocation: %v", err)
//             return nil, fmt.Errorf("error getting list of preferred allocation devices: %v", err)
//         }

//         // Log selected devices
//         klog.Infof("Selected devices for allocation: %v", devices)

//         // Construct response for this container
//         resp := &pluginapi.ContainerPreferredAllocationResponse{
//             DeviceIDs: devices,
//         }
//         response.ContainerResponses = append(response.ContainerResponses, resp)
//     }

//     klog.Info("GetPreferredAllocation completed successfully")
//     return response, nil
// }

// func testAPI() {
// 	fmt.Printf("\n\ntestAPI\n")
// 	// creates the in-cluster config
// 	config, err := rest.InClusterConfig()
// 	if err != nil {
// 		panic(err.Error())
// 	}
// 	// creates the clientset
// 	clientset, err := kubernetes.NewForConfig(config)
// 	if err != nil {
// 		panic(err.Error())
// 	}

// 	// get pods in all the namespaces by omitting namespace
// 	// Or specify namespace to get pods in particular namespace
// 	pods, err := clientset.CoreV1().Pods("").List(context.TODO(), metav1.ListOptions{})
// 	if err != nil {
// 		panic(err.Error())
// 	}
// 	fmt.Printf("There are %d pods in the cluster\n", len(pods.Items))
// 	fmt.Printf("Pods %v\n", pods)
// }

// func getNodeNameFromAPI() (string, error) {
// 	fmt.Println("Starting getNodeNameFromAPI")

// 	// Step 1: Create in-cluster Kubernetes config
// 	config, err := rest.InClusterConfig()
// 	if err != nil {
// 		fmt.Printf("Error creating in-cluster config: %v\n", err)
// 		return "", err
// 	}
// 	fmt.Println("Successfully created in-cluster config")

// 	// Step 2: Create a Kubernetes client
// 	clientset, err := kubernetes.NewForConfig(config)
// 	if err != nil {
// 		fmt.Printf("Error creating Kubernetes client: %v\n", err)
// 		return "", err
// 	}
// 	fmt.Println("Successfully created Kubernetes client")

// 	// Step 3: Get the pod name
// 	podName, err := os.Hostname()
// 	if err != nil {
// 		fmt.Printf("Error getting hostname: %v\n", err)
// 		return "", err
// 	}
// 	fmt.Printf("Pod name: %s\n", podName)

// 	podNamespace := "nvidia-device-plugin"

// 	// Step 5: Fetch the pod information
// 	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
// 	defer cancel()

// 	fmt.Println("Fetching pod information")
// 	pod, err := clientset.CoreV1().Pods(podNamespace).Get(ctx, podName, metav1.GetOptions{})
// 	if err != nil {
// 		fmt.Printf("Error getting pod: %v\n", err)
// 		return "", err
// 	}

// 	fmt.Printf("Successfully fetched pod information. Node name: %s\n", pod.Spec.NodeName)
// 	return pod.Spec.NodeName, nil
// }

// func matchDeviceIDs(deviceIDs []string, annotation string) bool {
// 	if annotation == "" {
// 		return false
// 	}

// 	// Parse the annotation into a list of device IDs (assume comma-separated)
// 	annotatedIDs := strings.Split(annotation, ",")

// 	// Check if all requested device IDs are present in the annotation
// 	for _, requestedID := range deviceIDs {
// 		found := false
// 		for _, annotatedID := range annotatedIDs {
// 			if requestedID == annotatedID {
// 				found = true
// 				break
// 			}
// 		}
// 		if !found {
// 			return false
// 		}
// 	}
// 	return true
// }

// func findPodForDeviceIDs(deviceIDs []string) (*v1.Pod, error) {
// 	// Create in-cluster Kubernetes config
// 	config, err := rest.InClusterConfig()
// 	if err != nil {
// 		return nil, fmt.Errorf("error creating in-cluster config: %v", err)
// 	}

// 	clientset, err := kubernetes.NewForConfig(config)
// 	if err != nil {
// 		return nil, fmt.Errorf("error creating Kubernetes client: %v", err)
// 	}

// 	// List all pods in the cluster
// 	fmt.Println("Listing all pods in the cluster...")
// 	pods, err := clientset.CoreV1().Pods("").List(context.TODO(), metav1.ListOptions{})
// 	if err != nil {
// 		return nil, fmt.Errorf("error listing pods: %v", err)
// 	}
// 	fmt.Printf("Found %d pods in the cluster\n", len(pods.Items))

// 	// Iterate through all pods to find one with matching GPU requests
// 	for _, pod := range pods.Items {
// 		fmt.Printf("Processing pod: %s/%s, NodeName: %s, Phase: %s\n",
// 			pod.Namespace, pod.Name, pod.Spec.NodeName, pod.Status.Phase)

// 		// Check if the pod is in the Pending phase or already assigned to the node
// 		if pod.Status.Phase != v1.PodPending && pod.Spec.NodeName == "" {
// 			continue
// 		}

// 		// Check each container in the pod for GPU requests
// 		for _, container := range pod.Spec.Containers {
// 			if gpuRequest, ok := container.Resources.Requests["nvidia.com/gpu"]; ok && gpuRequest.Value() > 0 {
// 				fmt.Printf("  Pod %s/%s is requesting GPUs: %d\n", pod.Namespace, pod.Name, gpuRequest.Value())

// 				// Match the requested device IDs
// 				if matchDeviceIDs(deviceIDs, pod.Annotations["preferred-device-ids"]) {
// 					fmt.Printf("  Matching pod found: %s/%s\n", pod.Namespace, pod.Name)
// 					return &pod, nil
// 				} else {
// 					fmt.Printf("  Device IDs do not match for pod %s/%s\n", pod.Namespace, pod.Name)
// 				}
// 			} else {
// 				fmt.Printf("  Container %s does not request GPUs\n", container.Name)
// 			}
// 		}
// 	}

// 	return nil, fmt.Errorf("no matching pod found for device IDs: %v", deviceIDs)
// }

// GetPreferredAllocation returns the preferred allocation from the set of devices specified in the request
func (plugin *NvidiaDevicePlugin) GetPreferredAllocation(ctx context.Context, r *pluginapi.PreferredAllocationRequest) (*pluginapi.PreferredAllocationResponse, error) {

	// fmt.Printf("\n\n*****\ndude\n")
	// nodeName, err := getNodeNameFromAPI()
	// if err != nil {
	// 	return nil, fmt.Errorf("failed to determine node name: %v", err)
	// }
	// fmt.Printf("\n\n*****\nDevice plugin running on node: %s\n", nodeName)

	// // Log pods and their annotations
	// fmt.Println("Fetching pods and their annotations...")
	// pod, err := findPodForDeviceIDs(r.ContainerRequests[0].MustIncludeDeviceIDs)
	// if err != nil {
	// 	fmt.Printf("Error: %v\n", err)
	// } else {
	// 	fmt.Printf("Found pod: %s/%s\n", pod.Namespace, pod.Name)
	// }
	// fmt.Println("Continuing")

	// testAPI()

	/*

		1-40  one GPU

		60 one GPU
		61 two GPUs, 30 each
		62 three,
		63 four

		64 one

	*/

	response := &pluginapi.PreferredAllocationResponse{}
	for _, req := range r.ContainerRequests {
		devices, err := plugin.rm.GetPreferredAllocation(req.AvailableDeviceIDs, req.MustIncludeDeviceIDs, int(req.AllocationSize))
		if err != nil {
			return nil, fmt.Errorf("error getting list of preferred allocation devices: %v", err)
		}

		resp := &pluginapi.ContainerPreferredAllocationResponse{
			DeviceIDs: devices,
		}

		response.ContainerResponses = append(response.ContainerResponses, resp)
	}
	return response, nil
}

// Allocate which return list of devices.
func (plugin *NvidiaDevicePlugin) Allocate(ctx context.Context, reqs *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	responses := pluginapi.AllocateResponse{}
	for _, req := range reqs.ContainerRequests {
		if err := plugin.rm.ValidateRequest(req.DevicesIDs); err != nil {
			return nil, fmt.Errorf("invalid allocation request for %q: %w", plugin.rm.Resource(), err)
		}
		response, err := plugin.getAllocateResponse(req.DevicesIDs)
		if err != nil {
			return nil, fmt.Errorf("failed to get allocate response: %v", err)
		}

		klog.Infof("~ getAllocateResponse: %v", response)
		responses.ContainerResponses = append(responses.ContainerResponses, response)
	}

	return &responses, nil
}

func (plugin *NvidiaDevicePlugin) getAllocateResponse(requestIds []string) (*pluginapi.ContainerAllocateResponse, error) {
	deviceIDs := plugin.deviceIDsFromAnnotatedDeviceIDs(requestIds)

	// Create an empty response that will be updated as required below.
	response := &pluginapi.ContainerAllocateResponse{
		Envs: make(map[string]string),
	}
	klog.Infof("~ AnyCDIEnabled: %v", plugin.deviceListStrategies.AnyCDIEnabled())
	if plugin.deviceListStrategies.AnyCDIEnabled() {
		responseID := uuid.New().String()
		if err := plugin.updateResponseForCDI(response, responseID, deviceIDs...); err != nil {
			return nil, fmt.Errorf("failed to get allocate response for CDI: %v", err)
		}
	}
	if plugin.config.Sharing.SharingStrategy() == spec.SharingStrategyMPS {
		klog.Infof("~ Using MPS")
		plugin.updateResponseForMPS(response)
	}

	// The following modifications are only made if at least one non-CDI device
	// list strategy is selected.
	if plugin.deviceListStrategies.AllCDIEnabled() {
		return response, nil
	}

	if plugin.deviceListStrategies.Includes(spec.DeviceListStrategyEnvvar) {
		klog.Infof("~ plugin.deviceListStrategies.Includes(spec.DeviceListStrategyEnvvar)")
		plugin.updateResponseForDeviceListEnvvar(response, deviceIDs...)
	}
	if plugin.deviceListStrategies.Includes(spec.DeviceListStrategyVolumeMounts) {
		plugin.updateResponseForDeviceMounts(response, deviceIDs...)
	}
	if *plugin.config.Flags.Plugin.PassDeviceSpecs {
		response.Devices = append(response.Devices, plugin.apiDeviceSpecs(*plugin.config.Flags.NvidiaDevRoot, requestIds)...)
	}
	if *plugin.config.Flags.GDSEnabled {
		response.Envs["NVIDIA_GDS"] = "enabled"
	}
	if *plugin.config.Flags.MOFEDEnabled {
		response.Envs["NVIDIA_MOFED"] = "enabled"
	}
	return response, nil
}

// updateResponseForMPS ensures that the ContainerAllocate response contains the information required to use MPS.
// This includes per-resource pipe and log directories as well as a global daemon-specific shm
// and assumes that an MPS control daemon has already been started.
func (plugin NvidiaDevicePlugin) updateResponseForMPS(response *pluginapi.ContainerAllocateResponse) {
	// TODO: We should check that the deviceIDs are shared using MPS.
	response.Envs["CUDA_MPS_PIPE_DIRECTORY"] = plugin.mpsDaemon.PipeDir()

	resourceName := plugin.rm.Resource()
	response.Mounts = append(response.Mounts,
		&pluginapi.Mount{
			ContainerPath: plugin.mpsDaemon.PipeDir(),
			HostPath:      plugin.mpsHostRoot.PipeDir(resourceName),
		},
		&pluginapi.Mount{
			ContainerPath: plugin.mpsDaemon.ShmDir(),
			HostPath:      plugin.mpsHostRoot.ShmDir(resourceName),
		},
	)
}

// updateResponseForCDI updates the specified response for the given device IDs.
// This response contains the annotations required to trigger CDI injection in the container engine or nvidia-container-runtime.
func (plugin *NvidiaDevicePlugin) updateResponseForCDI(response *pluginapi.ContainerAllocateResponse, responseID string, deviceIDs ...string) error {
	var devices []string
	for _, id := range deviceIDs {
		devices = append(devices, plugin.cdiHandler.QualifiedName("gpu", id))
	}
	if *plugin.config.Flags.GDSEnabled {
		devices = append(devices, plugin.cdiHandler.QualifiedName("gds", "all"))
	}
	if *plugin.config.Flags.MOFEDEnabled {
		devices = append(devices, plugin.cdiHandler.QualifiedName("mofed", "all"))
	}

	if len(devices) == 0 {
		return nil
	}

	if plugin.deviceListStrategies.Includes(spec.DeviceListStrategyCDIAnnotations) {
		annotations, err := plugin.getCDIDeviceAnnotations(responseID, devices...)
		if err != nil {
			return err
		}
		response.Annotations = annotations
	}
	if plugin.deviceListStrategies.Includes(spec.DeviceListStrategyCDICRI) {
		for _, device := range devices {
			cdiDevice := pluginapi.CDIDevice{
				Name: device,
			}
			response.CDIDevices = append(response.CDIDevices, &cdiDevice)
		}
	}

	return nil
}

func (plugin *NvidiaDevicePlugin) getCDIDeviceAnnotations(id string, devices ...string) (map[string]string, error) {
	annotations, err := cdiapi.UpdateAnnotations(map[string]string{}, "nvidia-device-plugin", id, devices)
	if err != nil {
		return nil, fmt.Errorf("failed to add CDI annotations: %v", err)
	}

	if plugin.cdiAnnotationPrefix == spec.DefaultCDIAnnotationPrefix {
		return annotations, nil
	}

	// update annotations if a custom CDI prefix is configured
	updatedAnnotations := make(map[string]string)
	for k, v := range annotations {
		newKey := plugin.cdiAnnotationPrefix + strings.TrimPrefix(k, spec.DefaultCDIAnnotationPrefix)
		updatedAnnotations[newKey] = v
	}

	return updatedAnnotations, nil
}

// PreStartContainer is unimplemented for this plugin
func (plugin *NvidiaDevicePlugin) PreStartContainer(context.Context, *pluginapi.PreStartContainerRequest) (*pluginapi.PreStartContainerResponse, error) {
	return &pluginapi.PreStartContainerResponse{}, nil
}

// dial establishes the gRPC communication with the registered device plugin.
func (plugin *NvidiaDevicePlugin) dial(unixSocketPath string, timeout time.Duration) (*grpc.ClientConn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c, err := grpc.DialContext(ctx, unixSocketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", addr)
		}),
	)
	if err != nil {
		return nil, err
	}

	return c, nil
}

func (plugin *NvidiaDevicePlugin) deviceIDsFromAnnotatedDeviceIDs(ids []string) []string {
	var deviceIDs []string
	if *plugin.config.Flags.Plugin.DeviceIDStrategy == spec.DeviceIDStrategyUUID {
		deviceIDs = rm.AnnotatedIDs(ids).GetIDs()
	}
	if *plugin.config.Flags.Plugin.DeviceIDStrategy == spec.DeviceIDStrategyIndex {
		deviceIDs = plugin.rm.Devices().Subset(ids).GetIndices()
	}
	return deviceIDs
}

func (plugin *NvidiaDevicePlugin) apiDevices() []*pluginapi.Device {
	return plugin.rm.Devices().GetPluginDevices()
}

// updateResponseForDeviceListEnvvar sets the environment variable for the requested devices.
func (plugin *NvidiaDevicePlugin) updateResponseForDeviceListEnvvar(response *pluginapi.ContainerAllocateResponse, deviceIDs ...string) {
	response.Envs[plugin.deviceListEnvvar] = strings.Join(deviceIDs, ",")
}

// updateResponseForDeviceMounts sets the mounts required to request devices if volume mounts are used.
func (plugin *NvidiaDevicePlugin) updateResponseForDeviceMounts(response *pluginapi.ContainerAllocateResponse, deviceIDs ...string) {
	plugin.updateResponseForDeviceListEnvvar(response, deviceListAsVolumeMountsContainerPathRoot)
	for _, id := range deviceIDs {
		mount := &pluginapi.Mount{
			HostPath:      deviceListAsVolumeMountsHostPath,
			ContainerPath: filepath.Join(deviceListAsVolumeMountsContainerPathRoot, id),
		}
		response.Mounts = append(response.Mounts, mount)
	}
}

func (plugin *NvidiaDevicePlugin) apiDeviceSpecs(devRoot string, ids []string) []*pluginapi.DeviceSpec {
	optional := map[string]bool{
		"/dev/nvidiactl":        true,
		"/dev/nvidia-uvm":       true,
		"/dev/nvidia-uvm-tools": true,
		"/dev/nvidia-modeset":   true,
	}

	paths := plugin.rm.GetDevicePaths(ids)

	var specs []*pluginapi.DeviceSpec
	for _, p := range paths {
		if optional[p] {
			if _, err := os.Stat(p); err != nil {
				continue
			}
		}
		spec := &pluginapi.DeviceSpec{
			ContainerPath: p,
			HostPath:      filepath.Join(devRoot, p),
			Permissions:   "rw",
		}
		specs = append(specs, spec)
	}

	return specs
}
