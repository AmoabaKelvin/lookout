#!/usr/bin/env sh
set -eu

# Runs the agent inside a Linux Go container so /proc/meminfo is available.
# Usage:
#   sh run-dev.sh
#
# Optional: in another terminal, run:
#   sh run-dev.sh stress

case "${1:-agent}" in
  agent)
    docker run -d --name lookout-app nginx 2>/dev/null || true
    docker run -it --rm \
      -e MEMINFO_PATH=/proc/meminfo \
      -e DISKINFO_PATH=/proc/mounts \
      -v "$(pwd)":/app \
      -v /var/run/docker.sock:/var/run/docker.sock \
      -w /app \
      golang:1.25-bookworm \
      sh -c 'go run main.go'
    ;;

  shell)
    docker run -it --rm \
      -v "$(pwd)":/app \
      -w /app \
      golang:1.24-bookworm \
      bash
    ;;

  stress)
    docker run --rm -it \
      -m 512m \
      debian:stable-slim \
      sh -c 'apt-get update >/dev/null && apt-get install -y stress-ng >/dev/null && stress-ng --vm 1 --vm-bytes 256M --timeout 60s'
    ;;

  *)
    echo "Unknown command: $1"
    echo "Usage: sh run-dev.sh [agent|shell|stress]"
    exit 1
    ;;
esac
