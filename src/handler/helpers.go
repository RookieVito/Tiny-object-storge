package handler

import (
	"encoding/base64"
	"strconv"
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
