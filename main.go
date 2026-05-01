package main

import (
	"io"
	"os"
	"fmt"
	"time"
	"errors"
	"regexp"
	"strings"
	"net/url"
	"encoding/json"
	"path/filepath"

	"github.com/bogem/id3v2/v2"
	"github.com/go-flac/go-flac"
	"github.com/go-flac/flacvorbis"
	"github.com/Sorrow446/go-mp4tag"
	"github.com/go-flac/flacpicture"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/navidrome/navidrome/plugins/pdk/go/metadata"
	"github.com/navidrome/navidrome/plugins/pdk/go/scrobbler"
)

const (
	qobuzBaseURL     = "https://www.qobuz.com/api.json/0.2"
	defaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_3) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15"
)

type qobuzAgent struct{}

var (
	_ metadata.ArtistBiographyProvider = (*qobuzAgent)(nil)
	_ metadata.ArtistImagesProvider    = (*qobuzAgent)(nil)
	_ metadata.AlbumImagesProvider     = (*qobuzAgent)(nil)
	_ metadata.AlbumInfoProvider       = (*qobuzAgent)(nil)
	_ metadata.SimilarArtistsProvider  = (*qobuzAgent)(nil)
	_ lyrics.Lyrics                    = (*qobuzAgent)(nil)
	_ scrobbler.Scrobbler              = (*qobuzAgent)(nil)
)

func init() {
	agent := &qobuzAgent{}
	metadata.Register(agent)
	lyrics.Register(agent)
	scrobbler.Register(agent)

	pdk.Log(pdk.LogInfo, "🚀 模式: 动态鉴权 + Qobuz 元数据 (JSON 泛型容错版) + 歌词")
}

func getConfigString(key, defaultVal string) string {
	val, ok := pdk.GetConfig(key)
	if !ok || val == "" { return defaultVal }
	return val
}

func getConfigBool(key string, defaultVal bool) bool {
	val, ok := pdk.GetConfig(key)
	if !ok || val == "" { return defaultVal }
	v := strings.ToLower(val)
	return v == "true" || v == "1" || v == "t" || v == "yes" || v == "y" || v == "on"
}

func getMainToken() string { return getConfigString("qobuz_token_main", "") }
func getFrToken() string   { return getConfigString("qobuz_token_fr", "") }
func getAppID() string     { return getConfigString("qobuz_app_id", "100000000") }

func getRegionCode(useFrToken bool) string {
	if useFrToken { return "fr" }
	return getConfigString("qobuz_region", "fr")
}

func appendRegion(urlStr string, useFrToken bool) string {
	region := getRegionCode(useFrToken)
	if strings.Contains(urlStr, "?") {
		return urlStr + "&country_code=" + region
	}
	return urlStr + "?country_code=" + region
}

func buildQobuzHeaders(useFrToken bool) map[string]string {
	var token string
	if useFrToken {
		token = getFrToken()
	} else {
		token = getMainToken()
	}
	
	token = strings.TrimSpace(token)
	
	headers := map[string]string{
		"X-App-Id":        getAppID(),
		"User-Agent":      defaultUserAgent,
		"Accept":          "application/json",
		"Accept-Language": "fr-FR,fr;q=0.9,en-US;q=0.8,en;q=0.7,zh-CN;q=0.6", 
	}
	if token != "" {
		headers["X-User-Auth-Token"] = token
	}
	return headers
}

type SongData struct {
	ID        string   `json:"ID"`
	Name      string   `json:"Name"`
	TrackNum  int      `json:"TrackNum"`
	DiscNum   int      `json:"DiscNum"`
	Artists   []string `json:"Artists"`
	ISRC      string   `json:"ISRC"`
	Genre     string   `json:"Genre"`
	WorkInfo  string   `json:"WorkInfo"`
	Composer  string   `json:"Composer"`
	Lyric     string   `json:"Lyric,omitempty"`
}

type AlbumData struct {
	AlbumID      string     `json:"AlbumID"`
	AlbumName    string     `json:"AlbumName"`
	PicURL       string     `json:"PicURL"`
	ArtistPicURL string     `json:"ArtistPicURL"`
	ArtistBio    string     `json:"ArtistBio"`
	Description  string     `json:"Description"`
	Company      string     `json:"Company"`
	PublishTime  int64      `json:"PublishTime"`
	PDFLink      string     `json:"PDFLink"`
	PDFName      string     `json:"PDFName"`
	Songs        []SongData `json:"Songs"`
}

func getLocalAlbumData(albumDir string) (AlbumData, bool) {
	b, err := os.ReadFile(filepath.Join(albumDir, "qobuz_metadata.json"))
	if err == nil {
		var data AlbumData
		if err := json.Unmarshal(b, &data); err == nil && data.AlbumID != "" { return data, true }
	}
	return AlbumData{}, false
}

func saveLocalAlbumData(albumDir string, data AlbumData) {
	b, _ := json.MarshalIndent(data, "", "  ")
	os.WriteFile(filepath.Join(albumDir, "qobuz_metadata.json"), b, 0666)
}

func isTrackProcessed(albumDir, filename string) bool {
	content, err := os.ReadFile(filepath.Join(albumDir, "qobuz_processed.txt"))
	if err != nil { return false }
	return strings.Contains(string(content), filename+"\n")
}

