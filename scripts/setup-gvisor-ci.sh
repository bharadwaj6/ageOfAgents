#!/bin/bash
set -eo pipefail

# This script sets up gVisor (runsc) on Ubuntu environments, particularly for GitHub Actions.
# It requires sudo privileges.

echo "Installing dependencies..."
sudo apt-get update
sudo apt-get install -y apt-transport-https ca-certificates curl gnupg

echo "Adding gVisor repository..."
curl -fsSL https://gvisor.dev/archive.key | sudo gpg --dearmor -o /usr/share/keyrings/gvisor-archive-keyring.gpg
echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/gvisor-archive-keyring.gpg] https://storage.googleapis.com/gvisor/releases release main" | sudo tee /etc/apt/sources.list.d/gvisor.list > /dev/null

echo "Installing runsc..."
sudo apt-get update && sudo apt-get install -y runsc

echo "Configuring Docker to use runsc..."
# Configure Docker daemon
sudo mkdir -p /etc/docker
cat <<EOF | sudo tee /etc/docker/daemon.json
{
    "runtimes": {
        "runsc": {
            "path": "/usr/bin/runsc"
        }
    }
}
EOF

echo "Restarting Docker..."
if command -v systemctl >/dev/null 2>&1; then
    sudo systemctl restart docker
else
    echo "systemctl not found; skipping docker daemon restart. Please restart the docker daemon manually or via your init system."
fi

echo "gVisor setup complete! You can now use '--runtime=runsc' with docker run."
