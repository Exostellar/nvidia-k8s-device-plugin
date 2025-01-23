#!/bin/bash
#nvidia-device-plugin

helm upgrade --install nvdp ./deployments/helm/nvidia-device-plugin \
     --namespace kube-system \
     --create-namespace \
  -f deployments/helm/nvidia-device-plugin/values.yaml

#kubectl rollout restart daemonset nvdp-nvidia-device-plugin -n nvidia-device-plugin

