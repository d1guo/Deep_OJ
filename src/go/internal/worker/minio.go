package worker

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"golang.org/x/sync/singleflight"
)

type TestCaseManager struct {
	client          *minio.Client
	bucketName      string
	workspace       string
	sf              singleflight.Group
	downloadTimeout time.Duration
	unzipTimeout    time.Duration
	unzipMaxBytes   int64
	unzipMaxFiles   int
	unzipMaxFile    int64
	cacheTTL        time.Duration
	cacheMax        int

	cacheMu   sync.Mutex
	cacheList *list.List
	cacheMap  map[string]*cacheEntry
}

type cacheEntry struct {
	problemID  string
	etag       string
	zipPath    string
	workDir    string
	lastAccess time.Time
	elem       *list.Element
}

func NewTestCaseManager(cfg *Config) (*TestCaseManager, error) {
	client, err := minio.New(cfg.MinIOEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.MinIOAccess, cfg.MinIOSecret, ""),
		Secure: false,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to init minio: %w", err)
	}

	return &TestCaseManager{
		client:          client,
		bucketName:      cfg.MinIOBucket,
		workspace:       cfg.Workspace,
		downloadTimeout: time.Duration(cfg.DownloadTimeoutMs) * time.Millisecond,
		unzipTimeout:    time.Duration(cfg.UnzipTimeoutMs) * time.Millisecond,
		unzipMaxBytes:   cfg.UnzipMaxBytes,
		unzipMaxFiles:   cfg.UnzipMaxFiles,
		unzipMaxFile:    cfg.UnzipMaxFileBytes,
		cacheTTL:        time.Duration(cfg.TestcaseCacheTTL) * time.Second,
		cacheMax:        cfg.TestcaseCacheMax,
		cacheList:       list.New(),
		cacheMap:        make(map[string]*cacheEntry),
	}, nil
}

// Download returns the local path of the downloaded testcase zip
func (m *TestCaseManager) Download(ctx context.Context, problemID string) (string, error) {
	objectName := fmt.Sprintf("problems/%s.zip", problemID)
	localPath := filepath.Join(m.workspace, "cases", problemID+".zip")

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
		return "", fmt.Errorf("failed to create directory: %w", err)
	}

	// Use singleflight to prevent redundant downloads (per-process)
	val, err, _ := m.sf.Do("dl:"+problemID, func() (interface{}, error) {
		dlCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.downloadTimeout)
		defer cancel()

		remoteETag, err := m.getRemoteETag(dlCtx, objectName)
		if err != nil {
			return "", err
		}

		if st, err := os.Stat(localPath); err == nil && st.Size() > 0 {
			if m.etagMatch(localPath, remoteETag) {
				m.touchCache(problemID, remoteETag, localPath, "")
				return localPath, nil
			}
		}

		lockPath := localPath + ".lock"
		lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
		if err != nil {
			return "", fmt.Errorf("failed to open lock file: %w", err)
		}
		defer lockFile.Close()

		if err := lockFileWithContext(dlCtx, lockFile); err != nil {
			return "", err
		}

		// Re-check after lock
		if st, err := os.Stat(localPath); err == nil && st.Size() > 0 {
			if m.etagMatch(localPath, remoteETag) {
				m.touchCache(problemID, remoteETag, localPath, "")
				return localPath, nil
			}
		}

		tmpPath := localPath + ".tmp"
		_ = os.Remove(tmpPath)

		dlStart := time.Now()
		if err := m.client.FGetObject(dlCtx, m.bucketName, objectName, tmpPath, minio.GetObjectOptions{}); err != nil {
			_ = os.Remove(tmpPath)
			return "", err
		}
		workerDownloadDuration.Observe(time.Since(dlStart).Seconds())
		if err := os.Rename(tmpPath, localPath); err != nil {
			_ = os.Remove(tmpPath)
			return "", fmt.Errorf("rename failed: %w", err)
		}
		_ = os.WriteFile(localPath+".etag", []byte(remoteETag), 0644)
		m.touchCache(problemID, remoteETag, localPath, "")
		return localPath, nil
	})

	if err != nil {
		return "", fmt.Errorf("failed to download %s: %w", objectName, err)
	}

	return val.(string), nil
}

