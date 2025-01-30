#!/bin/bash
#nvidia-device-plugin

# gcloud auth configure-docker us-central1-docker.pkg.dev

#helm upgrade --install nvdp ./deployments/helm/nvidia-device-plugin \
#     --namespace kube-system \
#  -f deployments/helm/nvidia-device-plugin/values_gcp.yaml

kubectl rollout restart daemonset nvdp-nvidia-device-plugin -n kube-system

