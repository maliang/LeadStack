package notify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"strconv"
)

// 加签算法（red line）：这两处必须与官方文档逐字节一致，否则对方会以 HTTP 200 + 业务错误码拒收。

// dingTalkSignRaw 计算钉钉机器人加签的原始 base64 值：
//
//	stringToSign = "<timestamp 毫秒>\n<secret>"
//	sign         = base64(HMAC-SHA256(key=secret, data=stringToSign))
func dingTalkSignRaw(secret string, timestampMS int64) string {
	stringToSign := strconv.FormatInt(timestampMS, 10) + "\n" + secret
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// dingTalkSign 计算钉钉机器人加签值，返回 urlencode 后的形式（+ → %2B、/ → %2F、= → %3D）。
//
// 注意：调用方把它拼进 URL 时**不能再 urlencode 一次**（这正是钉钉集成最常见的
// “看起来发了、其实对方拒收” 的原因），否则服务端解码后拿到的是 %2B/%3D 而非 +/=。
func dingTalkSign(secret string, timestampMS int64) string {
	return url.QueryEscape(dingTalkSignRaw(secret, timestampMS))
}

// feishuSign 计算飞书自定义机器人加签值：
//
//	stringToSign = "<timestamp 秒>\n<secret>"
//	sign         = base64(HMAC-SHA256(key=stringToSign, data=""))
//
// 注意飞书与钉钉的 key/data 位置相反：飞书把 stringToSign 当 key、消息体为空。
func feishuSign(secret string, timestampS int64) string {
	stringToSign := strconv.FormatInt(timestampS, 10) + "\n" + secret
	mac := hmac.New(sha256.New, []byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