// Prepare ensures testcase zip is downloaded and unzipped, returns workdir
func (m *TestCaseManager) Prepare(ctx context.Context, problemID string) (string, error) {
	zipPath, err := m.Download(ctx, problemID)
	if err != nil {
		return "", err
	}

	workDir := filepath.Join(m.workspace, "cases_unzipped", problemID)
	readyPath := filepath.Join(workDir, ".ready")
	if fileExists(readyPath) {
		return workDir, nil
	}

	_, err, _ = m.sf.Do("uz:"+problemID, func() (interface{}, error) {
		if fileExists(readyPath) {
			return workDir, nil
		}

		uzCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.unzipTimeout)
		defer cancel()

		if err := os.MkdirAll(filepath.Dir(workDir), 0755); err != nil {
			return "", fmt.Errorf("failed to create workdir parent: %w", err)
		}

		lockPath := workDir + ".lock"
		lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
		if err != nil {
			return "", fmt.Errorf("failed to open lock file: %w", err)
		}
		defer lockFile.Close()

		if err := lockFileWithContext(uzCtx, lockFile); err != nil {
			return "", err
		}

		if fileExists(readyPath) {
			return workDir, nil
		}

		tmpDir := workDir + ".tmp"
		_ = os.RemoveAll(tmpDir)
		if err := os.MkdirAll(tmpDir, 0755); err != nil {
			return "", fmt.Errorf("failed to create temp dir: %w", err)
		}

		uzStart := time.Now()
		if err := UnzipWithLimits(zipPath, tmpDir, m.unzipMaxBytes, m.unzipMaxFile, m.unzipMaxFiles); err != nil {
			_ = os.RemoveAll(tmpDir)
			return "", err
		}
		workerUnzipDuration.Observe(time.Since(uzStart).Seconds())

		_ = os.RemoveAll(workDir)
		if err := os.Rename(tmpDir, workDir); err != nil {
			_ = os.RemoveAll(tmpDir)
			return "", fmt.Errorf("rename failed: %w", err)
		}
		if err := os.WriteFile(readyPath, []byte(time.Now().Format(time.RFC3339)), 0644); err != nil {
			return "", fmt.Errorf("write ready marker failed: %w", err)
		}
		m.touchCache(problemID, m.readEtag(zipPath), zipPath, workDir)
		return workDir, nil
	})

	if err != nil {
		return "", err
	}
	return workDir, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func lockFileWithContext(ctx context.Context, f *os.File) error {
	for {
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			return nil
		} else if !errors.Is(err, syscall.EWOULDBLOCK) {
			return err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (m *TestCaseManager) getRemoteETag(ctx context.Context, objectName string) (string, error) {
	stat, err := m.client.StatObject(ctx, m.bucketName, objectName, minio.StatObjectOptions{})
	if err != nil {
		return "", err
	}
	return stat.ETag, nil
}

func (m *TestCaseManager) etagMatch(localPath, remoteETag string) bool {
	if remoteETag == "" {
		return false
	}
	b, err := os.ReadFile(localPath + ".etag")
	if err != nil {
		return false
	}
	return string(b) == remoteETag
}

func (m *TestCaseManager) readEtag(zipPath string) string {
	b, err := os.ReadFile(zipPath + ".etag")
	if err != nil {
		return ""
	}
	return string(b)
}

func (m *TestCaseManager) touchCache(problemID, etag, zipPath, workDir string) {
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()

	if e, ok := m.cacheMap[problemID]; ok {
		e.lastAccess = time.Now()
		if etag != "" {
			e.etag = etag
		}
		if zipPath != "" {
			e.zipPath = zipPath
		}
		if workDir != "" {
			e.workDir = workDir
		}
		m.cacheList.MoveToFront(e.elem)
	} else {
		entry := &cacheEntry{
			problemID:  problemID,
			etag:       etag,
			zipPath:    zipPath,
			workDir:    workDir,
			lastAccess: time.Now(),
		}
		entry.elem = m.cacheList.PushFront(entry)
		m.cacheMap[problemID] = entry
	}

	m.evictIfNeeded()
}

func (m *TestCaseManager) evictIfNeeded() {
	// Remove expired
	for id, entry := range m.cacheMap {
		if m.cacheTTL > 0 && time.Since(entry.lastAccess) > m.cacheTTL {
			m.removeEntry(id)
		}
	}
	// Remove LRU
	for m.cacheMax > 0 && m.cacheList.Len() > m.cacheMax {
		back := m.cacheList.Back()
		if back == nil {
			break
		}
		entry := back.Value.(*cacheEntry)
		m.removeEntry(entry.problemID)
	}
}

func (m *TestCaseManager) removeEntry(problemID string) {
	entry, ok := m.cacheMap[problemID]
	if !ok {
		return
	}
	m.cacheList.Remove(entry.elem)
	delete(m.cacheMap, problemID)

	if entry.zipPath != "" {
		_ = os.Remove(entry.zipPath)
		_ = os.Remove(entry.zipPath + ".etag")
		_ = os.Remove(entry.zipPath + ".lock")
	}
	if entry.workDir != "" {
		_ = os.RemoveAll(entry.workDir)
		_ = os.Remove(entry.workDir + ".lock")
		_ = os.Remove(entry.workDir + ".ready")
	}
}
