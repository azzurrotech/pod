#!/bin/bash
# run.sh for pod - Standalone pod record store
#
# Detects if pod is already running locally and prompts:
#   use local / start new local / use remote
# Default: use any running local instance

set -e

POD_PORT="${POD_PORT:-8081}"

# Check if pod is already running locally
is_local_running() {
    curl -s "http://127.0.0.1:$POD_PORT/api/health" > /dev/null 2>&1
    return $?
}

# Check if pod remote is reachable
is_remote_running() {
    curl -s "http://${POD_REMOTE:-127.0.0.1:$POD_PORT}/api/health" > /dev/null 2>&1
    return $?
}

echo "=== pod Service ==="
echo "Configuration:"
echo "  Local address: 127.0.0.1:$POD_PORT"

SELECTED=""

if is_local_running; then
    echo "✓ Local pod is already running on 127.0.0.1:$POD_PORT"
    read -p "Use local instance? [Y/n/remote]: " choice
    case "$choice" in
        n|N)
            SELECTED="remote"
            ;;
        *)
            SELECTED="local"
            ;;
    esac
fi

if [ "$SELECTED" = "" ]; then
    if is_remote_running; then
        echo "✓ Remote pod is reachable at ${POD_REMOTE:-127.0.0.1:$POD_PORT}"
        read -p "Use remote instance? [y/N/local]: " choice
        case "$choice" in
            y|Y)
                SELECTED="remote"
                ;;
            l|L)
                SELECTED="local"
                ;;
            *)
                SELECTED="local"
                ;;
        esac
    else
        echo "✗ No local or remote pod running. Starting new local instance..."
        SELECTED="local"
    fi
fi

case "$SELECTED" in
    local)
        echo ">>> Starting new local pod instance..."
        if [ -x "$(command -v pod)" ]; then
            pod -port "$POD_PORT" -db "./data/pod" &
            echo "    PID: $!"
            echo "✓ pod started locally on 127.0.0.1:$POD_PORT"
        else
            echo "✗ pod binary not found. Please build first: go build ./..."
        fi
        ;;
    remote)
        echo ">>> Using remote pod at ${POD_REMOTE:-127.0.0.1:$POD_PORT}"
        if [ -z "$POD_REMOTE" ]; then
            export POD_REMOTE="${POD_REMOTE:-127.0.0.1:$POD_PORT}"
            echo "✓ Remote set: $POD_REMOTE"
        fi
        ;;
    *)
        echo ">>> Starting new local pod instance..."
        if [ -x "$(command -v pod)" ]; then
            pod -port "$POD_PORT" -db "./data/pod" &
            echo "    PID: $!"
            echo "✓ pod started locally on 127.0.0.1:$POD_PORT"
        else
            echo "✗ pod binary not found. Please build first: go build ./..."
        fi
        ;;
esac

echo ""
echo "pod service ready on port $POD_PORT"
echo "API endpoints:"
echo "  Health:     http://127.0.0.1:$POD_PORT/api/health"
echo "  Tables:     http://127.0.0.1:$POD_PORT/api/tables"
echo "  Query:      http://127.0.0.1:$POD_PORT/api/query"
