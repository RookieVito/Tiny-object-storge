package main

import (
	"encoding/xml"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func init() {
	registerTest("Phase 13", testPhase13)
}

func testPhase13() {
	testPhase13EC()
	testPhase13Distributed()
}

// ============================================================
// EC Multipart Upload 测试
// 前提：服务器以 EC 模式启动
//   go run ./cmd/server/ --config test/ec-config.json
// ============================================================
func testPhase13EC() {
	bucket := "p13-ec-bucket"
	key := "ec-multipart.bin"
	ct := "application/octet-stream"

	// 清理。
	Do2("DELETE", "/"+bucket+"/"+key, "", "")
	Do2("DELETE", "/"+bucket+"/ec-abort.bin", "", "")
	Do2("DELETE", "/"+bucket, "", "")

	status, _ := Do2("PUT", "/"+bucket, "", "")
	Pass("EC Multipart: CreateBucket", status == 200 || status == 409)

	defer func() {
		Do2("DELETE", "/"+bucket+"/"+key, "", "")
		Do2("DELETE", "/"+bucket+"/ec-abort.bin", "", "")
		Do2("DELETE", "/"+bucket, "", "")
	}()

	// 1. InitiateMultipartUpload
	status, body := Do2("POST", "/"+bucket+"/"+key+"?uploads", "", ct)
	Pass("EC Multipart: InitiateUpload → 200", status == 200)

	type initiateResult struct {
		XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
		Bucket   string   `xml:"Bucket"`
		Key      string   `xml:"Key"`
		UploadId string   `xml:"UploadId"`
	}
	var initRes initiateResult
	xml.Unmarshal([]byte(body), &initRes)
	uploadId := initRes.UploadId
	Pass("EC Multipart: UploadId returned", uploadId != "")

	// 2. UploadPart — 2 个 part（EC 模式使用 LocalBackend 的 multipart 逻辑，不需要 5MB 限制）
	part1Data := strings.Repeat("A", 1024) // 1 KB
	part2Data := strings.Repeat("B", 2048) // 2 KB

	status1, _, h1 := Do("PUT", fmt.Sprintf("/%s/%s?partNumber=1&uploadId=%s", bucket, key, uploadId), part1Data, ct)
	status2, _, h2 := Do("PUT", fmt.Sprintf("/%s/%s?partNumber=2&uploadId=%s", bucket, key, uploadId), part2Data, ct)
	Pass("EC Multipart: UploadPart 1/2 → 200", status1 == 200 && status2 == 200)
	etag1 := h1.Get("ETag")
	etag2 := h2.Get("ETag")
	Pass("EC Multipart: UploadPart returns ETag", etag1 != "" && etag2 != "")

	// 3. ListParts
	status, body = Do2("GET", fmt.Sprintf("/%s/%s?uploadId=%s", bucket, key, uploadId), "", "")
	Pass("EC Multipart: ListParts → 200", status == 200)

	type listPartsResult struct {
		XMLName xml.Name `xml:"ListPartsResult"`
		Parts   []struct {
			PartNumber int    `xml:"PartNumber"`
			Size       int64  `xml:"Size"`
			ETag       string `xml:"ETag"`
		} `xml:"Part"`
	}
	var listRes listPartsResult
	xml.Unmarshal([]byte(body), &listRes)
	Pass("EC Multipart: ListParts returns 2 parts", len(listRes.Parts) == 2)
	if len(listRes.Parts) >= 2 {
		Pass("EC Multipart: ListParts sizes correct", listRes.Parts[0].Size == 1024 && listRes.Parts[1].Size == 2048)
	}

	// 4. ListMultipartUploads
	status, body = Do2("GET", "/"+bucket+"?uploads", "", "")
	Pass("EC Multipart: ListUploads → 200", status == 200)
	Pass("EC Multipart: ListUploads contains uploadId", strings.Contains(body, uploadId))

	// 5. CompleteMultipartUpload
	completeXml := fmt.Sprintf(`<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>%s</ETag></Part><Part><PartNumber>2</PartNumber><ETag>%s</ETag></Part></CompleteMultipartUpload>`, etag1, etag2)
	status, body = Do2("POST", fmt.Sprintf("/%s/%s?uploadId=%s", bucket, key, uploadId), completeXml, "application/xml")
	Pass("EC Multipart: CompleteUpload → 200", status == 200)

	type completeResult struct {
		XMLName xml.Name `xml:"CompleteMultipartUploadResult"`
		ETag    string   `xml:"ETag"`
		Key     string   `xml:"Key"`
	}
	var compRes completeResult
	xml.Unmarshal([]byte(body), &compRes)
	Pass("EC Multipart: CompleteUpload ETag with -2 suffix", strings.HasSuffix(compRes.ETag, "-2\""))

	// 6. 验证最终对象可读取且内容正确
	status, body = Do2("GET", "/"+bucket+"/"+key, "", "")
	Pass("EC Multipart: GetObject → 200", status == 200)
	expectedContent := part1Data + part2Data
	Pass("EC Multipart: GetObject content matches", body == expectedContent)

	// 7. AbortMultipartUpload
	abortKey := "ec-abort.bin"
	status, body = Do2("POST", "/"+bucket+"/"+abortKey+"?uploads", "", ct)
	Pass("EC Multipart: Initiate abort upload → 200", status == 200)
	var abortInitRes initiateResult
	xml.Unmarshal([]byte(body), &abortInitRes)
	abortUploadId := abortInitRes.UploadId

	Do("PUT", fmt.Sprintf("/%s/%s?partNumber=1&uploadId=%s", bucket, abortKey, abortUploadId), "abort-data", ct)
	status, _ = Do2("DELETE", fmt.Sprintf("/%s/%s?uploadId=%s", bucket, abortKey, abortUploadId), "", "")
	Pass("EC Multipart: AbortUpload → 204", status == 204)

	status, _ = Do2("GET", "/"+bucket+"/"+abortKey, "", "")
	Pass("EC Multipart: GetObject after abort → 404", status == 404)
}

// ============================================================
// 分布式 Multipart Upload 测试
// 自动启动 3 节点集群（复用 Phase 6 模式）
// ============================================================
func testPhase13Distributed() {
	tmpDir, err := os.MkdirTemp("", "phase13-dist-*")
	if err != nil {
		Pass("Distributed Multipart: create temp dir", false)
		return
	}
	defer os.RemoveAll(tmpDir)

	ports := []int{19201, 19202, 19203}
	nodeIDs := []string{
		fmt.Sprintf("localhost:%d", ports[0]),
		fmt.Sprintf("localhost:%d", ports[1]),
		fmt.Sprintf("localhost:%d", ports[2]),
	}
	baseURLs := []string{
		fmt.Sprintf("http://localhost:%d", ports[0]),
		fmt.Sprintf("http://localhost:%d", ports[1]),
		fmt.Sprintf("http://localhost:%d", ports[2]),
	}

	// 预编译服务器二进制。
	binPath := filepath.Join(tmpDir, "server")
	{
		buildCmd := exec.Command("go", "build", "-o", binPath, "./cmd/server/")
		buildCmd.Dir = "/home/vito/workspace/tiny-object-storge"
		if out, err := buildCmd.CombinedOutput(); err != nil {
			Pass("Distributed Multipart: build server", false)
			fmt.Fprintf(os.Stderr, "build error: %s\n%s\n", err, string(out))
			return
		}
		Pass("Distributed Multipart: build server", true)
	}

	// 确保端口未被占用。
	for _, p := range ports {
		for i := 0; i < 10; i++ {
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", p), 100*time.Millisecond)
			if err != nil {
				break // 端口空闲
			}
			conn.Close()
			time.Sleep(500 * time.Millisecond)
		}
	}

	// 启动 3 个节点。
	cmds := make([]*exec.Cmd, 3)
	for i := 0; i < 3; i++ {
		seeds := make([]string, 0, 2)
		for j := 0; j < 3; j++ {
			if j != i {
				seeds = append(seeds, nodeIDs[j])
			}
		}
		cmd, err := startNode(ports[i], nodeIDs[i], seeds, tmpDir)
		if err != nil {
			Pass(fmt.Sprintf("Distributed Multipart: start node %d", i+1), false)
			cleanupNodes(cmds)
			return
		}
		cmds[i] = cmd
		time.Sleep(300 * time.Millisecond)
	}
	defer cleanupNodes(cmds)

	// 等待集群收敛。
	clusterReady := false
	for i := 0; i < 40; i++ {
		time.Sleep(500 * time.Millisecond)
		alive := 0
		for _, url := range baseURLs {
			s, _ := httpGet(url + "/_cluster/members")
			if s == 200 {
				alive++
			}
		}
		if alive >= 3 {
			clusterReady = true
			break
		}
	}
	Pass("Distributed Multipart: cluster ready", clusterReady)
	if !clusterReady {
		return
	}

	bucket := "p13-dist-bucket"
	key := "dist-multipart.bin"
	ct := "application/octet-stream"

	// 创建 bucket
	status, _, _ := distDo("PUT", baseURLs[0]+"/"+bucket, "", "")
	Pass("Distributed Multipart: CreateBucket", status == 200)

	defer func() {
		distDo("DELETE", baseURLs[0]+"/"+bucket+"/"+key, "", "")
		distDo("DELETE", baseURLs[0]+"/"+bucket+"/dist-abort.bin", "", "")
		distDo("DELETE", baseURLs[0]+"/"+bucket, "", "")
	}()

	// 1. InitiateMultipartUpload
	status, body, _ := distDo("POST", baseURLs[0]+"/"+bucket+"/"+key+"?uploads", "", ct)
	Pass("Distributed Multipart: InitiateUpload → 200", status == 200)

	type distInitResult struct {
		XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
		Bucket   string   `xml:"Bucket"`
		Key      string   `xml:"Key"`
		UploadId string   `xml:"UploadId"`
	}
	var initRes distInitResult
	xml.Unmarshal([]byte(body), &initRes)
	uploadId := initRes.UploadId
	Pass("Distributed Multipart: UploadId returned", uploadId != "")

	// 2. UploadPart
	part1Data := strings.Repeat("X", 512)
	part2Data := strings.Repeat("Y", 1024)

	_, _, hdrs1 := distDo("PUT", fmt.Sprintf("%s/%s/%s?partNumber=1&uploadId=%s", baseURLs[0], bucket, key, uploadId), part1Data, ct)
	_, _, hdrs2 := distDo("PUT", fmt.Sprintf("%s/%s/%s?partNumber=2&uploadId=%s", baseURLs[0], bucket, key, uploadId), part2Data, ct)
	etag1 := hdrs1.Get("ETag")
	etag2 := hdrs2.Get("ETag")
	Pass("Distributed Multipart: UploadPart ETags", etag1 != "" && etag2 != "")

	// 3. ListParts
	status, body, _ = distDo("GET", fmt.Sprintf("%s/%s/%s?uploadId=%s", baseURLs[0], bucket, key, uploadId), "", "")
	Pass("Distributed Multipart: ListParts → 200", status == 200)
	Pass("Distributed Multipart: ListParts contains parts", strings.Contains(body, "<PartNumber>1</PartNumber>") && strings.Contains(body, "<PartNumber>2</PartNumber>"))

	// 4. CompleteMultipartUpload
	completeXml := fmt.Sprintf(`<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>%s</ETag></Part><Part><PartNumber>2</PartNumber><ETag>%s</ETag></Part></CompleteMultipartUpload>`, etag1, etag2)
	status, body, _ = distDo("POST", fmt.Sprintf("%s/%s/%s?uploadId=%s", baseURLs[0], bucket, key, uploadId), completeXml, "application/xml")
	Pass("Distributed Multipart: CompleteUpload → 200", status == 200)

	// 5. 验证最终对象
	time.Sleep(500 * time.Millisecond) // 等待 PutObject 完成
	status, body, _ = distDo("GET", baseURLs[0]+"/"+bucket+"/"+key, "", "")
	Pass("Distributed Multipart: GetObject → 200", status == 200)
	expectedContent := part1Data + part2Data
	Pass("Distributed Multipart: GetObject content matches", body == expectedContent)

	// 6. 从其他节点也能读取
	time.Sleep(500 * time.Millisecond)
	status2, body2, _ := distDo("GET", baseURLs[1]+"/"+bucket+"/"+key, "", "")
	Pass("Distributed Multipart: GetObject from node2 → 200", status2 == 200)
	Pass("Distributed Multipart: GetObject from node2 content matches", body2 == expectedContent)

	// 7. AbortMultipartUpload
	abortKey := "dist-abort.bin"
	status, body, _ = distDo("POST", baseURLs[0]+"/"+bucket+"/"+abortKey+"?uploads", "", ct)
	Pass("Distributed Multipart: Initiate abort upload → 200", status == 200)
	var abortRes distInitResult
	xml.Unmarshal([]byte(body), &abortRes)
	abortUploadId := abortRes.UploadId

	distDo("PUT", fmt.Sprintf("%s/%s/%s?partNumber=1&uploadId=%s", baseURLs[0], bucket, abortKey, abortUploadId), "abort-data", ct)
	status, _, _ = distDo("DELETE", fmt.Sprintf("%s/%s/%s?uploadId=%s", baseURLs[0], bucket, abortKey, abortUploadId), "", "")
	Pass("Distributed Multipart: AbortUpload → 204", status == 204)

	status, _, _ = distDo("GET", baseURLs[0]+"/"+bucket+"/"+abortKey, "", "")
	Pass("Distributed Multipart: GetObject after abort → 404", status == 404)
}
