#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
CONFIG="$SCRIPT_DIR/config.json"
LOG_DIR="$SCRIPT_DIR/logs"
PID_FILE="$SCRIPT_DIR/server.pid"
PORT=9000

mkdir -p "$LOG_DIR"

stop() {
    if [ -f "$PID_FILE" ]; then
        local pid
        pid=$(cat "$PID_FILE")
        if kill -0 "$pid" 2>/dev/null; then
            echo "Stopping local server (PID $pid)..."
            kill "$pid"
            wait "$pid" 2>/dev/null || true
        fi
        rm -f "$PID_FILE"
    fi
}

start() {
    if [ -f "$PID_FILE" ] && kill -0 "$(cat "$PID_FILE")" 2>/dev/null; then
        echo "Server is already running (PID $(cat "$PID_FILE"), port $PORT)"
        return 0
    fi

    echo "Starting local server on port $PORT..."
    cd "$PROJECT_ROOT"
    go run ./cmd/server/ --config "$CONFIG" > "$LOG_DIR/server.log" 2>&1 &
    local pid=$!
    echo "$pid" > "$PID_FILE"

    # 等待启动
    for i in $(seq 1 10); do
        if curl -s "http://localhost:$PORT/_metrics" > /dev/null 2>&1; then
            echo "Server started (PID $pid, port $PORT)"
            echo "  Endpoint: http://localhost:$PORT"
            echo "  Web UI:   http://localhost:$PORT/_ui/"
            echo "  Log:      $LOG_DIR/server.log"
            return 0
        fi
        sleep 0.5
    done

    echo "ERROR: Server failed to start. Check $LOG_DIR/server.log"
    rm -f "$PID_FILE"
    return 1
}

case "${1:-start}" in
    start)  start ;;
    stop)   stop ;;
    restart) stop; sleep 1; start ;;
    status)
        if [ -f "$PID_FILE" ] && kill -0 "$(cat "$PID_FILE")" 2>/dev/null; then
            echo "Running (PID $(cat "$PID_FILE"), port $PORT)"
        else
            echo "Stopped"
        fi
        ;;
    log)   tail -f "$LOG_DIR/server.log" ;;
    *)     echo "Usage: $0 {start|stop|restart|status|log}"; exit 1 ;;
esac
