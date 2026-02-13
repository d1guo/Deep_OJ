/**
 * @file middleware.go
 * @brief Gin 中间件
 */
package api

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// CORSMiddleware 跨域资源共享中间件
func CORSMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Authorization, X-Request-ID")
		c.Header("Access-Control-Max-Age", "86400") // 预检结果缓存 24 小时

		// 处理预检请求
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}

// RequestIDMiddleware 请求 ID 中间件
func RequestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 尝试从 Header 获取 (上游服务传递)
		requestID := c.GetHeader("X-Request-ID")
		if requestID == "" {
			// 生成新的 UUID
			requestID = uuid.New().String()
		}

		// 设置到 Context (便于后续处理使用)
		c.Set("request_id", requestID)

		// 设置响应 Header (便于客户端追踪)
		c.Header("X-Request-ID", requestID)

		c.Next()
	}
}

// AuthMiddleware 验证 JWT
func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Authorization header required", "code": "UNAUTHORIZED"})
			c.Abort()
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid authorization format", "code": "UNAUTHORIZED"})
			c.Abort()
			return
		}

		tokenString := parts[1]

		token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}
			return getJWTSecret(), nil
		})

		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid token: " + err.Error(), "code": "UNAUTHORIZED"})
			c.Abort()
			return
		}

		if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
			if userID, ok := claims["user_id"].(float64); ok {
				c.Set("user_id", int64(userID))
			}
			if username, ok := claims["username"].(string); ok {
				c.Set("username", username)
			}
			c.Next()
		} else {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid token claims", "code": "UNAUTHORIZED"})
			c.Abort()
		}
	}
}

// AdminMiddleware enforces admin-only access based on username claim.
func AdminMiddleware() gin.HandlerFunc {
	admins := loadAdminUsers()
	return func(c *gin.Context) {
		usernameAny, ok := c.Get("username")
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录", "code": "UNAUTHORIZED"})
			c.Abort()
			return
		}
		username, _ := usernameAny.(string)
		if _, ok := admins[username]; !ok {
			c.JSON(http.StatusForbidden, gin.H{"error": "无权限", "code": "FORBIDDEN"})
			c.Abort()
			return
		}
		c.Next()
	}
}

func loadAdminUsers() map[string]struct{} {
	admins := make(map[string]struct{})
	raw := os.Getenv("ADMIN_USERS")
	if raw == "" {
		raw = "admin"
	}
	for _, v := range strings.Split(raw, ",") {
		u := strings.TrimSpace(v)
		if u == "" {
			continue
		}
		admins[u] = struct{}{}
	}
	if len(admins) == 0 {
		admins["admin"] = struct{}{}
	}
	return admins
}

// =========================================================================
// Task 3.3: Prometheus Metrics
// =========================================================================

// MetricsMiddleware 记录请求指标 (使用 metrics.go 中定义的变量)
func MetricsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.FullPath()

		c.Next()

		duration := time.Since(start).Seconds()
		status := fmt.Sprintf("%d", c.Writer.Status())
		method := c.Request.Method

		// 如果 path 为空 (比如 404)，使用 raw path
		if path == "" {
			path = "unknown"
		}

		// 使用 metrics.go 中定义的 RequestTotal 和 RequestDuration
		RequestTotal.WithLabelValues(method, path, status).Inc()
		RequestDuration.WithLabelValues(method, path, status).Observe(duration)
	}
}

// (End of file)
