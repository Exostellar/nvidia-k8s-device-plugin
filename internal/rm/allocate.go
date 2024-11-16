/*
 * Copyright (c) 2022, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY Type, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package rm

import (
	"fmt"
	"sort"
	"strings"

	"k8s.io/klog/v2"
)

func countGPUOccurrences(gpuList []string) map[string]int {
	// Create a map to store the GPU counts
	counts := make(map[string]int)

	// Loop through each GPU entry in the list
	for _, gpu := range gpuList {
		// Split the string at "::" and take the first part (the GPU ID)
		gpuID := strings.Split(gpu, "::")[0]

		// Increment the count for the GPU ID in the map
		counts[gpuID]++
	}

	return counts
}

func calculateGPUAllocations(N int) []int {
	if N <= 40 { //40 is the size of an A100 (40GB)
		return []int{N}
	}
	switch N % 4 {
	case 1:
		return []int{N / 2, N/2 + 1}
	case 2:
		return []int{N / 3, N/3 + 1, N/3 + 1}
	case 3:
		return []int{N / 4, N/4 + 1, N/4 + 1, N/4 + 1}
	default: // 0, or ends in 4
		return []int{N / 2, N / 2}
	}
}

// distributedAlloc returns a list of devices such that any replicated
// devices are distributed across all replicated GPUs equally. It takes into
// account already allocated replicas to ensure a proper balance across them.
func (r *resourceManager) distributedAlloc(available, required []string, size int) ([]string, error) {
	klog.Info("~ distributedAlloc @ nvml_manager.go")

	// Get the set of candidate devices as the difference between available and required.
	candidates := r.devices.Subset(available).Difference(r.devices.Subset(required)).GetIDs()
	needed := size - len(required)

	klog.Infof(" available: %v", available)
	klog.Infof(" required: %v", required)
	klog.Infof(" size: %v", size)

	if len(candidates) < needed {
		return nil, fmt.Errorf("not enough available devices to satisfy allocation")
	}

	counts := countGPUOccurrences(candidates)
	klog.Infof("candidate counts: %v", candidates)
	// Print the counts
	for gpuID, count := range counts {
		klog.Infof("%s: %d\n", gpuID, count)
	}

	// var devices []string
	// devices = append(required, devices...)
	// return devices, nil

	// For each candidate device, build a mapping of (stripped) device ID to
	// total / available replicas for that device.
	replicas := make(map[string]*struct{ total, available int })
	for _, c := range candidates {
		id := AnnotatedID(c).GetID()
		if _, exists := replicas[id]; !exists {
			replicas[id] = &struct{ total, available int }{}
		}
		replicas[id].available++
	}
	klog.Infof("~ replicas: %v", replicas)

	for d := range r.devices {
		id := AnnotatedID(d).GetID()
		if _, exists := replicas[id]; !exists {
			continue
		}
		replicas[id].total++
	}

	sort.Slice(candidates, func(i, j int) bool {
		iid := AnnotatedID(candidates[i]).GetID()
		jid := AnnotatedID(candidates[j]).GetID()
		idiff := replicas[iid].total - replicas[iid].available
		jdiff := replicas[jid].total - replicas[jid].available
		return idiff < jdiff
	})

	var devices []string
	allocs := calculateGPUAllocations(needed) // size
	klog.Infof(" needed: %v", needed)
	klog.Infof(" size: %v", size)
	fmt.Printf("Looking for these allocations: %v\n", allocs)
	allocationMap := make(map[string]int) // To store the allocations
	for i, alloc := range allocs {
		if i >= len(candidates) {
			// If we run out of candidates, stop allocating
			fmt.Print("if i >= len(candidates)\n")
			break
		}

		candidateID := AnnotatedID(candidates[i]).GetID()

		// Check if the candidate has enough resources available
		if replicas[candidateID].available >= alloc {
			allocationMap[candidateID] = alloc
			replicas[candidateID].available -= alloc // Deduct the allocated resources
		} else {
			fmt.Print("Not enough resources!!!\n")
		}

		devices = append(devices, candidates[i])
	}

	// Print or use the allocation results
	for candidate, allocated := range allocationMap {
		fmt.Printf("Candidate %s allocated %d resources\n", candidate, allocated)
	}

	// Grab the set of 'needed' devices one-by-one from the candidates list.
	// Before selecting each candidate, first sort the candidate list using the
	// replicas map above. After sorting, the first element in the list will
	// contain the device with the least difference between total and available
	// replications (based on what's already been allocated). Add this device
	// to the list of devices to allocate, remove it from the candidate list,
	// down its available count in the replicas map, and repeat.
	// var devices []string
	// for i := 0; i < needed; i++ {

	// 	sort.Slice(candidates, func(i, j int) bool {
	// 		iid := AnnotatedID(candidates[i]).GetID()
	// 		jid := AnnotatedID(candidates[j]).GetID()
	// 		idiff := replicas[iid].total - replicas[iid].available
	// 		jdiff := replicas[jid].total - replicas[jid].available
	// 		return idiff < jdiff
	// 	})

	// 	klog.Info("~ Sorted devices: ", candidates)
	// 	klog.Info("~ Choosing: ", candidates[0])
	// 	// klog.Infof("  with: %s free memory", candidates[0])

	// 	id := AnnotatedID(candidates[0]).GetID()
	// 	replicas[id].available--
	// 	devices = append(devices, candidates[0])
	// 	candidates = candidates[1:]
	// }

	// Add the set of required devices to this list and return it.
	devices = append(required, devices...)
	fmt.Printf("Final devices: %v", devices)

	return devices, nil
}

func (r *resourceManager) distributedAlloc_v2(available, required []string, size int) ([]string, error) {
	klog.Info("~ distributedAlloc @ nvml_manager.go")

	// Get the set of candidate devices as the difference between available and required.
	candidates := r.devices.Subset(available).Difference(r.devices.Subset(required)).GetIDs()
	needed := size - len(required)

	if len(candidates) < needed {
		return nil, fmt.Errorf("not enough available devices to satisfy allocation")
	}

	// For each candidate device, build a mapping of (stripped) device ID to
	// total / available replicas for that device.
	replicas := make(map[string]*struct{ total, available int })
	for _, c := range candidates {
		id := AnnotatedID(c).GetID()
		if _, exists := replicas[id]; !exists {
			replicas[id] = &struct{ total, available int }{}
		}
		replicas[id].available++
	}
	klog.Infof("~ replicas: %v", replicas)

	for d := range r.devices {
		id := AnnotatedID(d).GetID()
		if _, exists := replicas[id]; !exists {
			continue
		}
		replicas[id].total++
	}

	// Grab the set of 'needed' devices one-by-one from the candidates list.
	// Before selecting each candidate, first sort the candidate list using the
	// replicas map above. After sorting, the first element in the list will
	// contain the device with the least difference between total and available
	// replications (based on what's already been allocated). Add this device
	// to the list of devices to allocate, remove it from the candidate list,
	// down its available count in the replicas map, and repeat.
	var devices []string
	for i := 0; i < needed; i++ {
		sort.Slice(candidates, func(i, j int) bool {
			iid := AnnotatedID(candidates[i]).GetID()
			jid := AnnotatedID(candidates[j]).GetID()
			idiff := replicas[iid].total - replicas[iid].available
			jdiff := replicas[jid].total - replicas[jid].available
			return idiff < jdiff
		})

		klog.Info("~ Sorted devices: ", candidates)
		klog.Info("~ Choosing: ", candidates[0])
		// klog.Infof("  with: %s free memory", candidates[0])

		id := AnnotatedID(candidates[0]).GetID()
		replicas[id].available--
		devices = append(devices, candidates[0])
		candidates = candidates[1:]
	}

	// Add the set of required devices to this list and return it.
	devices = append(required, devices...)

	return devices, nil
}
