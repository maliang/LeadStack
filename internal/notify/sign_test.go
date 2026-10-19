package notify

import (
	"net/url"
	"strings"
	"testing"
)

// 下面两组「已知答案」由 Python 的 hmac/hashlib/base64/urllib.parse 独立计算得出
// （不是用 Go 自身实现反过来自证）：
//
//	DingTalk: base64(HMAC-SHA256(key=secret, data=f"{ts_ms}\n{secret}"))
//	          sign = quote(b64, safe="")
//	Feishu:   base64(HMAC-SHA256(key=f"{ts_s}\n{secret}", data=b""))
func TestDingTalkSignKnownAnswer(t *testing.T) {
	cases := []struct {
		name    string
		secret  string
		tsMS    int64
		wantB64 string
		wantURL string
	}{
		{
			name:    "样例一",
			secret:  "SECtestsecret0123456789abcdef",
			tsMS:    1700000000000,
			wantB64: "oWnZboeOYjlS1NUgJCZh8t1VJ8C+IwsRPuEItFP4A0s=",
			wantURL: "oWnZboeOYjlS1NUgJCZh8t1VJ8C%2BIwsRPuEItFP4A0s%3D",
		},
		{
			name:    "样例二",
			secret:  "ding_secret_A1b2C3d4E5f6",
			tsMS:    1609459200000,
			wantB64: "EG5BU25l28cM2XUU6ay/P/x5UYvZK8WWDvdCEEb1w4s=",
			wantURL: "EG5BU25l28cM2XUU6ay%2FP%2Fx5UYvZK8WWDvdCEEb1w4s%3D",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dingTalkSignRaw(tc.secret, tc.tsMS); got != tc.wantB64 {
				t.Fatalf("dingTalkSignRaw = %q, want %q", got, tc.wantB64)
			}
			got := dingTalkSign(tc.secret, tc.tsMS)
			if got != tc.wantURL {
				t.Fatalf("dingTalkSign = %q, want %q", got, tc.wantURL)
			}
			// 必须真的 urlencode 过。
			if strings.ContainsAny(got, "+/=") {
				t.Fatalf("sign 未 urlencode: %q", got)
			}
			// 解码一次必须回到原始 base64（双重编码会在这里暴露）。
			dec, err := url.QueryUnescape(got)
			if err != nil {
				t.Fatalf("QueryUnescape: %v", err)
			}
			if dec != tc.wantB64 {
				t.Fatalf("解码后 = %q, want %q", dec, tc.wantB64)
			}
		})
	}
}

func TestFeishuSignKnownAnswer(t *testing.T) {
	cases := []struct {
		name   string
		secret string
		tsS    int64
		want   string
	}{
		{
			name:   "样例一",
			secret: "FeishuSecretTest0123456789abcd",
			tsS:    1700000000,
			want:   "uXURCgzFKclAtkaTVdpqFT3aFyTx/H5cko44KZPFLew=",
		},
		{
			name:   "样例二",
			secret: "feishu_secret_XyZ987",
			tsS:    1609459200,
			want:   "YKLftzos13INpbFs/FqSxwSsjpDlS1NSrGhKz4eqsfM=",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := feishuSign(tc.secret, tc.tsS); got != tc.want {
				t.Fatalf("feishuSign = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSignAlgorithmsAreNotSwapped 防「把两家算法搞混」——这是加签失败的另一大来源。
func TestSignAlgorithmsAreNotSwapped(t *testing.T) {
	const secret = "same-secret-for-both"
	const tsMS = 1700000000000
	const tsS = 1700000000

	ding := dingTalkSignRaw(secret, tsMS)
	fei := feishuSign(secret, tsS)

	// 钉钉口径 vs 飞书口径（同秒）必须不同。
	if dingTalkSignRaw(secret, tsS) == feishuSign(secret, tsS) {
		t.Fatal("钉钉与飞书的签名结果相同，算法可能被搞混")
	}
	if ding == "" || fei == "" {
		t.Fatal("签名结果不应为空")
	}
	// 飞书签名不做 urlencode（保留 + / =），与钉钉不同。
	if !strings.ContainsAny(fei, "+/=") {
		t.Fatalf("飞书签名样例应包含 base64 字符，实际 %q", fei)
	}
}
