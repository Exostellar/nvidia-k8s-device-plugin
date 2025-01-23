#!/bin/bash

go mod tidy
go mod vendor

docker build \
    -t nvcr.io/nvidia/k8s-device-plugin:devel \
    -f deployments/container/Dockerfile.ubuntu \
    .

# edit these to your ECR repo
aws ecr get-login-password --region us-east-2 | docker login --username AWS --password-stdin 557690625180.dkr.ecr.us-east-2.amazonaws.com
docker tag nvcr.io/nvidia/k8s-device-plugin:devel 557690625180.dkr.ecr.us-east-2.amazonaws.com/sdg/libnvidia:latest
docker push 557690625180.dkr.ecr.us-east-2.amazonaws.com/sdg/libnvidia:latest