func markTrackProcessed(albumDir, filename string) {
	f, err := os.OpenFile(filepath.Join(albumDir, "qobuz_processed.txt"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err == nil {
		f.WriteString(filename + "\n")
		f.Close()
	}
}

func matchLocalFileToSong(filename string, songs []SongData) (SongData, bool) {
	reNum := regexp.MustCompile(`^\s*0*(\d+)`)
	match := reNum.FindStringSubmatch(filename)
	var fileTrackNum int
	if len(match) > 1 { fmt.Sscanf(match[1], "%d", &fileTrackNum) }

	if fileTrackNum > 0 {
		for _, s := range songs { if s.TrackNum == fileTrackNum { return s, true } }
	}
	for _, s := range songs {
		if fuzzyMatch(filename, s.Name) { return s, true }
	}
	return SongData{}, false
}

func cleanArtistName(artist string) string {
	if artist == "[Unknown Artist]" || artist == "Unknown Artist" || artist == "Unknown" { return "" }
	return artist
}

func compactText(text string) string {
	if text == "" { return "" }
	text = strings.ReplaceAll(text, "</p>", "\n")
	text = strings.ReplaceAll(text, "<p>", "")
	text = strings.ReplaceAll(text, "<br>", "\n")
	text = strings.ReplaceAll(text, "<br/>", "\n")
	text = strings.ReplaceAll(text, "<br />", "\n")
	reHtml := regexp.MustCompile(`(?i)<[^>]*?>`)
	text = reHtml.ReplaceAllString(text, "")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	reSpace := regexp.MustCompile(`\n{3,}`)
	text = reSpace.ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text)
}

func removeAccents(s string) string {
	s = strings.ToLower(s)
	replacements := []string{
		"é", "e", "è", "e", "ê", "e", "ë", "e",
		"á", "a", "à", "a", "â", "a", "ä", "a",
		"í", "i", "ì", "i", "î", "i", "ï", "i",
		"ó", "o", "ò", "o", "ô", "o", "ö", "o",
		"ú", "u", "ù", "u", "û", "u", "ü", "u",
		"ñ", "n", "ç", "c",
		"\u0301", "", "\u0300", "", "\u0302", "", "\u0308", "", "\u0303", "", "\u0327", "",
	}
	r := strings.NewReplacer(replacements...)
	return r.Replace(s)
}

func cleanSearchTerm(text string) string {
	re := regexp.MustCompile(`[\[\(\{].*?[\]\)\}]`)
	text = re.ReplaceAllString(text, " ")
	return strings.TrimSpace(strings.Join(strings.Fields(text), " "))
}

func fuzzyMatch(s1, s2 string) bool {
	re := regexp.MustCompile(`[^\p{L}\p{N}]+`)
	n1 := re.ReplaceAllString(removeAccents(strings.ToLower(s1)), "")
	n2 := re.ReplaceAllString(removeAccents(strings.ToLower(s2)), "")
	if n1 == "" || n2 == "" { return false }
	if n1 == n2 { return true }
	if len(n1) > 3 && len(n2) > 3 {
		if strings.Contains(n1, n2) || strings.Contains(n2, n1) { return true }
	}
	reAscii := regexp.MustCompile(`[^\x00-\x7F]+`)
	a1 := reAscii.ReplaceAllString(n1, "")
	a2 := reAscii.ReplaceAllString(n2, "")
	if len(a1) > 3 && len(a2) > 3 {
		if strings.Contains(a1, a2) || strings.Contains(a2, a1) { return true }
	}
	return false
}

func cleanLyric(text string) string {
	if text == "" { return "" }
	reBy := regexp.MustCompile(`\[by:.*?\]\n?`)
	text = reBy.ReplaceAllString(text, "")
	reAd := regexp.MustCompile(`(?i)\[\d{2}:\d{2}[\.:]\d{2,3}\].*?(www\.|.net|.com|翻译:|QQ:|微信:).*?\n?`)
	text = reAd.ReplaceAllString(text, "")
	return strings.TrimSpace(text)
}

func mergeTranslatedLyrics(lrc string, tlyric string) string {
	if lrc == "" { return "" }
	pattern := regexp.MustCompile(`\[(\d{2}:\d{2})(?:\.\d{2,3})?\](.*)`)
	tagPattern := regexp.MustCompile(`\[(.*?)\]`)
	tMap := make(map[string]string)

	if tlyric != "" {
		tLines := strings.Split(tlyric, "\n")
		for _, line := range tLines {
			matches := pattern.FindStringSubmatch(line)
			if len(matches) >= 3 {
				timeKey := matches[1]
				content := strings.TrimSpace(matches[2])
				if content != "" { tMap[timeKey] = content }
			}
		}
	}

	var merged []string
	seen := make(map[string]bool)
	lLines := strings.Split(lrc, "\n")
	
	for _, line := range lLines {
		matches := pattern.FindStringSubmatch(line)
		if len(matches) >= 3 {
			timeKey := matches[1]
			originalText := strings.TrimSpace(matches[2])
			
			originalTimeTag := ""
			tagMatch := tagPattern.FindStringSubmatch(line)
			if len(tagMatch) >= 2 { originalTimeTag = tagMatch[1] }
			
			origLine := fmt.Sprintf("[%s]%s", originalTimeTag, originalText)
			if !seen[origLine] && originalText != "" {
				merged = append(merged, origLine)
				seen[origLine] = true
			}

			if transText, exists := tMap[timeKey]; exists && transText != "" && transText != originalText {
				transLine := fmt.Sprintf("[%s]%s", originalTimeTag, transText)
				if !seen[transLine] {
					merged = append(merged, transLine)
					seen[transLine] = true
				}
			}
		} else {
			if !seen[line] && strings.TrimSpace(line) != "" {
				merged = append(merged, line)
				seen[line] = true
			}
		}
	}
	return strings.Join(merged, "\n")
}

func resolveNeteaseSongID(title, artist string) int64 {
	query := title + " " + artist
	cacheKey := "netease_song_id_" + cleanSearchTerm(query)
	if data, ok, _ := host.KVStoreGet(cacheKey); ok {
		var id int64
		fmt.Sscanf(string(data), "%d", &id)
		if id > 0 { return id }
	}

	safeQuery := url.QueryEscape(query)
	apiURL := fmt.Sprintf("https://music.163.com/api/search/get/web?s=%s&type=1&offset=0&limit=1", safeQuery)
	resp, err := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: apiURL, Headers: map[string]string{"User-Agent": defaultUserAgent, "Referer": "https://music.163.com/"}})
	if err != nil { return 0 }

	var sr struct {
		Result struct {
			Songs []struct { ID int64 `json:"id"` } `json:"songs"`
		} `json:"result"`
	}
	json.Unmarshal(resp.Body, &sr)
	if len(sr.Result.Songs) > 0 {
		id := sr.Result.Songs[0].ID
		host.KVStoreSet(cacheKey, []byte(fmt.Sprintf("%d", id)))
		return id
	}
	return 0
}

type lyricResponse struct {
	Lrc    struct{ Lyric string `json:"lyric"` } `json:"lrc"`
	Tlyric struct{ Lyric string `json:"lyric"` } `json:"tlyric"`
}

func fetchAndWriteLocalLyrics(title, artist, absolutePath string) string {
	if absolutePath == "" { return "" }
	saveDir := filepath.Dir(absolutePath)
	ext := filepath.Ext(absolutePath)
	baseName := strings.TrimSuffix(filepath.Base(absolutePath), ext)
	lrcPath := filepath.Join(saveDir, baseName+".lrc")

	if content, err := os.ReadFile(lrcPath); err == nil {
		return string(content)
	}

	var lrcText, tlyricText string
	customAPI := getConfigString("lyrics_api_url", "")

	if customAPI != "" {
		customAPI = strings.TrimRight(customAPI, "/")
		safeTitle := url.QueryEscape(title)
		safeArtist := url.QueryEscape(artist)
		apiURL := fmt.Sprintf("%s/api/lyric?title=%s&artist=%s", customAPI, safeTitle, safeArtist)
		
		resp, err := host.HTTPSend(host.HTTPRequest{
			Method: "GET",
			URL:    apiURL,
			Headers: map[string]string{"User-Agent": defaultUserAgent},
		})
		if err == nil && resp.StatusCode == 200 {
			var customResp struct {
				Lrc    interface{} `json:"lrc"`
				Tlyric interface{} `json:"tlyric"`
			}
			if errParse := json.Unmarshal(resp.Body, &customResp); errParse == nil {
				extractLyric := func(v interface{}) string {
					if s, ok := v.(string); ok { return s }
					if m, ok := v.(map[string]interface{}); ok {
						if l, ok := m["lyric"].(string); ok { return l }
					}
					return ""
				}
				lrcText = cleanLyric(extractLyric(customResp.Lrc))
				tlyricText = cleanLyric(extractLyric(customResp.Tlyric))
			}
		}
	}
	
	if lrcText == "" {
		songID := resolveNeteaseSongID(title, artist)
		if songID != 0 {
			apiURL := "https://interface3.music.163.com/api/song/lyric"
			payload := fmt.Sprintf("id=%d&cp=false&tv=0&lv=0&rv=0&kv=0&yv=0&ytv=0&yrv=0", songID)
			resp, err := host.HTTPSend(host.HTTPRequest{
				Method:  "POST",
				URL:     apiURL,
				Headers: map[string]string{"User-Agent": defaultUserAgent, "Referer": "https://music.163.com/", "Content-Type": "application/x-www-form-urlencoded", "Cookie": "os=pc"},
				Body:    []byte(payload),
			})
			if err == nil && resp.StatusCode == 200 {
				var lrcResp lyricResponse
				json.Unmarshal(resp.Body, &lrcResp)
				lrcText = cleanLyric(lrcResp.Lrc.Lyric)
				tlyricText = cleanLyric(lrcResp.Tlyric.Lyric)
			}
		}
	}

	if lrcText == "" { return "" }
	finalLyric := mergeTranslatedLyrics(lrcText, tlyricText)

	if getConfigBool("enable_write_lyrics", true) {
		os.WriteFile(lrcPath, []byte(finalLyric), 0666)
	}
	return finalLyric
}

type ArtistCache struct {
	Biography string `json:"bio"`
	ImageURL  string `json:"img"`
	Timestamp int64  `json:"ts"`
}

func fetchQobuzArtistInfo(artistName string) (string, string) {
	searchName := cleanArtistName(artistName)
	if searchName == "" { return "", "" }

	sUrl := fmt.Sprintf("%s/catalog/search?query=%s&type=artists&limit=1", qobuzBaseURL, url.QueryEscape(searchName))
	sUrl = appendRegion(sUrl, false)
	sResp, err := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: sUrl, Headers: buildQobuzHeaders(false)})
	
	if err != nil {
		pdk.Log(pdk.LogError, fmt.Sprintf("[Qobuz API] 获取歌手网络请求失败: %v", err))
		return "", ""
	}
	if sResp.StatusCode != 200 {
		pdk.Log(pdk.LogError, fmt.Sprintf("[Qobuz API] 获取歌手失败 HTTP %d: %s", sResp.StatusCode, string(sResp.Body)))
		return "", ""
	}

	var sr struct { Artists struct { Items []struct { ID interface{} `json:"id"` } `json:"items"` } `json:"artists"` }
	if err := json.Unmarshal(sResp.Body, &sr); err != nil { return "", "" }
	if len(sr.Artists.Items) == 0 { return "", "" }

	targetArtistID := fmt.Sprintf("%v", sr.Artists.Items[0].ID)
	if targetArtistID == "0" || targetArtistID == "<nil>" || targetArtistID == "" { return "", "" }

	aUrl := fmt.Sprintf("%s/artist/get?artist_id=%s", qobuzBaseURL, targetArtistID)
	aUrl = appendRegion(aUrl, false)
	aResp, err := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: aUrl, Headers: buildQobuzHeaders(false)})
	if err != nil || aResp.StatusCode != 200 { return "", "" }

	var artDetail struct {
		Biography struct { Content string `json:"content"` } `json:"biography"`
		Image     struct { Large   string `json:"large"`   } `json:"image"`
	}
	json.Unmarshal(aResp.Body, &artDetail)

	bio := compactText(artDetail.Biography.Content)
	img := artDetail.Image.Large

	if bio != "" || img != "" {
		pdk.Log(pdk.LogInfo, fmt.Sprintf("[Qobuz API] 👤 成功拉取歌手详细信息: %s", searchName))
	}
	return bio, img
}

