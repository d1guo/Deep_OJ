/**
 * @file middleware.go
 * @brief Gin 中间件
 */
package api

import (
	"fmt"
	"net"
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
	allowedOrigins := loadAllowedOrigins()
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		allowOrigin := ""
		if origin != "" {
			if _, ok := allowedOrigins[origin]; ok {
				allowOrigin = origin
			}
		}

		if allowOrigin != "" {
			c.Header("Access-Control-Allow-Origin", allowOrigin)
			c.Header("Vary", "Origin")
		}
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Authorization, X-Request-ID")
		c.Header("Access-Control-Max-Age", "86400") // 预检结果缓存 24 小时

		// 处理预检请求
		if c.Request.Method == "OPTIONS" {
			if origin != "" && allowOrigin == "" {
				c.AbortWithStatus(http.StatusForbidden)
				return
			}
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

// GetRequestID 返回中间件写入的标准化 request_id。
func GetRequestID(c *gin.Context) string {
	if ridAny, ok := c.Get("request_id"); ok {
		if rid, ok := ridAny.(string); ok && rid != "" {
			return rid
		}
	}
	return c.GetHeader("X-Request-ID")
}

// AuthMiddleware 验证 JWT
func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "缺少 Authorization 请求头", "code": "UNAUTHORIZED"})
			c.Abort()
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Authorization 格式无效", "code": "UNAUTHORIZED"})
			c.Abort()
			return
		}

		tokenString := parts[1]

		token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("签名算法不符合预期: %v", token.Header["alg"])
			}
			return getJWTSecret(), nil
		})

		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Token 无效", "code": "UNAUTHORIZED"})
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
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Token 声明无效", "code": "UNAUTHORIZED"})
			c.Abort()
		}
	}
}

// AdminMiddleware 基于 username 声明限制管理员访问。
func AdminMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		usernameAny, ok := c.Get("username")
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录", "code": "UNAUTHORIZED"})
			c.Abort()
			return
		}
		username, _ := usernameAny.(string)
		if !IsAdminUsername(username) {
			c.JSON(http.StatusForbidden, gin.H{"error": "无权限", "code": "FORBIDDEN"})
			c.Abort()
			return
		}
		c.Next()
	}
}

func IsAdminUsername(username string) bool {
	if username == "" {
		return false
	}
	admins := loadAdminUsers()
	_, ok := admins[username]
	return ok
}

func loadAdminUsers() map[string]struct{} {
	admins := make(map[string]struct{})
	raw := strings.TrimSpace(os.Getenv("ADMIN_USERS"))
	if raw == "" {
		return admins
	}
	for _, v := range strings.Split(raw, ",") {
		u := strings.TrimSpace(v)
		if u == "" {
			continue
		}
		admins[u] = struct{}{}
	}
	return admins
}

// MetricsAccessMiddleware 保护 /metrics，禁止匿名远程访问。
func MetricsAccessMiddleware() gin.HandlerFunc {
	token := strings.TrimSpace(os.Getenv("METRICS_TOKEN"))
	return func(c *gin.Context) {
		if token != "" {
			if extractBearerToken(c.GetHeader("Authorization")) != token {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "未授权", "code": "UNAUTHORIZED"})
				c.Abort()
				return
			}
			c.Next()
			return
		}

		// 安全默认值：未配置 token 时仅允许本机抓取。
		if !isLoopbackClientIP(c.ClientIP()) {
			c.JSON(http.StatusForbidden, gin.H{"error": "禁止访问", "code": "FORBIDDEN"})
			c.Abort()
			return
		}
		c.Next()
	}
}

func loadAllowedOrigins() map[string]struct{} {
	allowed := make(map[string]struct{})
	for _, origin := range strings.Split(os.Getenv("CORS_ALLOWED_ORIGINS"), ",") {
		origin = strings.TrimSpace(origin)
		if origin == "" {
			continue
		}
		allowed[origin] = struct{}{}
	}
	return allowed
}

func extractBearerToken(header string) string {
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || parts[0] != "Bearer" {
		return ""
	}
	return parts[1]
}

func isLoopbackClientIP(clientIP string) bool {
	ip := net.ParseIP(clientIP)
	return ip != nil && ip.IsLoopback()
}

// Prometheus 指标

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
			path = "未知"
		}

		// 使用 metrics.go 中定义的 RequestTotal 和 RequestDuration
		RequestTotal.WithLabelValues(method, path, status).Inc()
		RequestDuration.WithLabelValues(method, path, status).Observe(duration)
		requestDurationSeconds.WithLabelValues(path, statusClassFromCode(c.Writer.Status())).Observe(duration)
	}
}
