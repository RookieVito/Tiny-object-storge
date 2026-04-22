package main

import (
	"encoding/xml"
	"fmt"
	"strings"
)

func init() {
	registerTest("Phase 8", testPhase8)
}

func testPhase8() {
	bucket := "p8-multipart"
	key := "large-file.bin"
	ct := "application/octet-stream"

	// 清理：确保测试 bucket 不存在。
	Do2("DELETE", "/"+bucket+"/"+key, "", "")
	Do2("DELETE", "/"+bucket+"/abort-file.bin", "", "")
	Do2("DELETE", "/"+bucket+"/err.txt", "", "")
	Do2("DELETE", "/"+bucket+"/small-parts.bin", "", "")
	Do2("DELETE", "/"+bucket+"/overwrite-file.bin", "", "")
	Do2("DELETE", "/"+bucket, "", "")

	// 创建测试 bucket（409 也说明已存在，可以接受）。
	status, _ := Do2("PUT", "/"+bucket, "", "")
	Pass("CreateBucket", status == 200 || status == 409)
	defer func() {
		// 清理测试 bucket。
		Do2("DELETE", "/"+bucket+"/"+key, "", "")
		Do2("DELETE", "/"+bucket, "", "")
	}()

	// ============================================================
	// 1. InitiateMultipartUpload
	// ============================================================
	status, body := Do2("POST", "/"+bucket+"/"+key+"?uploads", "", ct)
	Pass("InitiateMultipartUpload → 200", status == 200)

	type initiateResult struct {
		XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
		Bucket   string   `xml:"Bucket"`
		Key      string   `xml:"Key"`
		UploadId string   `xml:"UploadId"`
	}
	var initRes initiateResult
	xml.Unmarshal([]byte(body), &initRes)
	uploadId := initRes.UploadId
	Pass("InitiateMultipartUpload returns UploadId", uploadId != "")
	Pass("InitiateMultipartUpload bucket/key match", initRes.Bucket == bucket && initRes.Key == key)

	// ============================================================
	// 2. UploadPart — 上传 3 个 part
	// ============================================================
	part1Data := strings.Repeat("A", 5<<20) // 5 MB
	part2Data := strings.Repeat("B", 5<<20) // 5 MB
	part3Data := strings.Repeat("C", 1<<20) // 1 MB（最后一个 part 可以 < 5MB）

	status, _ = Do2("PUT", fmt.Sprintf("/%s/%s?partNumber=1&uploadId=%s", bucket, key, uploadId), part1Data, ct)
	Pass("UploadPart 1 → 200", status == 200)

	status, body = Do2("PUT", fmt.Sprintf("/%s/%s?partNumber=2&uploadId=%s", bucket, key, uploadId), part2Data, ct)
	Pass("UploadPart 2 → 200", status == 200)

	status, body = Do2("PUT", fmt.Sprintf("/%s/%s?partNumber=3&uploadId=%s", bucket, key, uploadId), part3Data, ct)
	Pass("UploadPart 3 → 200", status == 200)

	// 记录各 part 的 ETag。
	_, body = Do2("PUT", fmt.Sprintf("/%s/%s?partNumber=1&uploadId=%s", bucket, key, uploadId), part1Data, ct)
	etag1 := body // Do2 返回 body，但 ETag 在 header 中，需要用 Do

	_, body, headers := Do("PUT", fmt.Sprintf("/%s/%s?partNumber=1&uploadId=%s", bucket, key, uploadId), part1Data, ct)
	etag1 = headers.Get("ETag")
	Pass("UploadPart returns ETag header", etag1 != "")

	_, _, headers = Do("PUT", fmt.Sprintf("/%s/%s?partNumber=2&uploadId=%s", bucket, key, uploadId), part2Data, ct)
	etag2 := headers.Get("ETag")

	_, _, headers = Do("PUT", fmt.Sprintf("/%s/%s?partNumber=3&uploadId=%s", bucket, key, uploadId), part3Data, ct)
	etag3 := headers.Get("ETag")

	_ = body // 避免 unused 警告

	// ============================================================
	// 3. ListParts
	// ============================================================
	status, body = Do2("GET", fmt.Sprintf("/%s/%s?uploadId=%s", bucket, key, uploadId), "", "")
	Pass("ListParts → 200", status == 200)

	type listPartsResult struct {
		XMLName  xml.Name `xml:"ListPartsResult"`
		Bucket   string   `xml:"Bucket"`
		Key      string   `xml:"Key"`
		UploadId string   `xml:"UploadId"`
		Parts    []struct {
			PartNumber int    `xml:"PartNumber"`
			ETag       string `xml:"ETag"`
			Size       int64  `xml:"Size"`
		} `xml:"Part"`
	}
	var listRes listPartsResult
	xml.Unmarshal([]byte(body), &listRes)
	Pass("ListParts returns 3 parts", len(listRes.Parts) == 3)
	if len(listRes.Parts) >= 3 {
		Pass("ListParts part sizes correct", listRes.Parts[0].Size == 5<<20 && listRes.Parts[1].Size == 5<<20 && listRes.Parts[2].Size == 1<<20)
		Pass("ListParts sorted by PartNumber", listRes.Parts[0].PartNumber == 1 && listRes.Parts[1].PartNumber == 2 && listRes.Parts[2].PartNumber == 3)
	}

	// ============================================================
	// 4. 进行中的 multipart 在 ListObjects 中不可见
	// ============================================================
	status, body = Do2("GET", "/"+bucket, "", ct)
	Pass("ListObjects does not show in-progress multipart key", status == 200 && !strings.Contains(body, "<Key>"+key+"</Key>"))

	// ============================================================
	// 5. ListMultipartUploads
	// ============================================================
	status, body = Do2("GET", "/"+bucket+"?uploads", "", ct)
	Pass("ListMultipartUploads → 200", status == 200)

	type listUploadsResult struct {
		XMLName xml.Name `xml:"ListMultipartUploadsResult"`
		Uploads []struct {
			UploadId string `xml:"UploadId"`
			Key      string `xml:"Key"`
		} `xml:"Upload"`
	}
	var listUploadsRes listUploadsResult
	xml.Unmarshal([]byte(body), &listUploadsRes)
	Pass("ListMultipartUploads returns our upload", len(listUploadsRes.Uploads) >= 1)

	found := false
	for _, u := range listUploadsRes.Uploads {
		if u.UploadId == uploadId && u.Key == key {
			found = true
			break
		}
	}
	Pass("ListMultipartUploads contains our uploadId", found)

	// ============================================================
	// 6. CompleteMultipartUpload
	// ============================================================
	completeXml := fmt.Sprintf(`<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>%s</ETag></Part><Part><PartNumber>2</PartNumber><ETag>%s</ETag></Part><Part><PartNumber>3</PartNumber><ETag>%s</ETag></Part></CompleteMultipartUpload>`, etag1, etag2, etag3)
	status, body = Do2("POST", fmt.Sprintf("/%s/%s?uploadId=%s", bucket, key, uploadId), completeXml, "application/xml")
	Pass("CompleteMultipartUpload → 200", status == 200)

	type completeResult struct {
		XMLName xml.Name `xml:"CompleteMultipartUploadResult"`
		ETag    string   `xml:"ETag"`
		Key     string   `xml:"Key"`
	}
	var compRes completeResult
	xml.Unmarshal([]byte(body), &compRes)
	Pass("CompleteMultipartUpload returns ETag with -3 suffix", strings.HasSuffix(compRes.ETag, "-3\"") && strings.HasPrefix(compRes.ETag, "\""))

	// ============================================================
	// 7. 验证最终对象可通过 GET 读取
	// ============================================================
	status, body = Do2("GET", "/"+bucket+"/"+key, "", "")
	Pass("GetObject assembled file → 200", status == 200)
	expectedContent := part1Data + part2Data + part3Data
	Pass("GetObject content matches assembled parts", body == expectedContent)

	// ============================================================
	// 8. 测试 AbortMultipartUpload
	// ============================================================
	abortKey := "abort-file.bin"
	status, body = Do2("POST", "/"+bucket+"/"+abortKey+"?uploads", "", ct)
	Pass("Initiate abort upload → 200", status == 200)
	var abortInitRes initiateResult
	xml.Unmarshal([]byte(body), &abortInitRes)
	abortUploadId := abortInitRes.UploadId

	// 上传一个 part。
	Do2("PUT", fmt.Sprintf("/%s/%s?partNumber=1&uploadId=%s", bucket, abortKey, abortUploadId), part1Data, ct)

	// Abort。
	status, _ = Do2("DELETE", fmt.Sprintf("/%s/%s?uploadId=%s", bucket, abortKey, abortUploadId), "", "")
	Pass("AbortMultipartUpload → 204", status == 204)

	// Abort 后对象不应存在。
	status, _ = Do2("GET", "/"+bucket+"/"+abortKey, "", "")
	Pass("GetObject after abort → 404", status == 404)

	// ============================================================
	// 9. 错误处理
	// ============================================================

	// 无效 uploadId。
	status, _ = Do2("PUT", "/"+bucket+"/err.txt?partNumber=1&uploadId=invalid-upload-id", "data", ct)
	Pass("UploadPart invalid uploadId → error", status == 404)

	// 无效 partNumber。
	status, body = Do2("POST", "/"+bucket+"/err.txt?uploads", "", ct)
	var errInitRes initiateResult
	xml.Unmarshal([]byte(body), &errInitRes)
	errUploadId := errInitRes.UploadId

	status, _ = Do2("PUT", fmt.Sprintf("/%s/err.txt?partNumber=0&uploadId=%s", bucket, errUploadId), "data", ct)
	Pass("UploadPart partNumber=0 → 400", status == 400)

	status, _ = Do2("PUT", fmt.Sprintf("/%s/err.txt?partNumber=10001&uploadId=%s", bucket, errUploadId), "data", ct)
	Pass("UploadPart partNumber=10001 → 400", status == 400)

	// 清理。
	Do2("DELETE", fmt.Sprintf("/%s/err.txt?uploadId=%s", bucket, errUploadId), "", "")

	// EntityTooSmall — 非 final part 小于 5MB。
	status, body = Do2("POST", "/"+bucket+"/small-parts.bin?uploads", "", ct)
	var smallInitRes initiateResult
	xml.Unmarshal([]byte(body), &smallInitRes)
	smallUploadId := smallInitRes.UploadId

	// 上传两个小 part。
	Do2("PUT", fmt.Sprintf("/%s/small-parts.bin?partNumber=1&uploadId=%s", bucket, smallUploadId), "small1", ct)
	_, _, h := Do("PUT", fmt.Sprintf("/%s/small-parts.bin?partNumber=2&uploadId=%s", bucket, smallUploadId), "small2", ct)
	etagS1 := ""
	etagS2 := h.Get("ETag")

	// 重新上传获取正确 ETag。
	_, _, h = Do("PUT", fmt.Sprintf("/%s/small-parts.bin?partNumber=1&uploadId=%s", bucket, smallUploadId), "small1", ct)
	etagS1 = h.Get("ETag")
	_ = etag1 // suppress unused

	smallCompleteXml := fmt.Sprintf(`<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>%s</ETag></Part><Part><PartNumber>2</PartNumber><ETag>%s</ETag></Part></CompleteMultipartUpload>`, etagS1, etagS2)
	status, _ = Do2("POST", fmt.Sprintf("/%s/small-parts.bin?uploadId=%s", bucket, smallUploadId), smallCompleteXml, "application/xml")
	Pass("Complete with non-final part < 5MB → EntityTooSmall", status == 400)

	// 清理。
	Do2("DELETE", fmt.Sprintf("/%s/small-parts.bin?uploadId=%s", bucket, smallUploadId), "", "")

	// ============================================================
	// 10. 覆盖同一 partNumber 后 Complete 使用最新版本
	// ============================================================
	overwriteKey := "overwrite-file.bin"
	status, body = Do2("POST", "/"+bucket+"/"+overwriteKey+"?uploads", "", ct)
	Pass("Initiate overwrite upload → 200", status == 200)
	var owInitRes initiateResult
	xml.Unmarshal([]byte(body), &owInitRes)
	owUploadId := owInitRes.UploadId

	// 上传 part 1（版本 A）。
	_, _, h = Do("PUT", fmt.Sprintf("/%s/%s?partNumber=1&uploadId=%s", bucket, overwriteKey, owUploadId), "AAAA", ct)
	etagOw1_A := h.Get("ETag")

	// 上传 part 1（版本 B）覆盖。
	_, _, h = Do("PUT", fmt.Sprintf("/%s/%s?partNumber=1&uploadId=%s", bucket, overwriteKey, owUploadId), "BBBB", ct)
	etagOw1_B := h.Get("ETag")

	Pass("Overwrite part returns different ETag", etagOw1_A != etagOw1_B)

	owCompleteXml := fmt.Sprintf(`<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>%s</ETag></Part></CompleteMultipartUpload>`, etagOw1_B)
	status, _ = Do2("POST", fmt.Sprintf("/%s/%s?uploadId=%s", bucket, overwriteKey, owUploadId), owCompleteXml, "application/xml")
	Pass("Complete overwrite → 200", status == 200)

	// 验证最终内容是版本 B。
	status, body = Do2("GET", "/"+bucket+"/"+overwriteKey, "", "")
	Pass("GetObject after overwrite contains version B data", status == 200 && body == "BBBB")

	// 清理。
	Do2("DELETE", "/"+bucket+"/"+overwriteKey, "", "")
}
