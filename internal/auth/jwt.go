package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/maliang/leadstack/internal/store"
)

// Issuer 是签发者标识，校验时会一并核对。
const Issuer = "leadstack"

// jwtSecretKey 是 JWT 密钥在 settings 表中的键名。
const jwtSecretKey = "jwt_secret"

// ErrInvalidToken 表示令牌无效（签名错误、过期、结构不合法等）。
var ErrInvalidToken = errors.New("令牌无效")

// Claims 是登录令牌的载荷。
type Claims struct {
	Username string `json:"username"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

// Manager 负责 JWT 的签发与校验。
type Manager struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

// NewManager 创建 JWT 管理器；密钥至少 32 字节，避免 HS256 弱密钥。
func NewManager(secret []byte, ttl time.Duration) (*Manager, error) {
	if len(secret) < 32 {
		return nil, fmt.Errorf("JWT 密钥长度 %d 不足 32 字节", len(secret))
	}
	if ttl <= 0 {
		return nil, errors.New("JWT 有效期必须为正数")
	}
	return &Manager{secret: secret, ttl: ttl, now: time.Now}, nil
}

// TTL 返回令牌有效期。
func (m *Manager) TTL() time.Duration { return m.ttl }

// Sign 为指定用户签发 HS256 令牌，返回令牌与过期时间。
func (m *Manager) Sign(userID, username, role string) (string, time.Time, error) {
	if userID == "" {
		return "", time.Time{}, errors.New("签发令牌: 用户 ID 为空")
	}
	now := m.now()
	expiresAt := now.Add(m.ttl)
	claims := Claims{
		Username: username,
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    Issuer,
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now.Add(-30 * time.Second)), // 容忍轻微时钟漂移
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("签发令牌: %w", err)
	}
	return signed, expiresAt, nil
}

// Parse 校验并解析令牌。
func (m *Manager) Parse(tokenString string) (*Claims, error) {
	if tokenString == "" {
		return nil, fmt.Errorf("解析令牌: %w", ErrInvalidToken)
	}
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("非预期的签名算法 %v", t.Header["alg"])
		}
		return m.secret, nil
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(Issuer),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return nil, fmt.Errorf("解析令牌: %w: %w", ErrInvalidToken, err)
	}
	if !token.Valid {
		return nil, fmt.Errorf("解析令牌: %w", ErrInvalidToken)
	}
	return claims, nil
}

// LoadOrCreateSecret 从 settings 表读取 JWT 密钥；不存在则随机生成并持久化。
func LoadOrCreateSecret(ctx context.Context, st *store.Store) ([]byte, error) {
	if st == nil {
		return nil, errors.New("加载 JWT 密钥: store 为空")
	}
	value, err := st.EnsureSetting(ctx, jwtSecretKey, func() (string, error) {
		buf := make([]byte, 48)
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("读取随机数: %w", err)
		}
		return base64.RawURLEncoding.EncodeToString(buf), nil
	})
	if err != nil {
		return nil, fmt.Errorf("加载 JWT 密钥: %w", err)
	}
	return []byte(value), nil
}
