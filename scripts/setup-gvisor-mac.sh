#!/bin/bash
set -eo pipefail

# This script provides instructions and a docker-compose snippet to install
# gVisor (runsc) into the Docker Desktop for Mac VM, bypassing its read-only filesystem constraint.

echo "=========================================================================="
echo " Setting up gVisor (runsc) on Docker Desktop for Mac"
echo "=========================================================================="
echo ""
echo "Docker Desktop for Mac runs a Linux VM with a read-only filesystem."
echo "To install gVisor, we must use a privileged container to inject the runsc"
echo "binaries into a writable volume mount and update daemon.json."
echo ""
echo "To install gVisor, create a docker-compose.yml file with the following contents:"
echo ""

cat << 'EOF'
version: '3.8'
services:
  gvisor-installer:
    image: alpine:latest
    privileged: true
    pid: host
    volumes:
      # Mount the Docker daemon configuration path
      - /var/run/docker.sock:/var/run/docker.sock
      # Mount a writable path inside the VM to store the binary
      - /var/lib/docker/volumes:/var/lib/docker/volumes
      # Mount root to edit daemon.json
      - /:/host
    command: >
      sh -c "
        apk add --no-cache curl tar &&
        mkdir -p /var/lib/docker/volumes/gvisor/bin &&
        cd /var/lib/docker/volumes/gvisor/bin &&
        echo 'Downloading gVisor binaries...' &&
        curl -sSL -O https://storage.googleapis.com/gvisor/releases/release/latest/x86_64/runsc &&
        curl -sSL -O https://storage.googleapis.com/gvisor/releases/release/latest/x86_64/runsc.sha512 &&
        curl -sSL -O https://storage.googleapis.com/gvisor/releases/release/latest/x86_64/containerd-shim-runsc-v1 &&
        curl -sSL -O https://storage.googleapis.com/gvisor/releases/release/latest/x86_64/containerd-shim-runsc-v1.sha512 &&
        chmod a+rx runsc containerd-shim-runsc-v1 &&
        echo 'Binaries installed to /var/lib/docker/volumes/gvisor/bin' &&
        
        echo 'Updating Docker daemon.json...' &&
        mkdir -p /host/etc/docker &&
        if [ ! -f /host/etc/docker/daemon.json ]; then
          echo '{}' > /host/etc/docker/daemon.json
        fi &&
        # Use a simple sed to inject or manually ensure it's added.
        # Alternatively, use jq if installed.
        echo 'Please manually ensure /etc/docker/daemon.json has:' &&
        echo '{ \"runtimes\": { \"runsc\": { \"path\": \"/var/lib/docker/volumes/gvisor/bin/runsc\" } } }'
      "
EOF

echo ""
echo "1. Save the above to docker-compose.yml"
echo "2. Run: docker-compose up"
echo "3. Restart Docker Desktop completely (Quit and open again)"
echo "4. Test: docker run --rm --runtime=runsc alpine uname -a"
echo "=========================================================================="