func getCachedArtistInfo(artistName string) (string, string) {
	if cleanArtistName(artistName) == "" { return "", "" }
	cacheKey := "qobuz_artist" + cleanSearchTerm(artistName)
	
	if data, ok, _ := host.KVStoreGet(cacheKey); ok {
		var cache ArtistCache
		if err := json.Unmarshal(data, &cache); err == nil {
			if time.Now().Unix()-cache.Timestamp < 30*86400 { return cache.Biography, cache.ImageURL }
		}
	}

	bio, img := fetchQobuzArtistInfo(artistName)
	if bio != "" || img != "" {
		cache := ArtistCache{ Biography: bio, ImageURL: img, Timestamp: time.Now().Unix() }
		if b, err := json.Marshal(cache); err == nil { host.KVStoreSet(cacheKey, b) }
	}
	return bio, img
}

func fetchCompleteAlbumData(albumName, artistName string) (AlbumData, error) {
	var data AlbumData
	data.AlbumName = albumName

	query := cleanSearchTerm(albumName)
	artistClean := cleanSearchTerm(cleanArtistName(artistName))
	
	if artistClean != "" { 
		query += " " + artistClean 
	} else {
		pdk.Log(pdk.LogInfo, fmt.Sprintf("[Qobuz API] ⚠️ 歌手未知，降级为仅使用专辑名进行宽泛搜索: [%s]", query))
	}
	
	searchURL := fmt.Sprintf("%s/catalog/search?query=%s&type=albums&limit=1", qobuzBaseURL, url.QueryEscape(query))
	searchURL = appendRegion(searchURL, false)
	respSearch, err := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: searchURL, Headers: buildQobuzHeaders(false)})
	
	if err != nil {
		pdk.Log(pdk.LogError, fmt.Sprintf("[Qobuz API] Search HTTP Request Error: %v", err))
		return data, fmt.Errorf("search failed") 
	}
	if respSearch.StatusCode != 200 {
		pdk.Log(pdk.LogError, fmt.Sprintf("[Qobuz API] Search HTTP %d: %s", respSearch.StatusCode, string(respSearch.Body)))
		return data, fmt.Errorf("search failed") 
	}

	var sr struct { Albums struct { Items []struct { ID interface{} `json:"id"` } `json:"items"` } `json:"albums"` }
	if err := json.Unmarshal(respSearch.Body, &sr); err != nil {
		pdk.Log(pdk.LogError, fmt.Sprintf("[Qobuz API] Search JSON Parse Error: %v", err))
		return data, fmt.Errorf("parse failed")
	}
	if len(sr.Albums.Items) == 0 { 
		pdk.Log(pdk.LogInfo, fmt.Sprintf("[Qobuz API] Search returned 0 items for: %s", query))
		return data, fmt.Errorf("album not found") 
	}

	albumID := strings.ReplaceAll(fmt.Sprintf("%v", sr.Albums.Items[0].ID), "qobuz_", "")
	pdk.Log(pdk.LogInfo, fmt.Sprintf("[Qobuz API] ✅ 匹配到专辑 ID: %s，正在请求详细数据...", albumID))

	data, err = getAlbumDetailByID(albumID, data, artistName, false)
	if err != nil { return data, err }

	needsFallback := false
	if data.PDFLink == "" || data.Description == "" {
		needsFallback = true
	}

	if needsFallback {
		pdk.Log(pdk.LogInfo, "[Qobuz API] ⚠️ 主区域数据不完整 (缺失 PDF 或 专辑介绍)，触发强力匿名/跨区补全...")
		frData, err := getAlbumDetailByID(albumID, AlbumData{}, artistName, true)
		if err == nil {
			if data.PDFLink == "" && frData.PDFLink != "" {
				data.PDFLink = frData.PDFLink
				data.PDFName = frData.PDFName
				pdk.Log(pdk.LogInfo, "[Qobuz API] ✅ 成功从回退渠道补全 PDF 链接")
			}
			if data.Description == "" && frData.Description != "" {
				data.Description = frData.Description
				pdk.Log(pdk.LogInfo, "[Qobuz API] ✅ 成功从回退渠道补全专辑介绍")
			}
		}
	}

	if data.PDFLink != "" {
		pdfTag := fmt.Sprintf("PDF:%s", data.PDFLink)
		if data.Description != "" {
			data.Description = pdfTag + "\n" + data.Description
		} else {
			data.Description = pdfTag
		}
		pdk.Log(pdk.LogInfo, "[DEBUG Qobuz] 已成功将 PDF 标签合并入 Description")
	}

	return data, nil
}

