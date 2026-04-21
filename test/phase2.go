package main

import (
	"strings"
	"time"
)

func init() {
	registerTest("Phase 2", testPhase2)
}

func testPhase2() {
	ct := "application/octet-stream"
	bucket := "p2-bucket"

	// --- Auth ---

	status, _ := Do2("PUT", "/"+bucket, "", "")
	Pass("Auth: CreateBucket", status == 200)

	status, body := DoNoAuth("PUT", "/no-auth-p2")
	Pass("No auth → 403", status == 403)
	Pass("No auth error XML", strings.Contains(body, "AccessDenied"))

	status, body = DoRaw("PUT", "/bad-sig-p2", map[string]string{
		"Authorization": "AWS " + AccessKey + ":badsig",
		"Date":          time.Now().UTC().Format(time.RFC1123),
	})
	Pass("Bad sig → 403", status == 403)
	Pass("Bad sig error XML", strings.Contains(body, "SignatureDoesNotMatch"))

	// --- Put objects ---

	keys := []string{"a/b/c", "a/b/d", "a/e", "f", "readme.md"}
	for _, key := range keys {
		status, _ = Do2("PUT", "/"+bucket+"/"+key, "data-"+key, ct)
		Pass("PutObject /"+bucket+"/"+key, status == 200)
	}

	// --- ListObjectsV2 ---

	status, body = Do2("GET", "/"+bucket, "", "")
	Pass("ListObjects status", status == 200)
	Pass("ListObjects has 5 keys", strings.Count(body, "<Key>") == 5)
	Pass("ListObjects has a/b/c", strings.Contains(body, "<Key>a/b/c</Key>"))
	Pass("ListObjects has f", strings.Contains(body, "<Key>f</Key>"))

	// Delimiter grouping
	status, body = Do2("GET", "/"+bucket+"?delimiter=/", "", "")
	Pass("List delimiter=/ status", status == 200)
	Pass("List delimiter=/ Contents[f]", strings.Contains(body, "<Key>f</Key>"))
	Pass("List delimiter=/ CommonPrefix[a/]", strings.Contains(body, "<Prefix>a/</Prefix>"))

	// Prefix + delimiter
	status, body = Do2("GET", "/"+bucket+"?prefix=a/&delimiter=/", "", "")
	Pass("List prefix=a/ delimiter=/ status", status == 200)
	Pass("List Contents[a/e]", strings.Contains(body, "<Key>a/e</Key>"))
	Pass("List CommonPrefix[a/b/]", strings.Contains(body, "<Prefix>a/b/</Prefix>"))

	// max-keys pagination
	status, body = Do2("GET", "/"+bucket+"?max-keys=2", "", "")
	Pass("List max-keys=2 status", status == 200)
	Pass("List max-keys=2 truncated", strings.Contains(body, "<IsTruncated>true</IsTruncated>"))
	Pass("List max-keys=2 keyCount=2", strings.Count(body, "<Key>") == 2)

	// --- Content-Type auto-detect ---

	status, _ = Do2("PUT", "/"+bucket+"/auto.html", "<!DOCTYPE html><html></html>", "")
	Pass("PutObject auto.html", status == 200)

	_, _, hdrs := Do("HEAD", "/"+bucket+"/auto.html", "", "")
	Pass("Auto-detect text/html", strings.Contains(hdrs.Get("Content-Type"), "text/html"))

	// --- Get object ---

	_, body = Do2("GET", "/"+bucket+"/f", "", "")
	Pass("GetObject content", strings.Contains(body, "data-f"))

	// --- Get without auth ---

	status, _ = DoNoAuth("GET", "/"+bucket+"/f")
	Pass("GetNoAuth → 403", status == 403)

	// --- Clean up ---

	for _, key := range keys {
		Do2("DELETE", "/"+bucket+"/"+key, "", "")
	}
	Do2("DELETE", "/"+bucket+"/auto.html", "", "")
	status, _ = Do2("DELETE", "/"+bucket, "", "")
	Pass("DeleteBucket cleanup", status == 204)
}
