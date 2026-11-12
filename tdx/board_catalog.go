package tdx

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	tdxzsConfigFile       = "tdxzs.cfg"
	tdxzs3ConfigFile      = "tdxzs3.cfg"
	boardCatalogUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) tdx2db"
)

var boardCatalogURL = "http://www.tdx.com.cn/products/data/data/dbf/gbbq.zip"

type boardCatalogResult struct {
	Warnings  []string
	Checks    int
	Downloads int
}

func ensureOnlineBoardCatalog(ctx context.Context, cacheDir, tempDir string) (boardCatalogResult, error) {
	result := boardCatalogResult{}
	cacheReady := hasBoardCatalog(cacheDir)
	metadata := readBoardCacheMetadata(cacheDir)
	today := time.Now().Format("2006-01-02")
	if cacheReady && metadata.CatalogCheckedAt == today {
		return result, nil
	}
	if tempDir != "" {
		sharedArchive := filepath.Join(tempDir, "gbbq.zip")
		if _, err := os.Stat(sharedArchive); err == nil {
			if err := installBoardCatalog(ctx, cacheDir, sharedArchive, &metadata, today); err == nil {
				return result, writeBoardCacheMetadata(cacheDir, metadata)
			} else if cacheReady {
				result.Warnings = append(result.Warnings,
					fmt.Sprintf("解析本次下载的 gbbq.zip 失败，继续使用缓存: %v", err))
				return result, nil
			}
		}
	}

	headRequest, err := http.NewRequestWithContext(ctx, http.MethodHead, boardCatalogURL, nil)
	if err != nil {
		return result, fmt.Errorf("failed to create board catalog metadata request: %w", err)
	}
	headRequest.Header.Set("User-Agent", boardCatalogUserAgent)
	headResponse, err := (&http.Client{Timeout: 20 * time.Second}).Do(headRequest)
	result.Checks = 1
	if err != nil {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("检查在线行业目录失败，继续使用缓存: %v", err))
		if cacheReady {
			return result, nil
		}
		return result, fmt.Errorf("failed to check online board catalog: %w", err)
	}
	headResponse.Body.Close()
	if headResponse.StatusCode != http.StatusOK {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("检查在线行业目录返回 HTTP %d，继续使用缓存", headResponse.StatusCode))
		if cacheReady {
			return result, nil
		}
		return result, fmt.Errorf("online board catalog returned HTTP %d", headResponse.StatusCode)
	}

	etag := strings.TrimSpace(headResponse.Header.Get("ETag"))
	lastModified := strings.TrimSpace(headResponse.Header.Get("Last-Modified"))
	if cacheReady &&
		((etag != "" && etag == metadata.CatalogETag) ||
			(etag == "" && lastModified != "" && lastModified == metadata.CatalogLastModified)) {
		metadata.CatalogCheckedAt = today
		return result, writeBoardCacheMetadata(cacheDir, metadata)
	}

	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return result, fmt.Errorf("failed to create board cache dir: %w", err)
	}
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	downloadPath, err := os.CreateTemp(tempDir, "tdx-gbbq-*.zip")
	if err != nil {
		return result, fmt.Errorf("failed to create online catalog download: %w", err)
	}
	downloadPath.Close()
	archivePath := downloadPath.Name()
	defer os.Remove(archivePath)

	result.Downloads = 1
	if err := downloadBoardCatalog(ctx, archivePath); err != nil {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("下载在线行业目录失败，继续使用缓存: %v", err))
		if cacheReady {
			return result, nil
		}
		return result, fmt.Errorf("failed to download online board catalog: %w", err)
	}

	if err := installBoardCatalog(ctx, cacheDir, archivePath, &metadata, today); err != nil {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("解析在线行业目录失败，继续使用缓存: %v", err))
		if cacheReady {
			return result, nil
		}
		return result, err
	}
	metadata.CatalogETag = etag
	metadata.CatalogLastModified = lastModified
	return result, writeBoardCacheMetadata(cacheDir, metadata)
}

func hasBoardCatalog(dir string) bool {
	if strings.TrimSpace(dir) == "" {
		return false
	}
	for _, name := range []string{tdxzsConfigFile, tdxzs3ConfigFile, infoHarborBlockFile} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			return false
		}
	}
	return true
}

func downloadBoardCatalog(ctx context.Context, target string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, boardCatalogURL, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", boardCatalogUserAgent)
	response, err := (&http.Client{Timeout: 2 * time.Minute}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP status %d", response.StatusCode)
	}
	file, err := os.Create(target)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := io.Copy(file, response.Body); err != nil {
		return err
	}
	return file.Close()
}

