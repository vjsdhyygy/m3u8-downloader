// @author:llychao<lychao_vip@163.com> @edit:vjsdhyygy<vjsdhyygy@163.com>
// @contributor: Junyi<me@junyi.pw>
// @date:2026-02-11
// @功能: 多线程下载 + 广告切片暴力去重 + FFmpeg 外部合并
package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/levigross/grequests"
)

const (
	// HEAD_TIMEOUT 请求头超时时间
	HEAD_TIMEOUT = 5 * time.Second
	// PROGRESS_WIDTH 进度条长度
	PROGRESS_WIDTH = 20
	// TS_NAME_TEMPLATE ts视频片段命名规则
	TS_NAME_TEMPLATE = "%05d.ts"
)

var (
	// 命令行参数
	urlFlag = flag.String("u", "", "m3u8下载地址(http(s)://url/xx/xx/index.m3u8)")
	nFlag   = flag.Int("n", 24, "num:下载线程数(默认24)")
	htFlag  = flag.String("ht", "v1", "hostType:设置getHost的方式(v1: `http(s):// + url.Host + filepath.Dir(url.Path)`; v2: `http(s)://+ u.Host`")
	oFlag   = flag.String("o", "movie", "movieName:自定义文件名(默认为movie)不带后缀")
	cFlag   = flag.String("c", "", "cookie:自定义请求cookie")
	rFlag   = flag.Bool("r", false, "autoClear:是否自动清除ts文件")
	sFlag   = flag.Int("s", 0, "InsecureSkipVerify:是否允许不安全的请求(默认0)")
	spFlag  = flag.String("sp", "", "savePath:文件保存的绝对路径(默认为当前路径,建议默认值)")

	logger *log.Logger
	ro     = &grequests.RequestOptions{
		UserAgent:      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_13_6) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/79.0.3945.88 Safari/537.36",
		RequestTimeout: HEAD_TIMEOUT,
		Headers: map[string]string{
			"Connection":      "keep-alive",
			"Accept":          "*/*",
			"Accept-Encoding": "*",
			"Accept-Language": "zh-CN,zh;q=0.9, en;q=0.8, de;q=0.7, *;q=0.5",
		},
	}
)

// TsInfo 用于保存 ts 文件的下载地址和文件名
type TsInfo struct {
	Name string
	Url  string
}

func init() {
	logger = log.New(os.Stdout, "", log.Ldate|log.Ltime|log.Lshortfile)
}

func main() {
	Run()
}

func Run() {
	msgTpl := "[功能]:多线程下载直播流m3u8视屏\n[提醒]:下载失败，请使用 -ht=v2 \n[提醒]:下载失败，m3u8 地址可能存在嵌套\n[提醒]:进度条中途下载失败，可重复执行"
	fmt.Println(msgTpl)
	runtime.GOMAXPROCS(runtime.NumCPU())
	now := time.Now()

	// 1、解析命令行参数
	flag.Parse()
	m3u8Url := *urlFlag
	maxGoroutines := *nFlag
	hostType := *htFlag
	movieName := *oFlag
	autoClearFlag := *rFlag
	cookie := *cFlag
	insecure := *sFlag
	savePath := *spFlag

	ro.Headers["Referer"] = getHost(m3u8Url, "v2")
	if insecure != 0 {
		ro.InsecureSkipVerify = true
	}
	// http 自定义 cookie
	if cookie != "" {
		ro.Headers["Cookie"] = cookie
	}
	if !strings.HasPrefix(m3u8Url, "http") || m3u8Url == "" {
		flag.Usage()
		return
	}
	var download_dir string
	pwd, _ := os.Getwd()
	if savePath != "" {
		pwd = savePath
	}
	// 初始化下载ts的目录，后面所有的ts文件会保存在这里
	download_dir = filepath.Join(pwd, movieName)
	if isExist, _ := pathExists(download_dir); !isExist {
		os.MkdirAll(download_dir, os.ModePerm)
	}

	// 2、解析m3u8
	m3u8Host := getHost(m3u8Url, hostType)
	m3u8Body := getM3u8Body(m3u8Url)
	ts_key := getM3u8Key(m3u8Host, m3u8Body)
	if ts_key != "" {
		fmt.Printf("待解密 ts 文件 key : %s \n", ts_key)
	}
	ts_list := getTsList(m3u8Host, m3u8Body)
	fmt.Println("待下载 ts 文件数量:", len(ts_list))

	// 3、下载ts文件到download_dir
	downloader(ts_list, maxGoroutines, download_dir, ts_key)

	// 【新增】：去广告/重复项净化逻辑 (只要重复全删)
	purgeAllDuplicates(download_dir)

	if ok := checkTsDownDir(download_dir); !ok {
		fmt.Printf("\n[Failed] 请检查url地址有效性 \n")
		return
	}

	// 4、合并ts切割文件成mp4文件 (改为调用外部 FFmpeg)
	mv := mergeWithFFmpeg(download_dir, movieName, pwd)
	
	if autoClearFlag && mv != "" {
		//自动清除ts文件目录
		os.RemoveAll(download_dir)
	}

	//5、输出下载视频信息
	DrawProgressBar("Merging", float32(1), PROGRESS_WIDTH, mv)
	fmt.Printf("\n[Success] 下载保存路径：%s | 共耗时: %6.2fs\n", mv, time.Now().Sub(now).Seconds())
}

