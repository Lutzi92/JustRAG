package auth

import (
	"fmt"

	"github.com/golang-jwt/jwt/v5"
)

type Claims struct {
	ID        string
	Username  string
	Role      string
	JTI       string
	IssuedAt  int64
	ExpiresAt int64
}

func ParseToken(tokenStr, secret string) (*Claims, error) {
	token, err := jwt.Parse(tokenStr, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, fmt.Errorf("invalid token: %w", err)
	}

	mapClaims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}

	return extractClaims(mapClaims)
}

func DecodeTokenUnverified(tokenStr string) (*Claims, error) {
	parser := jwt.NewParser(jwt.WithoutClaimsValidation())
	token, _, err := parser.ParseUnverified(tokenStr, jwt.MapClaims{})
	if err != nil {
		return nil, fmt.Errorf("failed to decode token: %w", err)
	}

	mapClaims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("invalid token claims")
	}

	return extractClaims(mapClaims)
}

func extractClaims(m jwt.MapClaims) (*Claims, error) {
	c := &Claims{}

	if v, ok := m["id"].(string); ok {
		c.ID = v
	}
	if v, ok := m["username"].(string); ok {
		c.Username = v
	}
	if v, ok := m["role"].(string); ok {
		c.Role = v
	}
	if v, ok := m["jti"].(string); ok {
		c.JTI = v
	}
	if v, ok := m["iat"].(float64); ok {
		c.IssuedAt = int64(v)
	}
	if v, ok := m["exp"].(float64); ok {
		c.ExpiresAt = int64(v)
	}

	// Required fields. Without id the request has no subject to authorize;
	// without role RequireRole always fails closed (acceptable, but the token
	// is unusable); without jti the blacklist lookup keys on an empty string
	// and silently bypasses revocation — the actual security gap.
	if c.ID == "" {
		return nil, fmt.Errorf("invalid token claims: missing id")
	}
	if c.Role == "" {
		return nil, fmt.Errorf("invalid token claims: missing role")
	}
	if c.JTI == "" {
		return nil, fmt.Errorf("invalid token claims: missing jti")
	}

	return c, nil
}
