package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/d1guo/deep_oj/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
)

var (
	jwtSecret []byte
	jwtMu     sync.Mutex
)

func SetJWTSecret(secret string) {
	if secret == "" {
		return
	}
	jwtMu.Lock()
	defer jwtMu.Unlock()
	jwtSecret = []byte(secret)
}

func getJWTSecret() []byte {
	jwtMu.Lock()
	defer jwtMu.Unlock()
	if len(jwtSecret) == 0 {
		secret := os.Getenv("JWT_SECRET")
		if secret == "" {
			slog.Error("JWT_SECRET must be set")
			os.Exit(1)
		}
		jwtSecret = []byte(secret)
	}
	return jwtSecret
}

func getJWTExpireDuration() time.Duration {
	if sec := getEnvInt("JWT_EXPIRE_SEC", 0); sec > 0 {
		return time.Duration(sec) * time.Second
	}
	hours := getEnvInt("JWT_EXPIRE_HOURS", 24)
	return time.Duration(hours) * time.Hour
}

func getOAuthStateTTL() time.Duration {
	sec := getEnvInt("OAUTH_STATE_TTL_SEC", 300)
	return time.Duration(sec) * time.Second
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
		"exp":      time.Now().Add(getJWTExpireDuration()).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(getJWTSecret())
}

// =========================================================================
// Task 3.2: GitHub OAuth 2.0
// =========================================================================

var (
	githubConfig   *oauth2.Config
	githubConfigMu sync.Mutex
)

func SetGitHubOAuth(clientID, clientSecret, redirectURL string) {
	githubConfigMu.Lock()
	defer githubConfigMu.Unlock()
	if clientID == "" && clientSecret == "" && redirectURL == "" {
		return
	}
	if redirectURL == "" {
		redirectURL = "http://localhost:8080/api/v1/auth/github/callback"
	}
	githubConfig = &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		Scopes:       []string{"read:user", "user:email"},
		Endpoint:     github.Endpoint,
	}
}

func getGitHubConfig() *oauth2.Config {
	githubConfigMu.Lock()
	defer githubConfigMu.Unlock()
	if githubConfig != nil {
		return githubConfig
	}
	redirectURL := os.Getenv("GITHUB_REDIRECT_URL")
	if redirectURL == "" {
		redirectURL = "http://localhost:8080/api/v1/auth/github/callback"
	}
	githubConfig = &oauth2.Config{
		ClientID:     os.Getenv("GITHUB_CLIENT_ID"),
		ClientSecret: os.Getenv("GITHUB_CLIENT_SECRET"),
		RedirectURL:  redirectURL,
		Scopes:       []string{"read:user", "user:email"},
		Endpoint:     github.Endpoint,
	}
	return githubConfig
}

const (
	oauthStateCookieName = "oauth_state"
)

func generateOAuthState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// HandleGitHubLogin 跳转到 GitHub 登录页
func (h *Handler) HandleGitHubLogin(c *gin.Context) {
	state, err := generateOAuthState()
	if err != nil {
		slog.Error("生成 OAuth state 失败", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "OAuth 初始化失败"})
		return
	}
	secure := c.Request.TLS != nil
	c.SetSameSite(http.SameSiteLaxMode)
	stateTTL := getOAuthStateTTL()
	c.SetCookie(oauthStateCookieName, state, int(stateTTL.Seconds()), "/", "", secure, true)

	url := getGitHubConfig().AuthCodeURL(state, oauth2.AccessTypeOnline)
	c.Redirect(http.StatusTemporaryRedirect, url)
}

// HandleGitHubCallback GitHub 回调处理
func (h *Handler) HandleGitHubCallback(c *gin.Context) {
	code := c.Query("code")
	state := c.Query("state")
	if code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 Code 参数"})
		return
	}
	if state == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 state 参数"})
		return
	}

	cookieState, err := c.Cookie(oauthStateCookieName)
	secure := c.Request.TLS != nil
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(oauthStateCookieName, "", -1, "/", "", secure, true)
	if err != nil || cookieState == "" || cookieState != state {
		slog.Warn("OAuth state mismatch", "error", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "state 校验失败"})
		return
	}

	// 1. 换取 Token
	token, err := getGitHubConfig().Exchange(c.Request.Context(), code)
	if err != nil {
		slog.Error("GitHub Token 交换失败", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "GitHub 认证失败"})
		return
	}

	// 2. 获取用户信息
	client := getGitHubConfig().Client(c.Request.Context(), token)
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