func getAlbumDetailByID(albumID string, data AlbumData, fallbackArtist string, useFrToken bool) (AlbumData, error) {
	detailURL := fmt.Sprintf("%s/album/get?album_id=%s&offset=0&limit=50&extra=track_ids,albumsFromSameArtist", qobuzBaseURL, albumID)
	detailURL = appendRegion(detailURL, useFrToken)
	
	regionLog := " 🌐 主区域"
	if useFrToken {
		regionLog = "🇫🇷 回退区"
	}
	pdk.Log(pdk.LogInfo, fmt.Sprintf("[DEBUG Qobuz] 开始请求 %s 专辑详情 URL: %s", regionLog, detailURL))

	respAlbum, err := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: detailURL, Headers: buildQobuzHeaders(useFrToken)})
	
	if err != nil {
		pdk.Log(pdk.LogError, fmt.Sprintf("[Qobuz API] %s Detail HTTP Request Error: %v", regionLog, err))
		return data, fmt.Errorf("detail request failed") 
	}
	if respAlbum.StatusCode != 200 {
		pdk.Log(pdk.LogError, fmt.Sprintf("[Qobuz API] %s Detail HTTP %d: %s", regionLog, respAlbum.StatusCode, string(respAlbum.Body)))
		return data, fmt.Errorf("detail request failed") 
	}

	var rawMap map[string]interface{}
	if err := json.Unmarshal(respAlbum.Body, &rawMap); err != nil {
		pdk.Log(pdk.LogError, fmt.Sprintf("[Qobuz API] %s 基础泛型解析失败: %v", regionLog, err))
	} else {
		var extractedDesc string
		
		if d, ok := rawMap["description"].(string); ok && strings.TrimSpace(d) != "" {
			extractedDesc = strings.TrimSpace(d)
			pdk.Log(pdk.LogInfo, fmt.Sprintf("[DEBUG %s] 成功从外层 description 提取专辑介绍", regionLog))
		} else {
			if facts, ok := rawMap["product_sales_facts"].(map[string]interface{}); ok {
				if ed, ok := facts["editorial"].(map[string]interface{}); ok {
					if d, ok := ed["description"].(string); ok && strings.TrimSpace(d) != "" {
						extractedDesc = strings.TrimSpace(d)
						pdk.Log(pdk.LogInfo, fmt.Sprintf("[DEBUG %s] 成功从 product_sales_facts 提取专辑介绍", regionLog))
					}
				}
			}
			
			if extractedDesc == "" {
				if factors, ok := rawMap["product_sales_factors"].(map[string]interface{}); ok {
					if ed, ok := factors["editorial"].(map[string]interface{}); ok {
						if d, ok := ed["description"].(string); ok && strings.TrimSpace(d) != "" {
							extractedDesc = strings.TrimSpace(d)
							pdk.Log(pdk.LogInfo, fmt.Sprintf("[DEBUG %s] 成功从 product_sales_factors 提取专辑介绍", regionLog))
						}
					}
				}
			}
		}

		if extractedDesc == "" {
			if catch, ok := rawMap["catchline"].(string); ok && strings.TrimSpace(catch) != "" {
				extractedDesc = catch
				pdk.Log(pdk.LogInfo, fmt.Sprintf("[DEBUG %s] 成功从 catchline 提取兜底介绍", regionLog))
			}
		}

		cleanDesc := compactText(extractedDesc)
		if cleanDesc != "" {
			data.Description = cleanDesc
		}
		
		var pdfLink, pdfName string
		if goodies, ok := rawMap["goodies"].([]interface{}); ok {
			for _, item := range goodies {
				if gMap, ok := item.(map[string]interface{}); ok {
					gName, _ := gMap["name"].(string)
					gUrl, _ := gMap["url"].(string)
					gOrig, _ := gMap["original_name"].(string)
					
					var gFormatId int
					if f, ok := gMap["file_format_id"].(float64); ok {
						gFormatId = int(f)
					}
					
					lowerName := strings.ToLower(gName)
					if (gFormatId == 25 || gFormatId == 21 || strings.Contains(lowerName, "booklet") || strings.Contains(lowerName, "livret")) && gUrl != "" {
						pdfLink = gUrl
						pdfName = gOrig
						pdk.Log(pdk.LogInfo, fmt.Sprintf("[DEBUG %s] 成功匹配到 PDF", regionLog))
						break
					}
				}
			}
		}
		
		if pdfLink != "" {
			data.PDFLink = pdfLink
			data.PDFName = pdfName
		}
		
		pdk.Log(pdk.LogInfo, fmt.Sprintf("[DEBUG Qobuz %s] 解析完成 -> Description长度: %d, PDF链接: %s", regionLog, len(data.Description), data.PDFLink))
	}

	var detail struct {
		ID          interface{} `json:"id"`
		Title       string `json:"title"`
		Label       struct { Name string `json:"name"` } `json:"label"`
		ReleasedAt  int64 `json:"released_at"`
		Genre       struct { Name string `json:"name"` } `json:"genre"`
		Image       struct { Large string `json:"large"` } `json:"image"`
		Artist      struct { ID interface{} `json:"id"`; Name string `json:"name"` } `json:"artist"`
		Tracks      struct {
			Items []struct {
				ID          interface{} `json:"id"`
				Title       string `json:"title"`
				Work        string `json:"work"`
				TrackNumber int    `json:"track_number"`
				MediaNumber int    `json:"media_number"`
				ISRC        string `json:"isrc"`
				Composer    struct { ID interface{} `json:"id"`; Name string `json:"name"` } `json:"composer"`
				Performer   struct { Name string `json:"name"` } `json:"performer"`
			} `json:"items"`
		} `json:"tracks"`
	}
	
	if err := json.Unmarshal(respAlbum.Body, &detail); err != nil {
		pdk.Log(pdk.LogError, fmt.Sprintf("[Qobuz API] 核心骨架解析失败: %v", err))
		return data, fmt.Errorf("parse error")
	}

	if !useFrToken {
		data.AlbumID = fmt.Sprintf("%v", detail.ID)
		data.AlbumName = detail.Title
		data.PicURL = detail.Image.Large
		data.Company = detail.Label.Name
		data.PublishTime = detail.ReleasedAt * 1000

		targetArtistName := detail.Artist.Name
		if targetArtistName == "" && len(detail.Tracks.Items) > 0 { targetArtistName = detail.Tracks.Items[0].Performer.Name }
		if targetArtistName == "" { targetArtistName = fallbackArtist }
		
		bio, img := getCachedArtistInfo(targetArtistName)
		if img != "" { data.ArtistPicURL = img }
		if bio != "" { data.ArtistBio = bio } 

		for _, t := range detail.Tracks.Items {
			work := t.Work
			if work == "" { work = t.Title }
			compName := t.Composer.Name
			
			compId := fmt.Sprintf("%v", t.Composer.ID)
			workInfo := work

			if compName != "" {
				workInfo = fmt.Sprintf("%s (%s)", work, compName)
				if compId != "0" && compId != "<nil>" && compId != "" { 
					workInfo = fmt.Sprintf("%s [ID:qobuz_%s]", workInfo, compId) 
				}
			}

			performer := t.Performer.Name
			if performer == "" { performer = detail.Artist.Name }
			if performer == "" { performer = fallbackArtist }

			data.Songs = append(data.Songs, SongData{
				ID:       fmt.Sprintf("%v", t.ID),
				Name:     t.Title,
				TrackNum: t.TrackNumber,
				DiscNum:  t.MediaNumber,
				ISRC:     t.ISRC,
				Artists:  []string{performer},
				Genre:    detail.Genre.Name,
				WorkInfo: workInfo,
				Composer: compName,
			})
		}
	}
	return data, nil
}

func downloadImage(urlStr, savePath string) {
	if urlStr == "" || savePath == "" { return }
	if stat, err := os.Stat(savePath); err == nil && stat.Size() > 1024 { return }
	resp, err := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: urlStr, Headers: buildQobuzHeaders(false)})
	if err == nil && resp.StatusCode == 200 { os.WriteFile(savePath, resp.Body, 0666) }
}

func downloadPDF(urlStr, folderPath, originalName string) {
	if urlStr == "" || folderPath == "" { return }
	fileName := originalName
	if fileName == "" { fileName = "booklet.pdf" }
	fullPath := filepath.Join(folderPath, fileName)
	if stat, err := os.Stat(fullPath); err == nil && stat.Size() > 1024 { return } 
	
	resp, err := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: urlStr, Headers: buildQobuzHeaders(false)})
	if err == nil && resp.StatusCode == 200 { os.WriteFile(fullPath, resp.Body, 0666) }
}

func cleanFlacFile(absPath string) error {
	file, err := os.Open(absPath)
	if err != nil { return err }
	header := make([]byte, 10)
	if _, err := file.Read(header); err != nil { file.Close(); return err }
	if string(header[0:3]) != "ID3" { file.Close(); return fmt.Errorf("未检测到 ID3 头部") }
	size := (int(header[6]) << 21) | (int(header[7]) << 14) | (int(header[8]) << 7) | int(header[9])
	totalSize := int64(size + 10)
	magic := make([]byte, 4)
	if _, err := file.ReadAt(magic, totalSize); err != nil { file.Close(); return err }
	if string(magic) != "fLaC" { file.Close(); return fmt.Errorf("按协议计算大小后未找到真实的 fLaC 标识，跳过修复") }
	tempPath := absPath + ".tmp"
	tempFile, err := os.Create(tempPath)
	if err != nil { file.Close(); return err }
	file.Seek(totalSize, 0)
	_, err = io.Copy(tempFile, file)
	tempFile.Close()
	file.Close()
	if err != nil { os.Remove(tempPath); return err }
	return os.Rename(tempPath, absPath)
}

