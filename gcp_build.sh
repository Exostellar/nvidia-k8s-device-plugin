#!/bin/bash

go mod tidy
go mod vendor

docker build \
    -t nvcr.io/nvidia/k8s-device-plugin:devel \
    -f deployments/container/Dockerfile.ubuntu \
    .

# gcloud auth configure-docker us-central1-docker.pkg.dev

docker tag nvcr.io/nvidia/k8s-device-plugin:devel us-central1-docker.pkg.dev/poc-omniops-gpu/sdg/nvidia-dp:latest
docker push us-central1-docker.pkg.dev/poc-omniops-gpu/sdg/nvidia-dp:latest
