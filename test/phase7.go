package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Phase 7 tests: CLI 客户端集成测试。
// 通过调用 go run ./cmd/client/ 测试所有子命令。

func init() {
	registerTest("Phase 7", testPhase7)
}

func runCli(args ...string) (string, int) {
	cmdArgs := append([]string{"run", "./cmd/client/"}, args...)
	cmd := exec.Command("go", cmdArgs...)
	out, _ := cmd.CombinedOutput()
	return string(out), cmd.ProcessState.ExitCode()
}

func testPhase7() {
	clientBin := "go run ./cmd/client/"

	// 配置 CLI。
	_, exitCode := runCli("config", "--endpoint", BaseURL, "--access-key", AccessKey, "--secret-key", SecretKey)
	Pass("config set", exitCode == 0)

	out, exitCode := runCli("config")
	Pass("config show endpoint", exitCode == 0 && strings.Contains(out, BaseURL))
	Pass("config show access-key", exitCode == 0 && strings.Contains(out, AccessKey))
	Pass("config show secret-key masked", exitCode == 0 && strings.Contains(out, "****"))

	bucket := "p7-cli-bucket"

	// 先清理可能残留的 bucket。
	Do2("DELETE", "/"+bucket, "", "")

	// --- Bucket 操作 ---
	out, exitCode = runCli("mb", bucket)
	Pass("mb create bucket", exitCode == 0 && strings.Contains(out, "created"))

	out, exitCode = runCli("ls")
	Pass("ls list buckets", exitCode == 0 && strings.Contains(out, bucket))

	// --- Object 操作 ---

	// 先用 Do 上传一个测试文件（避免 CLI cp 的进度条干扰测试输出）。
	ct := "text/plain"
	status, _ := Do2("PUT", "/"+bucket+"/cli-test.txt", "Hello from CLI test!", ct)
	Pass("upload test file via API", status == 200)

	out, exitCode = runCli("ls", bucket)
	Pass("ls list objects", exitCode == 0 && strings.Contains(out, "cli-test.txt"))

	out, exitCode = runCli("stat", bucket+"/cli-test.txt")
	Pass("stat object", exitCode == 0 && strings.Contains(out, "cli-test.txt"))
	Pass("stat show size", exitCode == 0 && strings.Contains(out, "Content-Type"))

	out, exitCode = runCli("cat", bucket+"/cli-test.txt")
	Pass("cat object content", exitCode == 0 && strings.Contains(out, "Hello from CLI test!"))

	// cp 下载测试。
	tmpFile := "/tmp/p7-cli-download.txt"
	os.Remove(tmpFile)
	// 使用 2>/dev/null 抑制进度条 stderr 输出
	cmd := exec.Command("go", "run", "./cmd/client/", "cp", "s3://"+bucket+"/cli-test.txt", tmpFile)
	cmd.Stderr = nil // 不捕获 stderr，进度条直接输出
	err := cmd.Run()
	Pass("cp download", err == nil)

	data, err := os.ReadFile(tmpFile)
	Pass("cp download content", err == nil && string(data) == "Hello from CLI test!")
	os.Remove(tmpFile)

	// cp 上传测试。
	uploadSrc := "/tmp/p7-cli-upload.txt"
	os.WriteFile(uploadSrc, []byte("Uploaded from CLI!"), 0644)
	cmd = exec.Command("go", "run", "./cmd/client/", "cp", uploadSrc, "s3://"+bucket+"/uploaded.txt")
	cmd.Stderr = nil
	err = cmd.Run()
	Pass("cp upload", err == nil)
	os.Remove(uploadSrc)

	_, body := Do2("GET", "/"+bucket+"/uploaded.txt", "", "")
	Pass("cp upload content", strings.Contains(body, "Uploaded from CLI!"))

	// rm 测试。
	out, exitCode = runCli("rm", bucket+"/cli-test.txt")
	Pass("rm object", exitCode == 0 && strings.Contains(out, "deleted"))

	// rb 测试。
	out, exitCode = runCli("rm", bucket+"/uploaded.txt")
	Pass("rm uploaded object", exitCode == 0)

	out, exitCode = runCli("rb", bucket)
	Pass("rb remove bucket", exitCode == 0 && strings.Contains(out, "removed"))

	// 验证 bucket 已删除。
	lsOut, exitCode := runCli("ls")
	Pass("ls after rb", exitCode == 0 && !strings.Contains(lsOut, bucket))

	// 清理配置文件。
	cfgPath := fmt.Sprintf("%s/.tiny-storage/config.json", os.Getenv("HOME"))
	os.Remove(cfgPath)

	_ = clientBin // 标记已使用
}
