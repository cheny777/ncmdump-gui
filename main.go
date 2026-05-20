package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/sqweek/dialog"
)

type pageData struct {
	DefaultDir string
}

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}

	if err := ensureNcmdumpInCurrentDir(cwd); err != nil {
		fmt.Printf("警告：%v\n", err)
	}

	webDir := filepath.Join(cwd, "web")
	tmpl := template.Must(template.ParseFiles(filepath.Join(webDir, "index.html")))

	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir(webDir))))
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		tmpl.Execute(w, pageData{DefaultDir: cwd})
	})

	http.HandleFunc("/convert-file", convertFileHandler)
	http.HandleFunc("/convert-folder", convertFolderHandler)
	http.HandleFunc("/choose-target", chooseTargetHandler)

	addr := ":8080"
	url := "http://127.0.0.1" + addr + "/"
	fmt.Printf("启动本地 GUI：%s\n", url)
	openBrowser(url)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func convertFileHandler(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(100 << 20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "请选择一个 .ncm 文件。", http.StatusBadRequest)
		return
	}
	defer file.Close()

	target := strings.TrimSpace(r.FormValue("target"))
	if target == "" {
		http.Error(w, "请填写目标文件夹路径。", http.StatusBadRequest)
		return
	}
	if err := ensureFolder(target); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	tempPath, cleanup, err := saveUploadedFile(file, header.Filename)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer cleanup()

	logText, err := convertOnce(tempPath, target)
	if err != nil {
		http.Error(w, logText+"\n"+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(logText))
}

func convertFolderHandler(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(200 << 20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	target := strings.TrimSpace(r.FormValue("target"))
	if target == "" {
		http.Error(w, "请填写目标文件夹路径。", http.StatusBadRequest)
		return
	}
	if err := ensureFolder(target); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	concurrency := parseConcurrency(r.FormValue("concurrency"))
	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		http.Error(w, "请先选择文件夹并包含 .ncm 文件。", http.StatusBadRequest)
		return
	}

	var candidates []*multipart.FileHeader
	for _, header := range files {
		if strings.HasSuffix(strings.ToLower(header.Filename), ".ncm") {
			candidates = append(candidates, header)
		}
	}
	if len(candidates) == 0 {
		http.Error(w, "未找到任何 .ncm 文件。", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "服务器不支持实时日志输出。", http.StatusInternalServerError)
		return
	}

	type batchResult struct {
		fileName string
		log      string
		err      error
	}

	results := make(chan batchResult, len(candidates))
	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)

	fmt.Fprintf(w, "开始批量转换，共 %d 个 .ncm 文件，最大并发 %d。\n", len(candidates), concurrency)
	flusher.Flush()

	for _, header := range candidates {
		wg.Add(1)
		head := header
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			result := batchResult{fileName: head.Filename}
			result.log = fmt.Sprintf("--- %s 开始转换 ---\n", head.Filename)

			file, err := head.Open()
			if err != nil {
				result.err = fmt.Errorf("打开文件失败：%w", err)
				result.log += fmt.Sprintf("%s\n", result.err)
				results <- result
				return
			}
			defer file.Close()

			tempPath, cleanup, err := saveUploadedFile(file, head.Filename)
			if err != nil {
				result.err = fmt.Errorf("保存临时文件失败：%w", err)
				result.log += fmt.Sprintf("%s\n", result.err)
				results <- result
				return
			}
			defer cleanup()

			logText, err := convertOnce(tempPath, target)
			result.log += logText
			if err != nil {
				result.err = fmt.Errorf("转换失败：%w", err)
			}
			results <- result
		}()
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	var processed int32
	failed := 0
	for res := range results {
		count := atomic.AddInt32(&processed, 1)
		fmt.Fprintf(w, "已完成 %d/%d：%s\n", count, len(candidates), res.fileName)
		fmt.Fprint(w, res.log)
		if res.err != nil {
			failed++
			fmt.Fprintf(w, "%s 处理失败：%v\n", res.fileName, res.err)
		}
		flusher.Flush()
	}

	fmt.Fprintf(w, "批量转换完成：%d 个文件，成功 %d，失败 %d。\n", len(candidates), len(candidates)-failed, failed)
	flusher.Flush()
}

func parseConcurrency(value string) int {
	const defaultConcurrency = 10
	if value == "" {
		return defaultConcurrency
	}
	c, err := strconv.Atoi(value)
	if err != nil || c < 1 {
		return defaultConcurrency
	}
	return c
}

