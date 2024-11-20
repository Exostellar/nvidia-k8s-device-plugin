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
	// how many GPUs (len of array) and how many GB we need from each?
	if N <= 40 { //40 is the size of an A100 (40GB)
		return []int{N}
	}
	switch N % 10 {
	case 1:
		return []int{N / 2, N/2 + 1}
	case 2:
		return []int{N / 3, N/3 + 1, N/3 + 1}
	case 3:
		return []int{N / 4, N/4 + 1, N/4 + 1, N/4 + 1}
	case 4:
		return []int{N / 8, N / 8, N / 8, N / 8, N / 8, N / 8, N / 8, N / 8}
	default:
		return []int{N}
	}
}

// distributedAlloc returns a list of devices such that any replicated
// devices are distributed across all replicated GPUs equally. It takes into
// account already allocated replicas to ensure a proper balance across them.
func (r *resourceManager) distributedAlloc(available, required []string, size int) ([]string, error) {
	klog.Info("~ distributedAlloc @ nvml_manager.go")
	fmt.Print("~ distributedAlloc @ nvml_manager.go")

	// Get the set of candidate devices as the difference between available and required.
	candidates := r.devices.Subset(available).Difference(r.devices.Subset(required)).GetIDs()
	needed := size - len(required)

	fmt.Printf(" available: %v", available)
	fmt.Printf(" required: %v", required)
	fmt.Printf(" size: %v", size)

	if len(candidates) < needed {
		return nil, fmt.Errorf("not enough available devices to satisfy allocation")
	}

	type Replica struct {
		id         string
		total      int
		available  int
		candidates []string
	}
	var replicas []Replica

	// Process candidates
	for _, c := range candidates {
		id := AnnotatedID(c).GetID()

		// Find the replica in the list
		var replica *Replica
		for i := range replicas {
			if replicas[i].id == id {
				replica = &replicas[i]
				break
			}
		}

		// If not found, create a new replica entry
		if replica == nil {
			replicas = append(replicas, Replica{
				id: id,
			})
			replica = &replicas[len(replicas)-1]
		}

		// Update replica fields
		replica.available++
		replica.total++
		replica.candidates = append(replica.candidates, c)
	}

	// Sort replicas by less used first (total - available)
	sort.Slice(replicas, func(i, j int) bool {
		iUsed := replicas[i].total - replicas[i].available
		jUsed := replicas[j].total - replicas[j].available
		return iUsed < jUsed
	})

	// Debug print to verify sorted output
	klog.Infof("~ sorted [%d] candidates:", len(replicas))
	for _, r := range replicas {
		fmt.Printf("ID: %s, Total: %d, Available: %d, Candidates: %v\n",
			r.id, r.total, r.available, r.candidates)
	}

	var devices []string
	allocs := calculateGPUAllocations(needed) // TODO: needed or size?
	klog.Infof(" needed: %v", needed)
	klog.Infof(" size: %v", size)
	fmt.Printf("Looking for these allocations: %v\n", allocs)

	// var allocations []Replica

	for i, alloc := range allocs {
		if i >= len(candidates) {
			// If we run out of candidates, stop allocating
			fmt.Print("if i >= len(candidates)\n")
			break
		}

		// TODO: do we need all this?
		// Check if the candidate has enough resources available
		if replicas[i].available >= alloc {
			// allocationMap[i] = alloc
			replicas[i].available -= alloc // Deduct the allocated resources
		} else {
			fmt.Print("Not enough resources!!!\n")
		}

		//devices = append(devices, candidates[i])
		devices = append(devices, replicas[i].candidates[:alloc]...)
	}

	// Print or use the allocation results
	// for candidate, allocated := range allocationMap {
	// 	fmt.Printf("Candidate %s allocated %d resources\n", candidate, allocated)
	// }

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
	klog.Infof("Final devices: %v", devices)

	return devices, nil
}

// func (r *resourceManager) distributedAlloc_v2(available, required []string, size int) ([]string, error) {
// 	klog.Info("~ distributedAlloc @ nvml_manager.go")

// 	// Get the set of candidate devices as the difference between available and required.
// 	candidates := r.devices.Subset(available).Difference(r.devices.Subset(required)).GetIDs()
// 	needed := size - len(required)

// 	if len(candidates) < needed {
// 		return nil, fmt.Errorf("not enough available devices to satisfy allocation")
// 	}

// 	// For each candidate device, build a mapping of (stripped) device ID to
// 	// total / available replicas for that device.
// 	replicas := make(map[string]*struct{ total, available int })
// 	for _, c := range candidates {
// 		id := AnnotatedID(c).GetID()
// 		if _, exists := replicas[id]; !exists {
// 			replicas[id] = &struct{ total, available int }{}
// 		}
// 		replicas[id].available++
// 	}
// 	klog.Infof("~ replicas: %v", replicas)

// 	for d := range r.devices {
// 		id := AnnotatedID(d).GetID()
// 		if _, exists := replicas[id]; !exists {
// 			continue
// 		}
// 		replicas[id].total++
// 	}

// 	// Grab the set of 'needed' devices one-by-one from the candidates list.
// 	// Before selecting each candidate, first sort the candidate list using the
// 	// replicas map above. After sorting, the first element in the list will
// 	// contain the device with the least difference between total and available
// 	// replications (based on what's already been allocated). Add this device
// 	// to the list of devices to allocate, remove it from the candidate list,
// 	// down its available count in the replicas map, and repeat.
// 	var devices []string
// 	for i := 0; i < needed; i++ {
// 		sort.Slice(candidates, func(i, j int) bool {
// 			iid := AnnotatedID(candidates[i]).GetID()
// 			jid := AnnotatedID(candidates[j]).GetID()
// 			idiff := replicas[iid].total - replicas[iid].available
// 			jdiff := replicas[jid].total - replicas[jid].available
// 			return idiff < jdiff
// 		})

// 		klog.Info("~ Sorted devices: ", candidates)
// 		klog.Info("~ Choosing: ", candidates[0])
// 		// klog.Infof("  with: %s free memory", candidates[0])

// 		id := AnnotatedID(candidates[0]).GetID()
// 		replicas[id].available--
// 		devices = append(devices, candidates[0])
// 		candidates = candidates[1:]
// 	}

// 	// Add the set of required devices to this list and return it.
// 	devices = append(required, devices...)

// 	return devices, nil
// }
