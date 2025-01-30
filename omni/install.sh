#!/bin/bash

helm install gpu-operator nvidia/gpu-operator -n kube-system \
    -f gpu-operator-values.yaml \
    --set hostPaths.driverInstallDir=/home/kubernetes/bin/nvidia \
    --set toolkit.installDir=/home/kubernetes/bin/nvidia \
    --set cdi.enabled=true \
    --set cdi.default=true \
    --set driver.enabled=false