// purgeAllDuplicates 暴力去广告：只要MD5重复，全部物理删除
func purgeAllDuplicates(downloadDir string) {
	fmt.Printf("\n[校验] 正在扫描重复/广告切片...")
	hashCount := make(map[string]int)
	hashToFileList := make(map[string][]string)

	files, _ := ioutil.ReadDir(downloadDir)
	for _, f := range files {
		if f.IsDir() || filepath.Ext(f.Name()) != ".ts" {
			continue
		}
		path := filepath.Join(downloadDir, f.Name())
		
		file, err := os.Open(path)
		if err != nil { continue }
		h := md5.New()
		if _, err := io.Copy(h, file); err != nil {
			file.Close()
			continue
		}
		file.Close()
		sha := hex.EncodeToString(h.Sum(nil))

		hashCount[sha]++
		hashToFileList[sha] = append(hashToFileList[sha], path)
	}

	delCount := 0
	for sha, count := range hashCount {
		if count > 1 {
			fmt.Printf("\n[发现重复/广告] 内容哈希 %s 出现 %d 次，正在执行全数剔除...", sha[:8], count)
			for _, p := range hashToFileList[sha] {
				os.Remove(p)
				delCount++
			}
		}
	}
	fmt.Printf("\n[净化完成] 共剔除 %d 个异常切片。\n", delCount)
}

// mergeWithFFmpeg 调用系统 ffmpeg 执行 concat 无损合并
func mergeWithFFmpeg(downloadDir, movieName, savePath string) string {
	listPath := filepath.Join(downloadDir, "filelist.txt")
	listFile, err := os.Create(listPath)
	if err != nil { return "" }
	
	// 遍历目录生成清单
	files, _ := ioutil.ReadDir(downloadDir)
	hasTs := false
	for _, f := range files {
		if filepath.Ext(f.Name()) == ".ts" {
			listFile.WriteString(fmt.Sprintf("file '%s'\n", f.Name()))
			hasTs = true
		}
	}
	listFile.Close()

	if !hasTs { return "" }

	outputMp4 := filepath.Join(savePath, movieName+".mp4")
	
	// 调用外部 ffmpeg
	// -fflags +genpts: 重新生成时间戳，解决删除广告后的音画同步问题
	cmd := exec.Command("ffmpeg", "-f", "concat", "-safe", "0", "-i", listPath, "-c", "copy", "-fflags", "+genpts", "-y", outputMp4)
	
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		fmt.Printf("\n[FFmpeg Error]: %s\n", errOut.String())
		return ""
	}
	return outputMp4
}

// --- 以下辅助函数完全保留原样 ---

func getHost(Url, ht string) (host string) {
	u, err := url.Parse(Url)
	checkErr(err)
	switch ht {
	case "v1":
		host = u.Scheme + "://" + u.Host + filepath.Dir(u.EscapedPath())
	case "v2":
		host = u.Scheme + "://" + u.Host
	}
	return
}

func getM3u8Body(Url string) string {
	r, err := grequests.Get(Url, ro)
	checkErr(err)
	return r.String()
}

func getM3u8Key(host, html string) (key string) {
	lines := strings.Split(html, "\n")
	key = ""
	for _, line := range lines {
		if strings.Contains(line, "#EXT-X-KEY") {
			if !strings.Contains(line, "URI") {
				continue
			}
			uri_pos := strings.Index(line, "URI")
			quotation_mark_pos := strings.LastIndex(line, "\"")
			key_url := strings.Split(line[uri_pos:quotation_mark_pos], "\"")[1]
			if !strings.Contains(line, "http") {
				key_url = fmt.Sprintf("%s/%s", host, key_url)
			}
			res, err := grequests.Get(key_url, ro)
			checkErr(err)
			if res.StatusCode == 200 {
				key = res.String()
				break
			}
		}
	}
	return
}

