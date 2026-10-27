// Package auth 提供控制端的认证能力：密码哈希、JWT 签发/校验与 chi 中间件。
package auth

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// bcryptCost 与 x/crypto/bcrypt 的取值区间一致；12 是安全性/耗时的常用折中。
const bcryptCost = 12

// EnvBcryptCost 可覆盖新密码的 bcrypt 成本（4–31），仅供测试与极低算力环境使用。
//
// 默认 12：单次校验约 200–300ms，是安全性与登录耗时的折中。
// 测试里设为 4 可把整个 api 包的 -race 运行从数分钟压到秒级——否则 100 多次登录
// 在竞态检测器下会拖到超时，让 go test -race 闸门变成"随机失败"。
const EnvBcryptCost = "LEADSTACK_BCRYPT_COST"

// activeBcryptCost 返回本次哈希使用的成本：环境变量合法则用之，否则回落到默认值。
func activeBcryptCost() int {
	raw := strings.TrimSpace(os.Getenv(EnvBcryptCost))
	if raw == "" {
		return bcryptCost
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < bcrypt.MinCost || n > bcrypt.MaxCost {
		return bcryptCost
	}
	return n
}

// MaxPasswordBytes 是 bcrypt 能处理的密码上限，超出会直接报错。
const MaxPasswordBytes = 72

// passwordAlphabet 去掉了易混淆字符（0/O、1/l/I），便于用户手抄初始密码。
const passwordAlphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// timingEqualizerHash 是一个固定的 cost=12 bcrypt 哈希。
//
// 用户不存在时也执行一次同开销的校验，避免攻击者通过登录响应时间枚举用户名。
const timingEqualizerHash = "$2a$12$53ge7d2n4iN98765VKtRkenyvptw.pEoZsaCrJBy4WRtJnScY7Caq"

// HashPassword 用 bcrypt 生成密码哈希。
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", errors.New("密码哈希: 密码为空")
	}
	if len(password) > MaxPasswordBytes {
		return "", fmt.Errorf("密码哈希: 密码长度 %d 超过上限 %d 字节", len(password), MaxPasswordBytes)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), activeBcryptCost())
	if err != nil {
		return "", fmt.Errorf("生成密码哈希: %w", err)
	}
	return string(hash), nil
}

// VerifyPassword 校验明文密码与哈希是否匹配。任何异常一律返回 false。
func VerifyPassword(hash, password string) bool {
	if hash == "" || password == "" || len(password) > MaxPasswordBytes {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// WastePasswordTime 在用户不存在的分支上执行一次等价开销的哈希校验，
// 使登录失败耗时与“密码错误”分支一致，避免通过时间差枚举用户名。
func WastePasswordTime(password string) {
	if password == "" || len(password) > MaxPasswordBytes {
		return
	}
	_ = bcrypt.CompareHashAndPassword([]byte(timingEqualizerHash), []byte(password))
}

// GeneratePassword 生成 length 个字符的随机密码，用于首次启动的初始管理员。
func GeneratePassword(length int) (string, error) {
	if length <= 0 {
		length = 20
	}
	var sb strings.Builder
	sb.Grow(length)
	max := big.NewInt(int64(len(passwordAlphabet)))
	for i := 0; i < length; i++ {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("生成随机密码: %w", err)
		}
		sb.WriteByte(passwordAlphabet[n.Int64()])
	}
	return sb.String(), nil
}
