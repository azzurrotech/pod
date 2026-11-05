#!/bin/bash
# build-pod.sh - Build and run the POD (Podcast/OSINT record store) service
#
# Usage:
#   ./build-pod.sh           # build and start on default port
#   ./build-pod.sh 8081    # start on port 8081
#   ./build-pod.sh http://localhost:8081  # use existing running service
#
# Automatically detects an open local port if none specified,
# otherwise accepts a URL as argument.

set -e

# Defaults
PORT=""
DB_DIR="./data"

# Parse arguments: if a port number or URL is given, use it
if [ $# -ge 1 ]; then
  FIRST="$1"
  # Check if it looks like a URL
  if echo "$FIRST" | grep -q '^http://\|^https://'; then
    PORT="$FIRST"
  else
    # Assume it's a port number
    if ! echo "$FIRST" | grep -qE '^[0-9]+$'; then
      echo "ERROR: Expected a port number or URL, got: $FIRST"
      exit 1
    fi
    PORT=":$FIRST"
  fi
fi

# Find an open port if not provided
if [ -z "$PORT" ]; then
  for trial_port in 8081 8082 8083 8084 8085 9090 3000; do
    if ! curl -s "http://localhost${trial_port}" > /dev/null 2>&1; then
      PORT=":$trial_port"
      echo ">>> Using available port $trial_port"
      break
    fi
  done
  if [ -z "$PORT" ]; then
    echo ">>> No default port available, relying on Go defaults/env"
    PORT=""
  fi
fi

# Build the Go binary
echo ">>> Building POD binary..."
go build -o pod ./cmd/pod/

echo ">>> Starting POD..."
if [ -n "$PORT" ]; then
  echo ">>> Using port $PORT"
  export POD_PORT="${PORT#:}"
  ./pod -db "$DB_DIR" -port "${PORT#:}"
else
  ./pod -db "$DB_DIR" -port ":8080"
fi