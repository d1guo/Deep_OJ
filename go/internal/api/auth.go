package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/d1guo/deep_oj/internal/model"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
)

var (
    jwtSecret []byte
)

func init() {
    secret := os.Getenv("JWT_SECRET")
    if secret == "" {
        fmt.Println("⚠️ Warning: JWT_SECRET not set, using default insecure secret")
        secret = "deep-oj-insecure-secret-key"
    }
    jwtSecret = []byte(secret)
}

// Register 处理用户注册
func (h *Handler) Register(c *gin.Context) {
	var req model.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数错误: " + err.Error()})
		return
	}

	// 1. 检查用户名是否存在
	existingUser, err := h.db.GetUserByUsername(c.Request.Context(), req.Username)
	if err != nil {
		slog.Error("数据库查询失败", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "数据库错误"})
		return
	}
	if existingUser != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "用户名已存在"})
		return
	}

	// 2. 密码哈希
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		slog.Error("密码哈希失败", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "密码处理失败"})
		return
	}

	// 3. 创建用户
	user := &model.User{
		Username:     req.Username,
		PasswordHash: string(hashedPassword),
		Email:        req.Email,
	}
	if err := h.db.CreateUser(c.Request.Context(), user); err != nil {
		slog.Error("创建用户失败", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建用户失败"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"id": user.ID, "username": user.Username})
}

// Login 处理用户登录
func (h *Handler) Login(c *gin.Context) {
	var req model.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数错误: " + err.Error()})
		return
	}

	// 1. 查找用户
	user, err := h.db.GetUserByUsername(c.Request.Context(), req.Username)
	if err != nil {
		slog.Error("登录查询失败", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "数据库错误"})
		return
	}
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户名或密码错误"})
		return
	}

	// 2. 验证密码
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户名或密码错误"})
		return
	}

	// 3. 生成 JWT
	token, err := generateToken(user.ID, user.Username)
	if err != nil {
		slog.Error("Token 生成失败", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Token 生成失败"})
		return
	}

	c.JSON(http.StatusOK, model.LoginResponse{
		Token:    token,
		Username: user.Username,
		UserID:   user.ID,
	})
}

func generateToken(userID int64, username string) (string, error) {
	claims := jwt.MapClaims{
		"user_id":  userID,
		"username": username,
		"exp":      time.Now().Add(24 * time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(jwtSecret)
}

// =========================================================================
// Task 3.2: GitHub OAuth 2.0
// =========================================================================

var githubConfig = &oauth2.Config{
	ClientID:     os.Getenv("GITHUB_CLIENT_ID"),
	ClientSecret: os.Getenv("GITHUB_CLIENT_SECRET"),
	RedirectURL:  "http://localhost:8080/api/v1/auth/github/callback",
	Scopes:       []string{"read:user", "user:email"},
	Endpoint:     github.Endpoint,
}

// HandleGitHubLogin 跳转到 GitHub 登录页
func (h *Handler) HandleGitHubLogin(c *gin.Context) {
	// State 应该是一个随机字符串，用于防止 CSRF，这里简化处理
	url := githubConfig.AuthCodeURL("random-state", oauth2.AccessTypeOnline)
	c.Redirect(http.StatusTemporaryRedirect, url)
}

// HandleGitHubCallback GitHub 回调处理
func (h *Handler) HandleGitHubCallback(c *gin.Context) {
	code := c.Query("code")
	if code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 Code 参数"})
		return
	}

	// 1. 换取 Token
	token, err := githubConfig.Exchange(c.Request.Context(), code)
	if err != nil {
		slog.Error("GitHub Token 交换失败", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "GitHub 认证失败"})
		return
	}

	// 2. 获取用户信息
	client := githubConfig.Client(c.Request.Context(), token)
	resp, err := client.Get("https://api.github.com/user")
	if err != nil {
		slog.Error("GitHub 用户信息获取失败", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取用户信息失败"})
		return
	}
	defer resp.Body.Close()

	var ghUser struct {
		ID        int    `json:"id"`
		Login     string `json:"login"`
		Email     string `json:"email"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ghUser); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse user info"})
		return
	}

	// 3. 数据库操作 (Get or Create)
	user, err := h.db.GetOrCreateByGitHubID(
		c.Request.Context(), 
		fmt.Sprintf("%d", ghUser.ID), 
		ghUser.Login, 
		ghUser.Email,
		ghUser.AvatarURL,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error: " + err.Error()})
		return
	}

	// 4. 生成 JWT
	jwtToken, err := generateToken(user.ID, user.Username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate token"})
		return
	}

	c.JSON(http.StatusOK, model.LoginResponse{
		Token:    jwtToken,
		Username: user.Username,
		UserID:   user.ID,
	})
}
