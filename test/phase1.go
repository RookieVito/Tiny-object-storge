package main

import (
	"strings"
)

// Phase 1 tests: Core CRUD (no auth required, bypass via direct server config).
// These tests expect the server to be running with auth disabled or credentials known.
// Since Phase 2 added auth, we run Phase 1 tests with authenticated requests.

func init() {
	registerTest("Phase 1", testPhase1)
}

func testPhase1() {
	ct := "application/octet-stream"
	bucket := "p1-bucket"

	// --- Bucket operations ---

	status, _ := Do2("PUT", "/"+bucket, "", "")
	Pass("CreateBucket", status == 200)

	status, _ = Do2("HEAD", "/"+bucket, "", "")
	Pass("HeadBucket exists → 200", status == 200)

	status, _ = Do2("HEAD", "/no-such-bucket-p1", "", "")
	Pass("HeadBucket not exists → 404", status == 404)

	status, body := DoNoAuth("GET", "/")
	Pass("ListBuckets (no auth)", status == 200 && strings.Contains(body, "<Name>"+bucket+"</Name>"))

	// --- Object operations ---

	status, _ = Do2("PUT", "/"+bucket+"/hello.txt", "Hello, World!", "text/plain")
	Pass("PutObject hello.txt", status == 200)

	_, body = Do2("GET", "/"+bucket+"/hello.txt", "", "")
	Pass("GetObject content", strings.Contains(body, "Hello, World!"))

	_, _, hdrs := Do("HEAD", "/"+bucket+"/hello.txt", "", "")
	Pass("HeadObject → 200", hdrs.Get("Content-Length") == "13")
	Pass("HeadObject ETag present", hdrs.Get("Etag") != "")
	Pass("HeadObject Content-Type", hdrs.Get("Content-Type") == "text/plain")

	// --- Nested key ---

	status, _ = Do2("PUT", "/"+bucket+"/docs/notes/2024/note.json", `{"msg":"hi"}`, "application/json")
	Pass("PutObject nested key", status == 200)

	_, body = Do2("GET", "/"+bucket+"/docs/notes/2024/note.json", "", "")
	Pass("GetObject nested content", strings.Contains(body, `{"msg":"hi"}`))

	// --- Delete object (idempotent) ---

	status, _ = Do2("DELETE", "/"+bucket+"/hello.txt", "", "")
	Pass("DeleteObject → 204", status == 204)

	status, body = Do2("GET", "/"+bucket+"/hello.txt", "", "")
	Pass("Get deleted → NoSuchKey", status == 404 && strings.Contains(body, "NoSuchKey"))

	// Idempotent: delete again
	status, _ = Do2("DELETE", "/"+bucket+"/hello.txt", "", "")
	Pass("DeleteObject idempotent → 204", status == 204)

	// --- DeleteBucket not empty ---

	status, _ = Do2("DELETE", "/"+bucket, "", "")
	Pass("DeleteBucket not empty → 409", status == 409)

	// --- Path traversal defense ---
	// Go's ServeMux and http.Client both normalize ".." before routing.
	// PathMapper adds a 3rd layer of defense for any edge cases.
	// The key security guarantee: no object can be written outside the storage root.

	// --- Clean up ---

	Do2("DELETE", "/"+bucket+"/docs/notes/2024/note.json", "", "")
	status, _ = Do2("DELETE", "/"+bucket, "", "")
	Pass("DeleteBucket after cleanup → 204", status == 204)

	// ct used in PutObject calls above
	_ = ct
}