func chooseTargetHandler(w http.ResponseWriter, r *http.Request) {
	dir, err := dialog.Directory().Title("选择目标文件夹").Browse()
	if err != nil {
		if err == dialog.ErrCancelled {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if dir == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(dir))
}

func saveUploadedFile(src io.Reader, filename string) (string, func(), error) {
	tempDir, err := os.MkdirTemp("", "ncmdump-upload-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() {
		os.RemoveAll(tempDir)
	}

	name := filepath.Base(filename)
	if name == "" {
		name = "temp.ncm"
	}
	if !strings.HasSuffix(strings.ToLower(name), ".ncm") {
		name += ".ncm"
	}

	tempPath := filepath.Join(tempDir, name)
	tempFile, err := os.OpenFile(tempPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	defer tempFile.Close()

	if _, err := io.Copy(tempFile, src); err != nil {
		cleanup()
		return "", nil, err
	}
	return tempPath, cleanup, nil
}

func convertOnce(srcPath, dstDir string) (string, error) {
	exePath, err := findNcmdumpExe()
	if err != nil {
		return "", err
	}

	cmd := exec.Command(exePath, "-o", dstDir, srcPath)
	output, err := cmd.CombinedOutput()
	text := fmt.Sprintf("命令: %s -o %s %s\n", exePath, dstDir, srcPath)
	text += string(output)
	if err != nil {
		return text, err
	}
	dstFile := filepath.Join(dstDir, strings.TrimSuffix(filepath.Base(srcPath), filepath.Ext(srcPath))+".mp3")
	text += fmt.Sprintf("完成：%s\n", dstFile)
	return text, nil
}

func ensureFolder(path string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s 不是目录", absPath)
	}
	return nil
}

func ensureNcmdumpInCurrentDir(cwd string) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	exePath := filepath.Join(cwd, "ncmdump.exe")
	if _, err := os.Stat(exePath); err == nil {
		return nil
	}

	fmt.Println("当前目录缺少 ncmdump.exe，正在从 GitHub 下载最新版本...")
	url, err := fetchLatestNcmdumpReleaseURL()
	if err != nil {
		return fmt.Errorf("获取最新 release 失败：%w", err)
	}

	tmpZip, err := downloadToTempFile(url)
	if err != nil {
		return fmt.Errorf("下载 ncmdump zip 失败：%w", err)
	}
	defer os.Remove(tmpZip)

	if err := unzipFile(tmpZip, cwd); err != nil {
		return fmt.Errorf("解压 ncmdump 失败：%w", err)
	}

	if _, err := os.Stat(exePath); err != nil {
		return fmt.Errorf("下载完成，但未找到 %s", exePath)
	}
	fmt.Println("已下载并解压 ncmdump.exe 到当前目录。")
	return nil
}

func fetchLatestNcmdumpReleaseURL() (string, error) {
	req, err := http.NewRequest("GET", "https://api.github.com/repos/taurusxin/ncmdump/releases/latest", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ncmdump-gui")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API 返回 %d", resp.StatusCode)
	}

	var data struct {
		Assets []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", err
	}

	target := fmt.Sprintf("%s-%s.zip", runtime.GOOS, runtime.GOARCH)
	var fallback string
	for _, asset := range data.Assets {
		name := strings.ToLower(asset.Name)
		if !strings.HasSuffix(name, ".zip") {
			continue
		}
		if strings.Contains(name, target) {
			if strings.HasPrefix(name, "ncmdump-") {
				return asset.BrowserDownloadURL, nil
			}
			if fallback == "" {
				fallback = asset.BrowserDownloadURL
			}
		}
	}
	if fallback != "" {
		return fallback, nil
	}

	for _, asset := range data.Assets {
		name := strings.ToLower(asset.Name)
		if !strings.HasSuffix(name, ".zip") {
			continue
		}
		if strings.HasPrefix(name, "ncmdump-") && strings.Contains(name, runtime.GOOS) {
			return asset.BrowserDownloadURL, nil
		}
	}
	return "", fmt.Errorf("未找到适合当前环境 (%s/%s) 的 release zip", runtime.GOOS, runtime.GOARCH)
}

func downloadToTempFile(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载失败，HTTP %d", resp.StatusCode)
	}

	tmpFile, err := os.CreateTemp("", "ncmdump-*.zip")
	if err != nil {
		return "", err
	}
	defer tmpFile.Close()

	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		return "", err
	}
	return tmpFile.Name(), nil
}

func unzipFile(zipPath, destDir string) error {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer reader.Close()

	for _, file := range reader.File {
		targetPath := filepath.Join(destDir, file.Name)
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(targetPath, os.ModePerm); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), os.ModePerm); err != nil {
			return err
		}
		dstFile, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, file.Mode())
		if err != nil {
			return err
		}
		srcFile, err := file.Open()
		if err != nil {
			dstFile.Close()
			return err
		}
		_, err = io.Copy(dstFile, srcFile)
		dstFile.Close()
		srcFile.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func findNcmdumpExe() (string, error) {
	exeName := "ncmdump.exe"
	if runtime.GOOS != "windows" {
		exeName = "ncmdump"
	}
	exePath, err := os.Executable()
	if err == nil {
		candidate := filepath.Join(filepath.Dir(exePath), exeName)
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate, nil
		}
	}
	if candidate, err := filepath.Abs(exeName); err == nil {
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("未找到 ncmdump.exe，请将它与本程序放在同一目录或放入 PATH")
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
