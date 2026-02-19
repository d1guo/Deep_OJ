package api

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/d1guo/deep_oj/internal/repository"
	"github.com/gin-gonic/gin"
)

const defaultMaxProblemZipBytes int64 = 50 << 20 // 50MB

// HandleCreateProblem 创建题目 (上传 Zip)
// POST /api/v1/problems
func (h *Handler) HandleCreateProblem(c *gin.Context) {
	maxBytes := getEnvInt64("PROBLEM_ZIP_MAX_BYTES", defaultMaxProblemZipBytes)
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes+1)
	if err := c.Request.ParseMultipartForm(maxBytes); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "请求体过大", "code": "BODY_TOO_LARGE"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "表单解析失败"})
		return
	}

	// 1. 解析表单
	title := c.PostForm("title")
	timeLimit, _ := strconv.Atoi(c.PostForm("time_limit"))     // ms
	memoryLimit, _ := strconv.Atoi(c.PostForm("memory_limit")) // MB
	difficulty := c.PostForm("difficulty")

	if title == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Title required"})
		return
	}
	defaultTimeLimit := getEnvInt("PROBLEM_DEFAULT_TIME_LIMIT_MS", 1000)
	defaultMemoryLimit := getEnvInt("PROBLEM_DEFAULT_MEMORY_LIMIT_MB", 128)
	if timeLimit <= 0 {
		timeLimit = defaultTimeLimit
	}
	if memoryLimit <= 0 {
		memoryLimit = defaultMemoryLimit
	}

	// 2. 获取文件
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "File required"})
		return
	}
	defer file.Close()

	if filepath.Ext(header.Filename) != ".zip" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Only .zip allowed"})
		return
	}

	// 3. 计算 SHA-256 & 验证 Zip 内容 (stream -> temp file)
	tmpFile, err := os.CreateTemp("", "problem-*.zip")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "临时文件创建失败"})
		return
	}
	defer func() {
		tmpFile.Close()
		_ = os.Remove(tmpFile.Name())
	}()

	hasher := sha256.New()
	limited := io.LimitReader(file, maxBytes+1)
	written, err := io.Copy(io.MultiWriter(tmpFile, hasher), limited)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Read file error"})
		return
	}
	if written > maxBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "文件过大", "code": "FILE_TOO_LARGE"})
		return
	}
	hashStr := hex.EncodeToString(hasher.Sum(nil))

	// 验证 Zip
	if !validateZipContent(tmpFile, written) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid Zip: must contain .in and .out files"})
		return
	}

	// 4. 插入 DB (获取 ID)
	problem := &repository.Problem{
		Title:             title,
		Difficulty:        difficulty,
		TimeLimitMS:       timeLimit,
		MemoryLimitMB:     memoryLimit,
		TestcaseHash:      hashStr,
		TestcaseMinioPath: "", // 稍后更新
	}

	id, err := h.db.CreateProblem(c.Request.Context(), problem)
	if err != nil {
		slog.Error("创建题目时数据库异常", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
		return
	}

	// 5. 上传 MinIO
	objectName := fmt.Sprintf("problems/%d.zip", id)
	if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取文件失败"})
		return
	}
	err = h.minio.UploadFile(c.Request.Context(), objectName, tmpFile, written)
	if err != nil {
		slog.Error("创建题目时 MinIO 异常", "error", err)
		// 回滚 DB
		h.db.DeleteProblem(c.Request.Context(), id)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Storage error"})
		return
	}

	// 6. 更新 DB 路径
	if err := h.db.UpdateTestcasePath(c.Request.Context(), id, objectName); err != nil {
		slog.Error("更新题目路径异常", "error", err)
		_ = h.minio.RemoveFile(c.Request.Context(), objectName)
		_ = h.db.DeleteProblem(c.Request.Context(), id)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database update error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"id":         id,
		"status":     "created",
		"minio_path": objectName,
	})
}

// HandleDeleteProblem 删除题目
// DELETE /api/v1/problems/:id
func (h *Handler) HandleDeleteProblem(c *gin.Context) {
	idStr := c.Param("id")
	id, _ := strconv.Atoi(idStr)

	// 先查
	// [Optimization] We don't strictly need the problem details to delete it,
	// unless we want to delete related MinIO files which we are skipping for now.
	// So we can just call DeleteProblem directly.
	// But usually checking existence gives better 404.
	_, err := h.db.GetProblem(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Problem not found"})
		return
	}

	// 删 DB
	if err := h.db.DeleteProblem(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Delete DB error"})
		return
	}

	// 删 MinIO (可选，也可以保留作为历史归档)
	// 如果是基于 Hash 存储 (problems/hash.zip)，多个题目可能共用同一个包，不能随便删！
	// 所以这里我们只删 DB，或者检查引用计数 (太复杂)。
	// 为了演示，我们暂不删除 MinIO 文件，或者仅当 path 是 problems/id.zip 时删除。
	// 如果 path 是 problems/hash.zip，最好留着。

	// 这里假设使用 problems/hash.zip，不删除文件。

	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

func validateZipContent(readerAt io.ReaderAt, size int64) bool {
	r, err := zip.NewReader(readerAt, size)
	if err != nil {
		return false
	}

	inFiles := make(map[string]bool)
	outFiles := make(map[string]bool)

	// 1. Collect all files
	for _, f := range r.File {
		// Ignore directories and macOS specific files
		if f.FileInfo().IsDir() || strings.HasPrefix(f.Name, "__MACOSX") || strings.HasPrefix(filepath.Base(f.Name), ".") {
			continue
		}

		name := f.Name
		if strings.HasSuffix(name, ".in") {
			inFiles[strings.TrimSuffix(name, ".in")] = true
		} else if strings.HasSuffix(name, ".out") {
			outFiles[strings.TrimSuffix(name, ".out")] = true
		} else if strings.HasSuffix(name, ".ans") {
			outFiles[strings.TrimSuffix(name, ".ans")] = true
		}
	}

	// 2. Validation
	if len(inFiles) == 0 {
		return false
	}

	// Every .in must have a corresponding .out/.ans
	for prefix := range inFiles {
		if !outFiles[prefix] {
			return false
		}
	}

	return true
}