func writeTags(absPath, ext string, song SongData, album AlbumData, year, comment, description, lyric string, picData []byte) bool {
	filename := filepath.Base(absPath)
	artistStr := strings.Join(song.Artists, "/")

	defer func() {
		if r := recover(); r != nil {
			pdk.Log(pdk.LogError, fmt.Sprintf("[TagWriter] 🚨 致命崩溃拦截 (%s): %v", filename, r))
		}
	}()

	switch ext {
	case ".mp3":
		tag, err := id3v2.Open(absPath, id3v2.Options{Parse: true})
		if err != nil { return false }
		defer tag.Close()
		tag.SetDefaultEncoding(id3v2.EncodingUTF8)

		changed := false
		if tag.Artist() == "" && artistStr != "" { tag.SetArtist(artistStr); changed = true }
		if tag.Album() == "" && album.AlbumName != "" { tag.SetAlbum(album.AlbumName); changed = true }
		if tag.Year() == "" && year != "" { tag.SetYear(year); changed = true }

		if len(tag.GetFrames("TRCK")) == 0 && song.TrackNum > 0 { tag.AddTextFrame("TRCK", id3v2.EncodingUTF8, fmt.Sprintf("%d", song.TrackNum)); changed = true }
		if len(tag.GetFrames("TPOS")) == 0 && song.DiscNum > 0 { tag.AddTextFrame("TPOS", id3v2.EncodingUTF8, fmt.Sprintf("%d", song.DiscNum)); changed = true }
		if len(tag.GetFrames("TPUB")) == 0 && album.Company != "" { tag.AddTextFrame("TPUB", id3v2.EncodingUTF8, album.Company); changed = true }
		if len(tag.GetFrames("TSRC")) == 0 && song.ISRC != "" { tag.AddTextFrame("TSRC", id3v2.EncodingUTF8, song.ISRC); changed = true }
		if len(tag.GetFrames("TCON")) == 0 && song.Genre != "" { tag.AddTextFrame("TCON", id3v2.EncodingUTF8, song.Genre); changed = true }
		if len(tag.GetFrames("TIT1")) == 0 && song.WorkInfo != "" { tag.AddTextFrame("TIT1", id3v2.EncodingUTF8, song.WorkInfo); changed = true }
		if len(tag.GetFrames("TCOM")) == 0 && song.Composer != "" { tag.AddTextFrame("TCOM", id3v2.EncodingUTF8, song.Composer); changed = true }

		hasComm := false
		for _, f := range tag.AllFrames() { for _, frame := range f { if _, ok := frame.(id3v2.CommentFrame); ok { hasComm = true } } }
		if !hasComm && comment != "" {
			tag.AddCommentFrame(id3v2.CommentFrame{Encoding: id3v2.EncodingUTF8, Language: "eng", Text: comment})
			changed = true
		}

		if len(tag.GetFrames(tag.CommonID("Unsynchronised lyrics/text transcription"))) == 0 && lyric != "" {
			tag.AddUnsynchronisedLyricsFrame(id3v2.UnsynchronisedLyricsFrame{Encoding: id3v2.EncodingUTF8, Language: "eng", Lyrics: lyric})
			changed = true
		}

		hasPic := false
		for _, f := range tag.AllFrames() { for _, frame := range f { if _, ok := frame.(id3v2.PictureFrame); ok { hasPic = true } } }
		if !hasPic && len(picData) > 0 {
			tag.AddAttachedPicture(id3v2.PictureFrame{ Encoding: id3v2.EncodingUTF8, MimeType: "image/jpeg", PictureType: id3v2.PTFrontCover, Description: "Front Cover", Picture: picData })
			changed = true
		}

		if changed {
			if err := tag.Save(); err == nil { pdk.Log(pdk.LogInfo, fmt.Sprintf("[Phase2] 成功写入 MP3 标签: %s", filename)) }
		}
		return true

	case ".flac":
		f, err := flac.ParseFile(absPath)
		if err != nil {
			if strings.Contains(err.Error(), "fLaC head incorrect") {
				if fixErr := cleanFlacFile(absPath); fixErr == nil { f, err = flac.ParseFile(absPath) }
			}
			if err != nil { return false }
		}

		var cmt *flacvorbis.MetaDataBlockVorbisComment
		for _, meta := range f.Meta {
			if meta.Type == flac.VorbisComment { cmt, _ = flacvorbis.ParseFromMetaDataBlock(*meta); break }
		}
		if cmt == nil { cmt = flacvorbis.New() }

		getFlacLen := func(key string) int { v, _ := cmt.Get(key); return len(v) }
		changed := false

		if getFlacLen("ARTIST") == 0 && len(song.Artists) > 0 {
			for _, a := range song.Artists { cmt.Add("ARTIST", a) }
			cmt.Add("ALBUMARTIST", artistStr)
			changed = true
		}
		if getFlacLen("ALBUM") == 0 && album.AlbumName != "" { cmt.Add("ALBUM", album.AlbumName); changed = true }
		if getFlacLen("DATE") == 0 && year != "" { cmt.Add("DATE", year); changed = true }
		if getFlacLen("TRACKNUMBER") == 0 && song.TrackNum > 0 { cmt.Add("TRACKNUMBER", fmt.Sprintf("%d", song.TrackNum)); changed = true }
		if getFlacLen("DISCNUMBER") == 0 && song.DiscNum > 0 { cmt.Add("DISCNUMBER", fmt.Sprintf("%d", song.DiscNum)); changed = true }
		if getFlacLen("ORGANIZATION") == 0 && getFlacLen("LABEL") == 0 && album.Company != "" {
			cmt.Add("ORGANIZATION", album.Company); cmt.Add("LABEL", album.Company); changed = true
		}
		if getFlacLen("ISRC") == 0 && song.ISRC != "" { cmt.Add("ISRC", song.ISRC); changed = true }
		if getFlacLen("GENRE") == 0 && song.Genre != "" { cmt.Add("GENRE", song.Genre); changed = true }
		
		if getFlacLen("COMMENT") == 0 && comment != "" { cmt.Add("COMMENT", comment); changed = true }
		if getFlacLen("DESCRIPTION") == 0 && description != "" { cmt.Add("DESCRIPTION", description); changed = true }
		if getFlacLen("LYRICS") == 0 && lyric != "" { cmt.Add("LYRICS", lyric); changed = true }
		
		if getFlacLen("WORK") == 0 && song.WorkInfo != "" { cmt.Add("WORK", song.WorkInfo); changed = true }
		if getFlacLen("GROUPING") == 0 && song.WorkInfo != "" { cmt.Add("GROUPING", song.WorkInfo); changed = true }
		if getFlacLen("COMPOSER") == 0 && song.Composer != "" { cmt.Add("COMPOSER", song.Composer); changed = true }

		hasPic := false
		var newMeta []*flac.MetaDataBlock
		for _, meta := range f.Meta {
			if meta.Type != flac.VorbisComment {
				if meta.Type == flac.Picture { hasPic = true }
				newMeta = append(newMeta, meta)
			}
		}

		if !hasPic && len(picData) > 0 {
			pic, err := flacpicture.NewFromImageData(flacpicture.PictureTypeFrontCover, "Front Cover", picData, "image/jpeg")
			if err == nil {
				picBlock := pic.Marshal(); newMeta = append(newMeta, &picBlock); changed = true
			}
		}

		if changed {
			cmtBlock := cmt.Marshal(); newMeta = append(newMeta, &cmtBlock); f.Meta = newMeta
			tempPath := absPath + ".tmp_tag"
			if err := f.Save(tempPath); err != nil { os.Remove(tempPath); return false }
			if err := os.Rename(tempPath, absPath); err == nil { pdk.Log(pdk.LogInfo, fmt.Sprintf("[Phase2] 成功写入 FLAC 标签: %s", filename)) }
		}
		return true

	case ".m4a", ".alac", ".aac":
		mp4, err := mp4tag.Open(absPath)
		if err != nil { return false }
		defer mp4.Close()
		tags, err := mp4.Read()
		if err != nil { tags = &mp4tag.MP4Tags{} }

		changed := false
		if tags.Artist == "" && artistStr != "" { tags.Artist = artistStr; changed = true }
		if tags.AlbumArtist == "" && artistStr != "" { tags.AlbumArtist = artistStr; changed = true }
		if tags.Album == "" && album.AlbumName != "" { tags.Album = album.AlbumName; changed = true }
		if tags.Date == "" && year != "" { tags.Date = year; changed = true }
		if tags.TrackNumber == 0 && song.TrackNum > 0 { tags.TrackNumber = int16(song.TrackNum); changed = true }
		if tags.DiscNumber == 0 && song.DiscNum > 0 { tags.DiscNumber = int16(song.DiscNum); changed = true }
		if tags.CustomGenre == "" && song.Genre != "" { tags.CustomGenre = song.Genre; changed = true }
		
		if tags.Custom == nil { tags.Custom = make(map[string]string) }
		if _, exists := tags.Custom["grouping"]; !exists && song.WorkInfo != "" { tags.Custom["grouping"] = song.WorkInfo; changed = true }
		if _, exists := tags.Custom["©grp"]; !exists && song.WorkInfo != "" { tags.Custom["©grp"] = song.WorkInfo; changed = true }
		if tags.Composer == "" && song.Composer != "" { tags.Composer = song.Composer; changed = true }
		if _, exists := tags.Custom["label"]; !exists && album.Company != "" { tags.Custom["label"] = album.Company; changed = true }
		if _, exists := tags.Custom["ISRC"]; !exists && song.ISRC != "" { tags.Custom["ISRC"] = song.ISRC; changed = true }

		if tags.Lyrics == "" && lyric != "" { tags.Lyrics = lyric; changed = true }
		if tags.Comment == "" && comment != "" { tags.Comment = comment; changed = true }
		if _, exists := tags.Custom["description"]; !exists && description != "" { tags.Custom["description"] = description; changed = true }

		if len(tags.Pictures) == 0 && len(picData) > 0 {
			tags.Pictures = []*mp4tag.MP4Picture{{Data: picData}}
			changed = true
		}

		if changed {
			if err := mp4.Write(tags, []string{}); err == nil { pdk.Log(pdk.LogInfo, fmt.Sprintf("[Phase2] 成功写入 M4A 标签: %s", filename)) }
		}
		return true
	}
	return false
}

