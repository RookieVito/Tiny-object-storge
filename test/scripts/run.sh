#!/usr/bin/env bash
# 全量集成测试脚本
# 用法：
#   ./test/run.sh            # 运行所有测试（local + EC + distributed）
#   ./test/run.sh local      # 仅运行 local 模式测试（Phase 1-5, 7, 8）
#   ./test/run.sh ec         # 仅运行 EC 模式测试（Phase 5）
#   ./test/run.sh distributed # 仅运行分布式测试（Phase 6）
#   ./test/run.sh unit       # 仅运行单元测试

set -euo pipefail
cd "$(dirname "$0")/../.."
ROOT="$(pwd)"

# 记录启动的后台服务器 PID，仅清理这些进程。
SERVER_PIDS=()

cleanup() {
    for pid in "${SERVER_PIDS[@]}"; do
        kill "$pid" 2>/dev/null || true
    done
    wait 2>/dev/null || true
}
trap cleanup EXIT

fail() { echo "FAIL: $*"; exit 1; }

echo "==> Building server..."
go build -o "$ROOT/server" ./cmd/server/ || fail "build failed"

run_local_tests() {
    echo ""
    echo "========================================"
    echo "  Local Mode Tests (Phase 1-5, 7, 8)"
    echo "========================================"

    cleanup
    echo "==> Starting local server..."
    "$ROOT/server" --root "$ROOT/testdata/local" > /tmp/tos-local.log 2>&1 &
    SERVER_PIDS+=($!)
    sleep 1

    echo "==> Running Phase 1-5, 7, 8..."
    go run ./test/ 2>&1
    LOCAL_EXIT=$?

    cleanup
    SERVER_PIDS=()
    return $LOCAL_EXIT
}

run_ec_tests() {
    echo ""
    echo "========================================"
    echo "  EC Mode Tests (Phase 5)"
    echo "========================================"

    cleanup
    # 清理旧 EC 数据
    rm -rf "$ROOT/testdata/ec"

    echo "==> Starting EC server..."
    "$ROOT/server" --config ./test/ec-config.json --root "$ROOT/testdata/ec" > /tmp/tos-ec.log 2>&1 &
    SERVER_PIDS+=($!)
    sleep 1

    echo "==> Running Phase 5..."
    go run ./test/ phase5 2>&1
    EC_EXIT=$?

    cleanup
    SERVER_PIDS=()
    rm -rf "$ROOT/testdata/ec"
    return $EC_EXIT
}

run_distributed_tests() {
    echo ""
    echo "========================================"
    echo "  Distributed Mode Tests (Phase 6)"
    echo "========================================"

    cleanup
    # 清理旧分布式数据
    rm -rf "$ROOT/testdata/dist-node-{1,2,3}"

    echo "==> Running Phase 6 (auto-starts 3 nodes)..."
    go run ./test/ phase6 2>&1
    DIST_EXIT=$?

    cleanup
    SERVER_PIDS=()
    rm -rf "$ROOT/testdata/dist-node-{1,2,3}"
    return $DIST_EXIT
}

run_unit_tests() {
    echo ""
    echo "========================================"
    echo "  Unit Tests"
    echo "========================================"

    echo "==> Running hash tests..."
    go test ./src/hash/... -v 2>&1

    echo "==> Running cluster tests..."
    go test ./src/cluster/... -v 2>&1

    echo "==> Running EC tests..."
    go test ./src/ec/... -v 2>&1
}

MODE="${1:-all}"

case "$MODE" in
    local)       run_local_tests ;;
    ec)          run_ec_tests ;;
    distributed) run_distributed_tests ;;
    unit)        run_unit_tests ;;
    all)
        FAILED=0
        run_local_tests      || FAILED=1
        run_ec_tests         || FAILED=1
        run_distributed_tests || FAILED=1
        run_unit_tests       || FAILED=1
        if [ $FAILED -ne 0 ]; then
            echo ""
            echo "!!! SOME TESTS FAILED !!!"
            exit 1
        fi
        ;;
    *)
        echo "Usage: $0 {all|local|ec|distributed|unit}"
        exit 1
        ;;
esac

echo ""
echo "========================================"
echo "  ALL TESTS PASSED"
echo "========================================"