func extractBoardCatalog(archivePath string) (map[string][]byte, error) {
	outer, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open gbbq.zip: %w", err)
	}
	defer outer.Close()

	var zhb []byte
	result := make(map[string][]byte, 3)
	for _, file := range outer.File {
		name := filepath.Base(file.Name)
		if strings.EqualFold(name, "zhb.zip") || strings.EqualFold(name, infoHarborBlockFile) {
			reader, err := file.Open()
			if err != nil {
				return nil, err
			}
			content, err := io.ReadAll(reader)
			reader.Close()
			if err != nil {
				return nil, err
			}
			if strings.EqualFold(name, "zhb.zip") {
				zhb = content
			} else {
				result[infoHarborBlockFile] = content
			}
		}
	}
	if len(zhb) == 0 {
		return nil, fmt.Errorf("gbbq.zip does not contain zhb.zip")
	}

	inner, err := zip.NewReader(bytes.NewReader(zhb), int64(len(zhb)))
	if err != nil {
		return nil, fmt.Errorf("failed to open nested zhb.zip: %w", err)
	}
	for _, file := range inner.File {
		name := filepath.Base(file.Name)
		if name != tdxzsConfigFile && name != tdxzs3ConfigFile {
			continue
		}
		reader, err := file.Open()
		if err != nil {
			return nil, err
		}
		content, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			return nil, err
		}
		result[name] = content
	}
	if len(result) != 3 {
		return nil, fmt.Errorf("gbbq.zip is missing infoharbor_block.dat, tdxzs.cfg or tdxzs3.cfg")
	}
	return result, nil
}

func installBoardCatalog(
	ctx context.Context,
	cacheDir string,
	archivePath string,
	metadata *boardCacheMetadata,
	checkedAt string,
) error {
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return err
	}
	configs, err := extractBoardCatalog(archivePath)
	if err != nil {
		return err
	}
	stagedPaths := make(map[string]string, len(configs))
	for name, content := range configs {
		path := filepath.Join(cacheDir, name+".download")
		if err := os.WriteFile(path, content, 0644); err != nil {
			return fmt.Errorf("failed to stage online %s: %w", name, err)
		}
		defer os.Remove(path)
		stagedPaths[name] = path
	}
	for name, path := range stagedPaths {
		if name == infoHarborBlockFile {
			if _, err := parseInfoHarbor(ctx, path); err != nil {
				return fmt.Errorf("failed to parse online %s: %w", name, err)
			}
			continue
		}
		definitions, found, err := parseBoardCatalogFile(ctx, path, path)
		if err != nil {
			return fmt.Errorf("failed to parse online %s: %w", name, err)
		}
		if !found || len(definitions) == 0 {
			return fmt.Errorf("online %s contains no board definitions", name)
		}
	}
	for name, stagedPath := range stagedPaths {
		if err := commitBoardSnapshot(stagedPath, filepath.Join(cacheDir, name)); err != nil {
			return err
		}
	}
	metadata.CatalogCheckedAt = checkedAt
	return nil
}

func prepareStaticBoardDir(
	tempDir string,
	cacheDir string,
	syncedFiles map[string]boardFileSync,
) (string, *boardFileSync, error) {
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp(tempDir, "tdx-board-static-*")
	if err != nil {
		return "", nil, err
	}

	copyFrom := func(name, source string) (bool, error) {
		if source == "" {
			return false, nil
		}
		return copyBoardConfig(source, filepath.Join(dir, name))
	}
	pickFile := func(name string) string {
		path := filepath.Join(cacheDir, name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
		return ""
	}

	for _, name := range []string{tdxzsConfigFile, tdxzs3ConfigFile} {
		if _, err := copyFrom(name, pickFile(name)); err != nil {
			return "", nil, err
		}
	}

	var tdxhySnapshot *boardFileSync
	if file, ok := syncedFiles[industryMappingFile]; ok {
		snapshot := file
		tdxhySnapshot = &snapshot
		source := file.CachePath
		if file.Status == "downloaded" {
			source = file.StagingPath
		}
		if _, err := copyFrom(industryMappingFile, source); err != nil {
			return "", nil, err
		}
	} else if _, err := copyFrom(industryMappingFile, pickFile(industryMappingFile)); err != nil {
		return "", nil, err
	}

	return dir, tdxhySnapshot, nil
}

func copyBoardConfig(source, target string) (bool, error) {
	if source == "" {
		return false, nil
	}
	input, err := os.Open(source)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	defer input.Close()
	output, err := os.Create(target)
	if err != nil {
		return false, err
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return false, err
	}
	if err := output.Close(); err != nil {
		return false, err
	}
	return true, nil
}