type subsonicAlbumResponse struct {
	SubsonicResponse struct {
		Album struct {
			Song []struct {
				Path        string `json:"path"`
				Artist      string `json:"artist"`
				AlbumArtist string `json:"albumArtist"`
				Suffix      string `json:"suffix"`
				Size        int64  `json:"size"`
			} `json:"song"`
		} `json:"album"`
	} `json:"subsonic-response"`
}

type subsonicSongResponse struct {
	SubsonicResponse struct {
		Song struct { 
			Path        string `json:"path"`
			Suffix      string `json:"suffix"`
			Size        int64  `json:"size"`
			Artist      string `json:"artist"`
			AlbumArtist string `json:"albumArtist"`
		} `json:"song"`
	} `json:"subsonic-response"`
}

var errWalkStop = errors.New("stop walk")

func findAudioBySize(root, suffix string, size int64) (string, error) {
	if size <= 0 { return "", fmt.Errorf("invalid size") }
	dotSuffix := "." + suffix
	var found string
	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, dotSuffix) { return nil }
		if info.Size() == size { found = path; return errWalkStop }
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, errWalkStop) { return "", walkErr }
	if found == "" { return "", fmt.Errorf("not found") }
	return found, nil
}

func resolveAbsolutePath(relPath, suffix string, size int64) (string, error) {
	libraries, _ := host.LibraryGetAllLibraries()
	for _, lib := range libraries {
		root := lib.MountPoint
		if root == "" { root = lib.Path }
		if root == "" { continue }
		direct := filepath.Join(root, relPath)
		if _, err := os.Stat(direct); err == nil { return direct, nil }
		if actualPath, searchErr := findAudioBySize(root, suffix, size); searchErr == nil { return actualPath, nil }
	}
	return "", fmt.Errorf("not found absolute")
}

func resolveFromRelativePath(relPath string) string {
	if relPath == "" || filepath.IsAbs(relPath) { return relPath }
	libraries, err := host.LibraryGetAllLibraries()
	if err == nil {
		for _, lib := range libraries {
			root := lib.MountPoint
			if root == "" { root = lib.Path }
			if root == "" { continue }
			fullPath := filepath.Join(root, relPath)
			if _, err := os.Stat(fullPath); err == nil {
				if absPath, err := filepath.Abs(fullPath); err == nil { return absPath }
				return fullPath 
			}
		}
	}
	if absFallback, err := filepath.Abs(relPath); err == nil { return absFallback }
	return relPath
}

func getAlbumDirAndArtistFromID(albumID string) (string, string) {
	if albumID == "" { return "", "" }
	jsonStr, err := host.SubsonicAPICall("getAlbum?id=" + albumID + "&u=admin&f=json&v=1.16.0")
	if err != nil { return "", "" }
	var resp subsonicAlbumResponse
	json.Unmarshal([]byte(jsonStr), &resp)
	
	if len(resp.SubsonicResponse.Album.Song) > 0 {
		song := resp.SubsonicResponse.Album.Song[0]
		art := cleanArtistName(song.AlbumArtist)
		if art == "" { art = cleanArtistName(song.Artist) }
		
		abs, _ := resolveAbsolutePath(song.Path, song.Suffix, song.Size)
		if abs == "" { abs = resolveFromRelativePath(song.Path) }
		
		if abs != "" {
			return filepath.Dir(abs), art
		}
	}
	return "", ""
}

func findAlbumDirViaSubsonicAPI(albumName, artistName string) (string, string) {
	if albumName == "" { return "", "" }
	query := url.QueryEscape(albumName)
	jsonStr, err := host.SubsonicAPICall(fmt.Sprintf("search3?query=%s&albumCount=10&u=admin&f=json&v=1.16.0", query))
	if err != nil { return "", "" }

	var resp struct {
		SubsonicResponse struct {
			SearchResult3 struct {
				Album []struct {
					ID     string `json:"id"`
					Name   string `json:"name"`
				} `json:"album"`
			} `json:"searchResult3"`
		} `json:"subsonic-response"`
	}
	json.Unmarshal([]byte(jsonStr), &resp)

	for _, alb := range resp.SubsonicResponse.SearchResult3.Album {
		if fuzzyMatch(alb.Name, albumName) {
			dir, art := getAlbumDirAndArtistFromID(alb.ID)
			if dir != "" { return dir, art }
		}
	}
	return "", ""
}

func guessAlbumPathAndArtist(albumName, artistName string) (string, string) {
	libraries, _ := host.LibraryGetAllLibraries()
	cleanArtist := cleanArtistName(artistName)
	cleanAlbum := cleanSearchTerm(albumName) 
	
	for _, lib := range libraries {
		root := lib.MountPoint
		if root == "" { root = lib.Path }
		if root == "" { continue }
		
		if cleanArtist != "" {
			guess1 := filepath.Join(root, cleanArtist, albumName)
			if stat, err := os.Stat(guess1); err == nil && stat.IsDir() { return guess1, cleanArtist }
			
			artistGuess := filepath.Join(root, cleanArtist)
			if stat, err := os.Stat(artistGuess); err == nil && stat.IsDir() {
				if subEntries, err := os.ReadDir(artistGuess); err == nil {
					for _, sub := range subEntries {
						if !sub.IsDir() { continue }
						if fuzzyMatch(cleanAlbum, sub.Name()) || strings.Contains(strings.ToLower(sub.Name()), strings.ToLower(cleanAlbum)) {
							return filepath.Join(artistGuess, sub.Name()), cleanArtist
						}
					}
				}
			}
		}
		
		if entries, err := os.ReadDir(root); err == nil {
			for _, entry := range entries {
				if !entry.IsDir() { continue }
				
				isArtistFolder := cleanArtist != "" && (fuzzyMatch(cleanArtist, entry.Name()) || strings.Contains(strings.ToLower(entry.Name()), strings.ToLower(cleanArtist)))
				
				if isArtistFolder || cleanArtist == "" {
					artistDir := filepath.Join(root, entry.Name())
					if subEntries, err := os.ReadDir(artistDir); err == nil {
						for _, sub := range subEntries {
							if !sub.IsDir() { continue }
							if fuzzyMatch(cleanAlbum, sub.Name()) || strings.Contains(strings.ToLower(sub.Name()), strings.ToLower(cleanAlbum)) {
								return filepath.Join(artistDir, sub.Name()), entry.Name()
							}
						}
					}
				} 
				
				if fuzzyMatch(cleanAlbum, entry.Name()) || strings.Contains(strings.ToLower(entry.Name()), strings.ToLower(cleanAlbum)) { return filepath.Join(root, entry.Name()), "" }
			}
		}
	}
	return "", cleanArtist
}

func getSongDetailsFromSubsonic(username, trackID string) (*subsonicSongResponse, error) {
	if username == "" { username = "admin" }
	jsonStr, err := host.SubsonicAPICall("getSong?id=" + trackID + "&u=" + username + "&f=json&v=1.16.0")
	if err != nil { return nil, err }
	var resp subsonicSongResponse
	json.Unmarshal([]byte(jsonStr), &resp)
	if resp.SubsonicResponse.Song.Path == "" { return nil, fmt.Errorf("relpath failed") }
	return &resp, nil
}

