package main

func init() {
	registerTest("Phase 12", testPhase12)
}

func testPhase12() {
	const bucket = "cors-test-bucket"
	const key = "cors-test-object"

	Do2("DELETE", "/"+bucket+"/"+key, "", "")
	Do2("DELETE", "/"+bucket, "", "")
	Do2("PUT", "/"+bucket, "", "")
	Do2("PUT", "/"+bucket+"/"+key, "cors content", "text/plain")

	// --- CORS 启用（默认配置）---

	// 带 Origin 的 GET 请求返回 CORS 头。
	_, _, h := DoWithHeaders("GET", "/"+bucket+"/"+key, "", "", map[string]string{"Origin": "http://localhost:5173"})
	Pass("GET with Origin → Allow-Origin is set", h.Get("Access-Control-Allow-Origin") != "")
	Pass("GET with Origin → Expose-Headers ETag", h.Get("Access-Control-Expose-Headers") == "ETag")

	// OPTIONS preflight 返回 204 + CORS 头。
	_, _, h = DoWithHeaders("OPTIONS", "/"+bucket+"/"+key, "", "", map[string]string{"Origin": "http://localhost:5173"})
	Pass("OPTIONS preflight → 204 status", h.Get("Access-Control-Max-Age") == "3600")

	// 无 Origin 的请求不带 CORS 头。
	_, _, h = DoWithHeaders("GET", "/"+bucket+"/"+key, "", "", map[string]string{})
	Pass("GET without Origin → no Allow-Origin", h.Get("Access-Control-Allow-Origin") == "")

	// 通配符 "*" 匹配任意 origin。
	_, _, h = DoWithHeaders("GET", "/"+bucket+"/"+key, "", "", map[string]string{"Origin": "http://any-origin.example.com"})
	Pass("Wildcard origin → Allow-Origin is *", h.Get("Access-Control-Allow-Origin") == "*")

	// 通配符 "*" 也匹配任意 origin（包括非预期域名）。
	_, _, h = DoWithHeaders("GET", "/"+bucket+"/"+key, "", "", map[string]string{"Origin": "http://evil.com"})
	Pass("Wildcard matches any origin", h.Get("Access-Control-Allow-Origin") == "*")

	// PUT 请求也返回 CORS 头。
	_, _, h = DoWithHeaders("PUT", "/"+bucket+"/cors-put-test", "data", "text/plain", map[string]string{"Origin": "http://localhost:5173"})
	Pass("PUT with Origin → Allow-Origin is set", h.Get("Access-Control-Allow-Origin") != "")

	// V4 认证请求 + CORS 头共存。
	_, _, h = DoV4WithHeaders("GET", "/"+bucket+"/"+key, "", "", map[string]string{"Origin": "http://localhost:5173"})
	Pass("V4 auth + Origin → Allow-Origin is set", h.Get("Access-Control-Allow-Origin") != "")

	// ListBuckets（无认证）+ CORS 头。
	_, _, h = DoWithHeaders("GET", "/", "", "", map[string]string{"Origin": "http://localhost:5173"})
	Pass("ListBuckets + Origin → Allow-Origin is set", h.Get("Access-Control-Allow-Origin") != "")

	// OPTIONS 返回 204 状态码。
	s, _, _ := DoWithHeaders("OPTIONS", "/"+bucket+"/"+key, "", "", map[string]string{"Origin": "http://localhost:5173"})
	Pass("OPTIONS preflight → 204", s == 204)

	// OPTIONS 包含 Allow-Methods 和 Allow-Headers。
	_, _, h = DoWithHeaders("OPTIONS", "/"+bucket, "", "", map[string]string{"Origin": "http://localhost:5173"})
	Pass("OPTIONS → Allow-Methods", h.Get("Access-Control-Allow-Methods") != "")
	Pass("OPTIONS → Allow-Headers", h.Get("Access-Control-Allow-Headers") != "")

	// 清理。
	Do2("DELETE", "/"+bucket+"/cors-put-test", "", "")
	Do2("DELETE", "/"+bucket+"/"+key, "", "")
	Do2("DELETE", "/"+bucket, "", "")

	P("INFO: Phase 12 CORS complete")
}
