#!/bin/bash
#nvidia-device-plugin

helm upgrade --install --wait \
    -n kube-system \
    nvdp \
    ./deployments/helm/nvidia-device-plugin \
    --set hostPaths.driverInstallDir=/home/kubernetes/bin/nvidia \
    --set toolkit.installDir=/home/kubernetes/bin/nvidia \
    --set cdi.enabled=true \
    --set cdi.default=true \
    --set driver.enabled=false

#helm upgrade --install nvdp ./deployments/helm/nvidia-device-plugin \
#     --namespace kube-system \
#  -f deployments/helm/nvidia-device-plugin/values_gcp.yaml

#kubectl rollout restart daemonset nvdp-nvidia-device-plugin -n kube-system