func getTrackArtistAndDir(username, trackID, trackArtist, fallbackPath string) (string, string) {
	var abs string
	finalArtist := ""

	if detail, err := getSongDetailsFromSubsonic(username, trackID); err == nil {
		abs, _ = resolveAbsolutePath(detail.SubsonicResponse.Song.Path, detail.SubsonicResponse.Song.Suffix, detail.SubsonicResponse.Song.Size)

		if aArtist := cleanArtistName(detail.SubsonicResponse.Song.AlbumArtist); aArtist != "" {
			finalArtist = aArtist
		} else if art := cleanArtistName(detail.SubsonicResponse.Song.Artist); art != "" {
			finalArtist = art
		}
	}

	if abs == "" { abs = resolveFromRelativePath(fallbackPath) }
	if finalArtist == "" { finalArtist = cleanArtistName(trackArtist) }

	if finalArtist == "" && abs != "" {
		parts := strings.Split(filepath.ToSlash(abs), "/")
		if len(parts) >= 3 {
			guessedArtist := parts[len(parts)-3]
			if guessedArtist != "" && !strings.Contains(guessedArtist, "Music Library") && guessedArtist != "." {
				finalArtist = guessedArtist
			}
		}
	}
	return finalArtist, abs
}

func fetchAndCacheAlbum(albumID, albumName, artistName, knownDir string) AlbumData {
	finalArtist := strings.TrimSpace(artistName)
	if finalArtist == "[Unknown Artist]" || finalArtist == "Unknown Artist" || finalArtist == "Unknown" {
		finalArtist = ""
	}
	
	albumDir := knownDir
	
	if albumID != "" {
		apiDir, apiArt := getAlbumDirAndArtistFromID(albumID)
		if apiArt != "" && finalArtist == "" { finalArtist = apiArt }
		if apiDir != "" && albumDir == "" { albumDir = apiDir }
	}
	
	if albumDir == "" {
		apiDir, apiArt := findAlbumDirViaSubsonicAPI(albumName, finalArtist)
		if apiDir != "" {
			albumDir = apiDir
			if apiArt != "" && finalArtist == "" { finalArtist = apiArt }
		}
	}

	if albumDir == "" {
		guessDir, inferredArtist := guessAlbumPathAndArtist(albumName, finalArtist)
		albumDir = guessDir
		if finalArtist == "" && inferredArtist != "" { finalArtist = inferredArtist }
	}

	if albumDir != "" {
		if localData, found := getLocalAlbumData(albumDir); found { 
			if getConfigBool("enable_write_cover_image", true) && localData.PicURL != "" { 
				downloadImage(localData.PicURL, filepath.Join(albumDir, "cover.jpg")) 
			}
			if getConfigBool("enable_write_artist_image", true) && localData.ArtistPicURL != "" { 
				downloadImage(localData.ArtistPicURL, filepath.Join(filepath.Dir(albumDir), "artist.jpg")) 
			}
			return localData 
		}
	}

	cacheKey := fmt.Sprintf("qobuz_album_%s_%s", cleanSearchTerm(albumName), cleanSearchTerm(finalArtist))
	
	if data, ok, _ := host.KVStoreGet(cacheKey); ok {
		var album AlbumData
		if err := json.Unmarshal(data, &album); err == nil && album.AlbumID != "" { 
			if albumDir != "" {
				if _, found := getLocalAlbumData(albumDir); !found {
					saveLocalAlbumData(albumDir, album)
				}
				if getConfigBool("enable_write_cover_image", true) && album.PicURL != "" {
					downloadImage(album.PicURL, filepath.Join(albumDir, "cover.jpg"))
				}
				if getConfigBool("enable_write_artist_image", true) && album.ArtistPicURL != "" {
					downloadImage(album.ArtistPicURL, filepath.Join(filepath.Dir(albumDir), "artist.jpg"))
				}
			}
			return album 
		}
	}

	lockKey := "lock:" + cacheKey
	if lockData, ok, _ := host.KVStoreGet(lockKey); ok {
		var ts int64
		fmt.Sscanf(string(lockData), "%d", &ts)
		if time.Now().Unix()-ts < 15 { 
			pdk.Log(pdk.LogInfo, fmt.Sprintf("[Phase1] ⏳ 检测到并发请求，正在等待数据缓存就绪: %s", albumName))
			for i := 0; i < 15; i++ {
				time.Sleep(1 * time.Second)
				if data, ok, _ := host.KVStoreGet(cacheKey); ok {
					var album AlbumData
					if err := json.Unmarshal(data, &album); err == nil && album.AlbumID != "" { 
						if albumDir != "" {
							if _, found := getLocalAlbumData(albumDir); !found { saveLocalAlbumData(albumDir, album) }
							if getConfigBool("enable_write_cover_image", true) && album.PicURL != "" { downloadImage(album.PicURL, filepath.Join(albumDir, "cover.jpg")) }
							if getConfigBool("enable_write_artist_image", true) && album.ArtistPicURL != "" { downloadImage(album.ArtistPicURL, filepath.Join(filepath.Dir(albumDir), "artist.jpg")) }
						}
						return album 
					}
				}
			}
			return AlbumData{}
		}
	}
	host.KVStoreSet(lockKey, []byte(fmt.Sprintf("%d", time.Now().Unix())))

	pdk.Log(pdk.LogInfo, fmt.Sprintf("[Phase1] 🌐 本地无缓存，正在拉取 Qobuz API: 专辑[%s]", albumName))
	fetchedData, err := fetchCompleteAlbumData(albumName, finalArtist)
	
	if (err != nil || fetchedData.AlbumID == "") && finalArtist != "" {
		pdk.Log(pdk.LogInfo, fmt.Sprintf("[Phase1] ⚠️ 带歌手搜索失败，降级为【纯专辑名】模糊搜索: [%s]", albumName))
		fetchedData, err = fetchCompleteAlbumData(albumName, "")
	}
	
	if err == nil && fetchedData.AlbumID != "" {
		if b, e := json.Marshal(fetchedData); e == nil { host.KVStoreSet(cacheKey, b) }
		
		if albumDir != "" {
			saveLocalAlbumData(albumDir, fetchedData)
			if getConfigBool("enable_write_cover_image", true) && fetchedData.PicURL != "" { downloadImage(fetchedData.PicURL, filepath.Join(albumDir, "cover.jpg")) }
			if getConfigBool("enable_write_artist_image", true) && fetchedData.ArtistPicURL != "" { downloadImage(fetchedData.ArtistPicURL, filepath.Join(filepath.Dir(albumDir), "artist.jpg")) }
			if getConfigBool("enable_write_pdf", true) && fetchedData.PDFLink != "" { downloadPDF(fetchedData.PDFLink, albumDir, fetchedData.PDFName) }
			pdk.Log(pdk.LogInfo, "[Phase1] ✅ API 抓取完成，成功写入本地及下载附加资源")
		} else {
			pdk.Log(pdk.LogInfo, "[Phase1] ⚠️ 内存加载成功，但无法锁定物理路径，跳过写入硬盘")
		}
		return fetchedData
	}
	return AlbumData{}
}

func (a *qobuzAgent) GetAlbumInfo(input metadata.AlbumRequest) (*metadata.AlbumInfoResponse, error) {
	pdk.Log(pdk.LogInfo, fmt.Sprintf("[Phase1] 发起【专辑介绍】请求: %s", input.Name))
	data := fetchAndCacheAlbum("", input.Name, input.Artist, "")

	if data.AlbumID != "" {
		desc := data.Description
		
		rePDF := regexp.MustCompile(`PDF:(https?://[^\s<]+)\s*`)
		if rePDF.MatchString(desc) {
			desc = rePDF.ReplaceAllString(desc, `<a href="$1" style="color: #EAB308; font-weight: bold; text-decoration: underline;" target="_blank">点击下载 PDF </a>`)
		}
        
		desc = strings.ReplaceAll(desc, "\n", "<br><br>")
		
		return &metadata.AlbumInfoResponse{Description: desc}, nil
	}
	return nil, nil
}

func (a *qobuzAgent) GetAlbumImages(input metadata.AlbumRequest) (*metadata.AlbumImagesResponse, error) {
	pdk.Log(pdk.LogInfo, fmt.Sprintf("[Phase1] 发起【专辑封面】请求: %s", input.Name))
	data := fetchAndCacheAlbum("", input.Name, input.Artist, "")

	if data.PicURL != "" {
		return &metadata.AlbumImagesResponse{Images: []metadata.ImageInfo{{URL: data.PicURL, Size: 1200}}}, nil
	}
	return nil, nil
}

func (a *qobuzAgent) GetArtistBiography(input metadata.ArtistRequest) (*metadata.ArtistBiographyResponse, error) {
	bio, _ := getCachedArtistInfo(input.Name)
	if bio != "" {
		bio = strings.ReplaceAll(bio, "\n", "<br>")
		return &metadata.ArtistBiographyResponse{Biography: bio}, nil
	}
	return nil, nil
}

