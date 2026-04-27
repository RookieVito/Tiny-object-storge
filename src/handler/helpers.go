package handler

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// parseMaxKeys 解析 max-keys 查询参数。
func parseMaxKeys(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 1000, err
	}
	if n < 0 {
		return 1000, nil
	}
	return n, nil
}

// decodeToken 解码 base64 编码的 continuation token。
func decodeToken(token string) (string, error) {
	decoded, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

// byteRange 表示一个字节范围（两端均为闭区间）。
type byteRange struct {
	start, end int64 // end == -1 表示"到文件末尾"
}

// parseRangeHeader 解析 HTTP Range 头。
// 支持格式：bytes=0-99, bytes=100-, bytes=-50
// 多 range 或格式错误返回 nil（调用方应回退到 200 全量返回）。
// 语法正确但无法满足的 range 返回 invalid=true。
func parseRangeHeader(header string, size int64) (ranges []byteRange, invalid bool) {
	if !strings.HasPrefix(header, "bytes=") {
		return nil, false
	}
	spec := strings.TrimPrefix(header, "bytes=")
	parts := strings.Split(spec, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		parts2 := strings.SplitN(p, "-", 2)
		if len(parts2) != 2 {
			return nil, false
		}
		var br byteRange
		if parts2[0] == "" {
			// suffix range: bytes=-50
			suffix, err := strconv.ParseInt(parts2[1], 10, 64)
			if err != nil || suffix <= 0 {
				return nil, false
			}
			if suffix > size {
				br.start = 0
			} else {
				br.start = size - suffix
			}
			br.end = size - 1
		} else if parts2[1] == "" {
			// open-ended: bytes=100-
			start, err := strconv.ParseInt(parts2[0], 10, 64)
			if err != nil || start < 0 {
				return nil, false
			}
			if start >= size {
				return nil, true
			}
			br.start = start
			br.end = -1
		} else {
			// explicit range: bytes=0-99
			start, err := strconv.ParseInt(parts2[0], 10, 64)
			if err != nil || start < 0 {
				return nil, false
			}
			end, err := strconv.ParseInt(parts2[1], 10, 64)
			if err != nil || end < 0 {
				return nil, false
			}
			if start > end {
				return nil, true
			}
			if start >= size {
				return nil, true
			}
			br.start = start
			br.end = end
			if br.end >= size {
				br.end = size - 1
			}
		}
		ranges = append(ranges, br)
	}
	return ranges, false
}

// contentRangeValue 生成 Content-Range 头的值。
func contentRangeValue(start, end, total int64) string {
	return fmt.Sprintf("bytes %d-%d/%d", start, end, total)
}
