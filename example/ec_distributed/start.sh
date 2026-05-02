#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
LOG_DIR="$SCRIPT_DIR/logs"
PID_DIR="$SCRIPT_DIR/pids"
PORTS=(9101 9102 9103 9104 9105 9106)
CONFIGS=(node1-config.json node2-config.json node3-config.json node4-config.json node5-config.json node6-config.json)
NODE_COUNT=${#PORTS[@]}

mkdir -p "$LOG_DIR" "$PID_DIR"

stop() {
    echo "Stopping EC distributed cluster..."
    local stopped=0
    for i in $(seq 0 $((NODE_COUNT - 1))); do
        local pid_file="$PID_DIR/node$((i + 1)).pid"
        if [ -f "$pid_file" ]; then
            local pid
            pid=$(cat "$pid_file")
            if kill -0 "$pid" 2>/dev/null; then
                kill "$pid"
                echo "  Node $((i + 1)) (port ${PORTS[$i]}, PID $pid) stopped"
                stopped=$((stopped + 1))
            fi
            rm -f "$pid_file"
        fi
    done
    sleep 1
    echo "Stopped $stopped node(s)"
}

wait_cluster() {
    local seed_port=${PORTS[0]}
    local timeout=${1:-20}
    local elapsed=0

    echo "Waiting for cluster to converge (timeout ${timeout}s)..."
    while [ $elapsed -lt $timeout ]; do
        local alive
        alive=$(curl -s "http://localhost:$seed_port/_cluster/members" 2>/dev/null \
            | python3 -c "import sys,json; print(len(json.load(sys.stdin)))" 2>/dev/null || echo 0)
        if [ "$alive" -eq "$NODE_COUNT" ]; then
            echo "Cluster ready: $NODE_COUNT nodes alive"
            return 0
        fi
        sleep 1
        elapsed=$((elapsed + 1))
        echo -ne "  $alive/$NODE_COUNT nodes alive...\r"
    done
    echo ""
    echo "WARNING: Only $alive/$NODE_COUNT nodes alive after ${timeout}s"
    return 1
}

start() {
    local running=0
    for i in $(seq 0 $((NODE_COUNT - 1))); do
        if [ -f "$PID_DIR/node$((i + 1)).pid" ] && kill -0 "$(cat "$PID_DIR/node$((i + 1)).pid")" 2>/dev/null; then
            running=$((running + 1))
        fi
    done
    if [ "$running" -eq "$NODE_COUNT" ]; then
        echo "Cluster is already running ($NODE_COUNT nodes)"
        return 0
    fi

    echo "Starting EC distributed cluster ($NODE_COUNT nodes, K=4, M=2)..."
    cd "$PROJECT_ROOT"

    for i in $(seq 0 $((NODE_COUNT - 1))); do
        local port=${PORTS[$i]}
        local config=${CONFIGS[$i]}
        local pid_file="$PID_DIR/node$((i + 1)).pid"

        go run ./cmd/server/ --config "$SCRIPT_DIR/$config" > "$LOG_DIR/node$((i + 1)).log" 2>&1 &
        local pid=$!
        echo "$pid" > "$pid_file"
        echo "  Node $((i + 1)) started (port $port, PID $pid)"
        sleep 0.5
    done

    wait_cluster 20

    echo ""
    echo "Cluster endpoints:"
    for i in $(seq 0 $((NODE_COUNT - 1))); do
        echo "  Node $((i + 1)): http://localhost:${PORTS[$i]} (log: $LOG_DIR/node$((i + 1)).log)"
    done
}

status() {
    local alive=0
    for i in $(seq 0 $((NODE_COUNT - 1))); do
        local pid_file="$PID_DIR/node$((i + 1)).pid"
        if [ -f "$pid_file" ] && kill -0 "$(cat "$pid_file")" 2>/dev/null; then
            echo "  Node $((i + 1)) (port ${PORTS[$i]}): Running (PID $(cat "$pid_file"))"
            alive=$((alive + 1))
        else
            echo "  Node $((i + 1)) (port ${PORTS[$i]}): Stopped"
        fi
    done
    echo "  Total: $alive/$NODE_COUNT"
}

log() {
    local node=${1:-1}
    local log_file="$LOG_DIR/node${node}.log"
    if [ -f "$log_file" ]; then
        tail -f "$log_file"
    else
        echo "Log file not found: $log_file"
    fi
}

case "${1:-start}" in
    start)   start ;;
    stop)    stop ;;
    restart) stop; sleep 1; start ;;
    status)  status ;;
    log)     log "${2:-1}" ;;
    *)       echo "Usage: $0 {start|stop|restart|status|log [node_number]}"; exit 1 ;;
esac