func guessArtistPath(artistName string) string {
	libraries, _ := host.LibraryGetAllLibraries()
	cleanArtist := cleanArtistName(artistName)
	if cleanArtist == "" { return "" }
	for _, lib := range libraries {
		root := lib.MountPoint
		if root == "" { root = lib.Path }
		if root == "" { continue }
		guess := filepath.Join(root, cleanArtist)
		if stat, err := os.Stat(guess); err == nil && stat.IsDir() { return guess }
		if entries, err := os.ReadDir(root); err == nil {
			for _, entry := range entries {
				if entry.IsDir() && (fuzzyMatch(cleanArtist, entry.Name()) || strings.Contains(strings.ToLower(entry.Name()), strings.ToLower(cleanArtist))) {
					return filepath.Join(root, entry.Name())
				}
			}
		}
	}
	return ""
}

func (a *qobuzAgent) GetArtistImages(input metadata.ArtistRequest) (*metadata.ArtistImagesResponse, error) {
	_, img := getCachedArtistInfo(input.Name)
	if img != "" {
		if getConfigBool("enable_write_artist_image", true) {
			artistDir := guessArtistPath(input.Name)
			if artistDir != "" {
				downloadImage(img, filepath.Join(artistDir, "artist.jpg"))
			}
		}
		return &metadata.ArtistImagesResponse{Images: []metadata.ImageInfo{{URL: img, Size: 1200}}}, nil
	}
	return nil, nil
}

func (a *qobuzAgent) GetSimilarArtists(input metadata.SimilarArtistsRequest) (*metadata.SimilarArtistsResponse, error) {
	searchName := cleanArtistName(input.Name)
	if searchName == "" { return nil, nil }

	sUrl := fmt.Sprintf("%s/catalog/search?query=%s&type=artists&limit=1", qobuzBaseURL, url.QueryEscape(searchName))
	sUrl = appendRegion(sUrl, false)
	sResp, err := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: sUrl, Headers: buildQobuzHeaders(false)})
	if err != nil || sResp.StatusCode != 200 { return nil, nil }

	var sr struct { Artists struct { Items []struct { ID interface{} `json:"id"` } `json:"items"` } `json:"artists"` }
	json.Unmarshal(sResp.Body, &sr)
	if len(sr.Artists.Items) == 0 { return nil, nil }

	targetArtistID := fmt.Sprintf("%v", sr.Artists.Items[0].ID)
	if targetArtistID == "0" || targetArtistID == "<nil>" || targetArtistID == "" { return nil, nil }

	simUrl := fmt.Sprintf("%s/artist/getSimilarArtists?artist_id=%s&limit=20", qobuzBaseURL, targetArtistID)
	simUrl = appendRegion(simUrl, false)
	simResp, err := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: simUrl, Headers: buildQobuzHeaders(false)})
	if err != nil || simResp.StatusCode != 200 { return nil, nil }

	var sim struct {
		Artists struct {
			Items []struct {
				ID   interface{} `json:"id"`
				Name string      `json:"name"`
			} `json:"items"`
		} `json:"artists"`
	}
	json.Unmarshal(simResp.Body, &sim)

	var res []metadata.ArtistRef
	for _, art := range sim.Artists.Items {
		if art.Name != "" {
			res = append(res, metadata.ArtistRef{
				ID:   fmt.Sprintf("qobuz_art_%v", art.ID),
				Name: art.Name,
			})
		}
	}
	
	pdk.Log(pdk.LogInfo, fmt.Sprintf("[Qobuz API] 成功获取 %s 的相似艺人: %d 个", input.Name, len(res)))
	return &metadata.SimilarArtistsResponse{Artists: res}, nil
}

func (a *qobuzAgent) GetLyrics(input lyrics.GetLyricsRequest) (lyrics.GetLyricsResponse, error) { 
	if !getConfigBool("enable_lyrics", true) { return lyrics.GetLyricsResponse{}, nil }
	
	_, abs := getTrackArtistAndDir("admin", input.Track.ID, input.Track.Artist, input.Track.Path)
	lyricText := fetchAndWriteLocalLyrics(input.Track.Title, input.Track.Artist, abs)
	
	if lyricText == "" { return lyrics.GetLyricsResponse{}, nil }
	return lyrics.GetLyricsResponse{Lyrics: []lyrics.LyricsText{{Text: lyricText}}}, nil
}

func (a *qobuzAgent) IsAuthorized(_ scrobbler.IsAuthorizedRequest) (bool, error) { return true, nil }

func runDiskWritePhase(absPath, title, finalArtist, originalAlbum string) {
	defer func() {
		if r := recover(); r != nil { pdk.Log(pdk.LogError, fmt.Sprintf("🚨 致命崩溃拦截: %v", r)) }
	}()

	if !getConfigBool("enable_write_metadata", true) { return }
	ext := strings.ToLower(filepath.Ext(absPath))
	if ext == ".wav" { return }

	lockKey := fmt.Sprintf("qobuz_track:%s", absPath)
	if lockData, ok, _ := host.KVStoreGet(lockKey); ok {
		var ts int64
		fmt.Sscanf(string(lockData), "%d", &ts)
		if time.Now().Unix()-ts < 15 { return }
	}
	host.KVStoreSet(lockKey, []byte(fmt.Sprintf("%d", time.Now().Unix())))

	albumDir := filepath.Dir(absPath)
	fileName := filepath.Base(absPath)

	if isTrackProcessed(albumDir, fileName) { return }

	pdk.Log(pdk.LogInfo, fmt.Sprintf("[Phase2] 正在为单曲增量写入元数据: %s", fileName))

	var albumData AlbumData
	if localData, found := getLocalAlbumData(albumDir); found {
		albumData = localData
	} else {
		albumData = fetchAndCacheAlbum("", originalAlbum, finalArtist, albumDir)
	}

	if albumData.AlbumID == "" { return }

	if getConfigBool("enable_write_cover_image", true) && albumData.PicURL != "" {
		downloadImage(albumData.PicURL, filepath.Join(albumDir, "cover.jpg"))
	}
	if getConfigBool("enable_write_artist_image", true) && albumData.ArtistPicURL != "" {
		downloadImage(albumData.ArtistPicURL, filepath.Join(filepath.Dir(albumDir), "artist.jpg"))
	}

	matchedSong, foundSong := matchLocalFileToSong(fileName, albumData.Songs)
	if !foundSong { return }

	lyricText := matchedSong.Lyric
	if lyricText == "" {
		lyricText = fetchAndWriteLocalLyrics(matchedSong.Name, finalArtist, absPath)
		if lyricText != "" && foundSong {
			for i, s := range albumData.Songs {
				if s.ID == matchedSong.ID {
					albumData.Songs[i].Lyric = lyricText
					saveLocalAlbumData(albumDir, albumData)
					pdk.Log(pdk.LogInfo, "[Phase2] 歌词已成功合并至 qobuz_metadata.json")
					break
				}
			}
		}
	}

	var picData []byte
	if getConfigBool("enable_write_cover_image", true) {
		picData, _ = os.ReadFile(filepath.Join(albumDir, "cover.jpg"))
	}

	finalComment := albumData.Description

	year := ""
	if albumData.PublishTime > 0 { year = time.Unix(albumData.PublishTime/1000, 0).Format("2006") }

	if writeTags(absPath, ext, matchedSong, albumData, year, finalComment, finalComment, lyricText, picData) {
		markTrackProcessed(albumDir, fileName)
	}
}

func (a *qobuzAgent) NowPlaying(req scrobbler.NowPlayingRequest) error {
	finalArtist, abs := getTrackArtistAndDir(req.Username, req.Track.ID, req.Track.Artist, req.Track.Path)
	if abs != "" { 
		runDiskWritePhase(abs, req.Track.Title, finalArtist, req.Track.Album) 
	}
	return nil
}

func (a *qobuzAgent) Scrobble(req scrobbler.ScrobbleRequest) error {
	finalArtist, abs := getTrackArtistAndDir(req.Username, req.Track.ID, req.Track.Artist, req.Track.Path)
	if abs != "" { 
		runDiskWritePhase(abs, req.Track.Title, finalArtist, req.Track.Album) 
	}
	return nil
}