func getTsList(host, body string) (tsList []TsInfo) {
	lines := strings.Split(body, "\n")
	index := 0
	var ts TsInfo
	for _, line := range lines {
		if !strings.HasPrefix(line, "#") && line != "" {
			index++
			if strings.HasPrefix(line, "http") {
				ts = TsInfo{
					Name: fmt.Sprintf(TS_NAME_TEMPLATE, index),
					Url:  line,
				}
				tsList = append(tsList, ts)
			} else {
				line = strings.TrimPrefix(line, "/")
				ts = TsInfo{
					Name: fmt.Sprintf(TS_NAME_TEMPLATE, index),
					Url:  fmt.Sprintf("%s/%s", host, line),
				}
				tsList = append(tsList, ts)
			}
		}
	}
	return
}

func downloadTsFile(ts TsInfo, download_dir, key string, retries int) {
	if retries <= 0 {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			downloadTsFile(ts, download_dir, key, retries-1)
		}
	}()
	curr_path_file := filepath.Join(download_dir, ts.Name)
	if isExist, _ := pathExists(curr_path_file); isExist {
		return
	}
	res, err := grequests.Get(ts.Url, ro)
	if err != nil || !res.Ok {
		if retries > 0 {
			downloadTsFile(ts, download_dir, key, retries-1)
			return
		} else {
			return
		}
	}
	var origData []byte
	origData = res.Bytes()
	contentLen := 0
	contentLenStr := res.Header.Get("Content-Length")
	if contentLenStr != "" {
		contentLen, _ = strconv.Atoi(contentLenStr)
	}
	if len(origData) == 0 || (contentLen > 0 && len(origData) < contentLen) || res.Error != nil {
		downloadTsFile(ts, download_dir, key, retries-1)
		return
	}
	if key != "" {
		origData, err = AesDecrypt(origData, []byte(key))
		if err != nil {
			downloadTsFile(ts, download_dir, key, retries-1)
			return
		}
	}
	syncByte := uint8(71) //0x47
	bLen := len(origData)
	for j := 0; j < bLen; j++ {
		if origData[j] == syncByte {
			origData = origData[j:]
			break
		}
	}
	ioutil.WriteFile(curr_path_file, origData, 0666)
}

func downloader(tsList []TsInfo, maxGoroutines int, downloadDir string, key string) {
	retry := 5
	var wg sync.WaitGroup
	limiter := make(chan struct{}, maxGoroutines)
	tsLen := len(tsList)
	downloadCount := 0
	for _, ts := range tsList {
		wg.Add(1)
		limiter <- struct{}{}
		go func(ts TsInfo, downloadDir, key string, retryies int) {
			defer func() {
				wg.Done()
				<-limiter
			}()
			downloadTsFile(ts, downloadDir, key, retryies)
			downloadCount++
			DrawProgressBar("Downloading", float32(downloadCount)/float32(tsLen), PROGRESS_WIDTH, ts.Name)
			return
		}(ts, downloadDir, key, retry)
	}
	wg.Wait()
}

func checkTsDownDir(dir string) bool {
	// 修改为检查目录是否存在且有ts文件
	files, _ := ioutil.ReadDir(dir)
	return len(files) > 0
}

func DrawProgressBar(prefix string, proportion float32, width int, suffix ...string) {
	pos := int(proportion * float32(width))
	s := fmt.Sprintf("[%s] %s%*s %6.2f%% \t%s",
		prefix, strings.Repeat("■", pos), width-pos, "", proportion*100, strings.Join(suffix, ""))
	fmt.Print("\r" + s)
}

func pathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func PKCS7Padding(ciphertext []byte, blockSize int) []byte {
	padding := blockSize - len(ciphertext)%blockSize
	padtext := bytes.Repeat([]byte{byte(padding)}, padding)
	return append(ciphertext, padtext...)
}

func PKCS7UnPadding(origData []byte) []byte {
	length := len(origData)
	if length == 0 { return nil }
	unpadding := int(origData[length-1])
	return origData[:(length - unpadding)]
}

func AesDecrypt(crypted, key []byte, ivs ...[]byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	blockSize := block.BlockSize()
	var iv []byte
	if len(ivs) == 0 {
		iv = key
	} else {
		iv = ivs[0]
	}
	blockMode := cipher.NewCBCDecrypter(block, iv[:blockSize])
	origData := make([]byte, len(crypted))
	blockMode.CryptBlocks(origData, crypted)
	origData = PKCS7UnPadding(origData)
	return origData, nil
}

func checkErr(e error) {
	if e != nil {
		logger.Panic(e)
	}
}
