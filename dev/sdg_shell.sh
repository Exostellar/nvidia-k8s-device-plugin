#!/bin/bash

# Path to the pod manifest template
POD_MANIFEST_TEMPLATE="base_pod.yaml"
# Generate a unique pod name using a timestamp
POD_NAME="sdgpu-$(date +%s)"
# Namespace
NAMESPACE="default"

# Check for the -g parameter
while getopts ":m:" opt; do
  case $opt in
    m)
      SDGPU_GBS="$OPTARG"
      ;;
    \?)
      echo "Invalid option: -$OPTARG" >&2
      exit 1
      ;;
    :)
      echo "Option -$OPTARG requires an argument." >&2
      exit 1
      ;;
  esac
done

# Ensure the -g parameter is provided
if [ -z "$SDGPU_GBS" ]; then
  echo "Usage: $0 -m <SDGPU_GBS>"
  exit 1
fi

# Convert SDGPU_GBS (in GB) to SDGPU_BYTES (in bytes)
SDGPU_BYTES=$((SDGPU_GBS * 1024 * 1024 * 1024))

# Generate the pod manifest from the template, replacing placeholders
POD_MANIFEST="pod.yaml"
sed -e "s/{{POD_NAME}}/$POD_NAME/" -e "s/{{SDGPU_GBS}}/$SDGPU_GBS/" -e "s/{{SDGPU_BYTES}}/$SDGPU_BYTES/" $POD_MANIFEST_TEMPLATE > $POD_MANIFEST

# Apply the manifest to create the pod
echo "Creating pod with name $POD_NAME, SDGPU_GBS=$SDGPU_GBS, and SDGPU_BYTES=$SDGPU_BYTES..."
kubectl apply -f $POD_MANIFEST

# Wait for the pod to be ready
echo "Waiting for the pod $POD_NAME to be ready..."
kubectl wait --for=condition=ready pod/$POD_NAME --namespace=$NAMESPACE --timeout=180s

# Open a shell in the pod
echo "Opening a shell in the pod $POD_NAME..."
kubectl exec -it $POD_NAME --namespace=$NAMESPACE -- /bin/bash
