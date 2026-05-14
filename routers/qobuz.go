package routers

import (
	"fmt"
	"time"
	"regexp"
	"strings"
	"net/url"
	"encoding/json"

	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
)

const (
	qobuzBaseURL     = "https://www.qobuz.com/api.json/0.2"
	defaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_3) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15"
)

func getMainToken() string { return getConfigString("qobuz_token_main", "") }
func getFrToken() string   { return getConfigString("qobuz_token_fr", "") }
func getAppID() string     { return getConfigString("qobuz_app_id", "304027809") }

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
	
	appID := getAppID()
	//debugLog(fmt.Sprintf("🔨 AppID=%s, Token长度=%d, 使用FR_Token=%v", appID, len(token), useFrToken))

	headers := map[string]string{
		"X-App-Id":        appID,
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
	PublishDate  string     `json:"PublishDate"`
	PublishTime  int64      `json:"PublishTime"`
	PDFLink      string     `json:"PDFLink"`
	PDFName      string     `json:"PDFName"`
	Songs        []SongData `json:"Songs"`
}

type ArtistCache struct {
	Biography string `json:"bio"`
	ImageURL  string `json:"img"`
	Timestamp int64  `json:"ts"`
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

func getArtistRadioAPI(artistID string, limit, offset int) ([]byte, error) {
	if artistID == "" {
		debugLog("⚠️ [API 拦截] getArtistRadioAPI: 歌手 ID 为空，已拦截请求")
		return nil, fmt.Errorf("empty artist id")
	}

	url := fmt.Sprintf("%s/radio/artist?artist_id=%s&limit=%d&offset=%d", qobuzBaseURL, artistID, limit, offset)
	url = appendRegion(url, false)
	debugLog(fmt.Sprintf("🌐 [API 请求] 获取歌手电台 (Artist Radio): %s", url))
	
	resp, err := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: url, Headers: buildQobuzHeaders(false)})
	if err != nil { 
		debugLog(fmt.Sprintf("❌ [API 错误] 歌手电台网络连接失败: %v", err))
		return nil, err 
	}
	if resp.StatusCode != 200 {
		debugLog(fmt.Sprintf("❌ [API 错误] 歌手电台 HTTP=%d 响应=%s", resp.StatusCode, string(resp.Body)))
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	
	debugLog(fmt.Sprintf("✅ [API 成功] 歌手电台获取成功 (HTTP 200), 数据长度: %d bytes", len(resp.Body)))
	return resp.Body, nil
}

func getTrackRadioAPI(trackID string, limit, offset int) ([]byte, error) {
	if trackID == "" {
		debugLog("⚠️ [API 拦截] getTrackRadioAPI: 单曲 ID 为空，已拦截请求")
		return nil, fmt.Errorf("empty track id")
	}

	url := fmt.Sprintf("%s/radio/track?track_id=%s&limit=%d&offset=%d", qobuzBaseURL, trackID, limit, offset)
	url = appendRegion(url, false)
	debugLog(fmt.Sprintf("🌐 [API 请求] 获取单曲电台 (Track Radio): %s", url))
	
	resp, err := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: url, Headers: buildQobuzHeaders(false)})
	if err != nil { 
		debugLog(fmt.Sprintf("❌ [API 错误] 单曲电台网络连接失败: %v", err))
		return nil, err 
	}
	if resp.StatusCode != 200 {
		debugLog(fmt.Sprintf("❌ [API 错误] 单曲电台 HTTP=%d 响应=%s", resp.StatusCode, string(resp.Body)))
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	
	debugLog(fmt.Sprintf("✅ [API 成功] 单曲电台获取成功 (HTTP 200), 数据长度: %d bytes", len(resp.Body)))
	return resp.Body, nil
}

func resolveQobuzArtistID(artistName string) string {
	searchName := cleanArtistName(artistName)
	if searchName == "" { return "" }
	cacheKey := "qobuz_artist_id_" + cleanSearchTerm(searchName)
	if data, ok, _ := host.KVStoreGet(cacheKey); ok {
		return string(data)
	}

	sUrl := fmt.Sprintf("%s/catalog/search?query=%s&type=artists&limit=1", qobuzBaseURL, url.QueryEscape(searchName))
	sUrl = appendRegion(sUrl, false)
	debugLog(fmt.Sprintf("🌐 搜索歌手ID: %s", sUrl))
	
	sResp, err := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: sUrl, Headers: buildQobuzHeaders(false)})
	if err != nil {
		debugLog(fmt.Sprintf("❌ 搜索歌手网络报错: %v", err))
		return ""
	}
	if sResp.StatusCode != 200 {
		debugLog(fmt.Sprintf("❌ 搜索歌手 HTTP=%d 响应=%s", sResp.StatusCode, string(sResp.Body)))
		return ""
	}

	var sr struct { Artists struct { Items []struct { ID interface{} `json:"id"` } `json:"items"` } `json:"artists"` }
	json.Unmarshal(sResp.Body, &sr)
	if len(sr.Artists.Items) > 0 {
		idStr := fmt.Sprintf("%v", sr.Artists.Items[0].ID)
		host.KVStoreSet(cacheKey, []byte(idStr))
		return idStr
	}
	return ""
}

func resolveQobuzTrackID(title, artist string) string {
	query := cleanSearchTerm(title) + " " + cleanSearchTerm(artist)
	cacheKey := "qobuz_track_id_" + cleanSearchTerm(query)

	if data, ok, _ := host.KVStoreGet(cacheKey); ok { return string(data) }

	searchURL := fmt.Sprintf("%s/catalog/search?query=%s&type=tracks&limit=1", qobuzBaseURL, url.QueryEscape(query))
	searchURL = appendRegion(searchURL, false)
	debugLog(fmt.Sprintf("🌐 搜索单曲ID: %s", searchURL))

	resp, err := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: searchURL, Headers: buildQobuzHeaders(false)})
	if err != nil || resp.StatusCode != 200 { 
		debugLog(fmt.Sprintf("❌ 搜索单曲出错: HTTP=%d, Err=%v", resp.StatusCode, err))
		return "" 
	}

	var sr struct { Tracks struct { Items []struct { ID interface{} `json:"id"` } `json:"items"` } `json:"tracks"` }
	json.Unmarshal(resp.Body, &sr)

	if len(sr.Tracks.Items) > 0 {
		idStr := fmt.Sprintf("%v", sr.Tracks.Items[0].ID)
		host.KVStoreSet(cacheKey, []byte(idStr))
		return idStr
	}
	return ""
}

func fetchQobuzArtistInfo(artistName string) (string, string) {
	searchName := cleanArtistName(artistName)
	if searchName == "" { return "", "" }
	targetArtistID := resolveQobuzArtistID(searchName)
	if targetArtistID == "" { return "", "" }

	aUrl := fmt.Sprintf("%s/artist/get?artist_id=%s", qobuzBaseURL, targetArtistID)
	aUrl = appendRegion(aUrl, false)
	debugLog(fmt.Sprintf("🌐 获取歌手详情: %s", aUrl))

	aResp, err := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: aUrl, Headers: buildQobuzHeaders(false)})
	if err != nil || aResp.StatusCode != 200 { 
		debugLog(fmt.Sprintf("❌ 歌手详情出错: HTTP=%d, Err=%v", aResp.StatusCode, err))
		return "", "" 
	}

	var artDetail struct {
		Biography struct { Content string `json:"content"` } `json:"biography"`
		Image     struct { Large   string `json:"large"`   } `json:"image"`
	}
	json.Unmarshal(aResp.Body, &artDetail)
	return compactText(artDetail.Biography.Content), artDetail.Image.Large
}

func getCachedArtistInfo(artistName string) (string, string) {
	if cleanArtistName(artistName) == "" { return "", "" }
	cacheKey := "qobuz_artist_" + cleanSearchTerm(artistName)
	
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
		pdk.Log(pdk.LogInfo, fmt.Sprintf("⚠️ 歌手未知，降级使用专辑名搜索: [%s]", query))
	}
	
	searchURL := fmt.Sprintf("%s/catalog/search?query=%s&type=albums&limit=1", qobuzBaseURL, url.QueryEscape(query))
	searchURL = appendRegion(searchURL, false)
	respSearch, err := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: searchURL, Headers: buildQobuzHeaders(false)})
	
	if err != nil {
		pdk.Log(pdk.LogError, fmt.Sprintf("❌ Search HTTP Request Error: %v", err))
		return data, fmt.Errorf("search failed") 
	}
	if respSearch.StatusCode != 200 {
		pdk.Log(pdk.LogError, fmt.Sprintf("⏳ Search HTTP %d: %s", respSearch.StatusCode, string(respSearch.Body)))
		return data, fmt.Errorf("search failed") 
	}

	var sr struct { Albums struct { Items []struct { ID interface{} `json:"id"` } `json:"items"` } `json:"albums"` }
	if err := json.Unmarshal(respSearch.Body, &sr); err != nil {
		pdk.Log(pdk.LogError, fmt.Sprintf("❌ Search JSON Parse Error: %v", err))
		return data, fmt.Errorf("parse failed")
	}
	if len(sr.Albums.Items) == 0 { 
		pdk.Log(pdk.LogInfo, fmt.Sprintf("📄 Search returned 0 items for: %s", query))
		return data, fmt.Errorf("album not found") 
	}

	albumID := strings.ReplaceAll(fmt.Sprintf("%v", sr.Albums.Items[0].ID), "qobuz_", "")
	pdk.Log(pdk.LogInfo, fmt.Sprintf("✅ 匹配到专辑 ID: %s，正在请求详细数据...", albumID))

	data, err = getAlbumDetailByID(albumID, data, artistName, false)
	if err != nil { return data, err }

	needsFallback := false
	if data.PDFLink == "" || data.Description == "" {
		needsFallback = true
	}

	if needsFallback {
		if getFrToken() == "" {
			pdk.Log(pdk.LogInfo, "⏭️ 主区域数据不完整，但回退区 Token 为空，跳过跨区补全动作")
		} else {
			pdk.Log(pdk.LogInfo, "⚠️ 主区域数据不完整 (缺失 PDF 或 专辑介绍)，触发跨区补全...")
			frData, err := getAlbumDetailByID(albumID, AlbumData{}, artistName, true)
			if err == nil {
				if data.PDFLink == "" && frData.PDFLink != "" {
					data.PDFLink = frData.PDFLink
					data.PDFName = frData.PDFName
					pdk.Log(pdk.LogInfo, "✅ 成功从回退渠道补全 PDF 链接")
				}
				if data.Description == "" && frData.Description != "" {
					data.Description = frData.Description
					pdk.Log(pdk.LogInfo, "✅ 成功从回退渠道补全专辑介绍")
				}
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
		pdk.Log(pdk.LogInfo, "✅ 已成功将 PDF 标签合并入 Description")
	}

	return data, nil
}

func getAlbumDetailByID(albumID string, data AlbumData, fallbackArtist string, useFrToken bool) (AlbumData, error) {
	detailURL := fmt.Sprintf("%s/album/get?album_id=%s&offset=0&limit=50&extra=track_ids,albumsFromSameArtist", qobuzBaseURL, albumID)
	detailURL = appendRegion(detailURL, useFrToken)
	
	regionLog := "🌐 主区域"
	if useFrToken {
		regionLog = "🇫🇷 回退区"
	}
	pdk.Log(pdk.LogInfo, fmt.Sprintf("🚀 开始请求 %s 专辑详情 URL: %s", regionLog, detailURL))

	respAlbum, err := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: detailURL, Headers: buildQobuzHeaders(useFrToken)})
	
	if err != nil {
		pdk.Log(pdk.LogError, fmt.Sprintf("[Qobuz API] %s Detail HTTP Request Error: %v", regionLog, err))
		return data, fmt.Errorf("detail request failed") 
	}
	if respAlbum.StatusCode != 200 {
		pdk.Log(pdk.LogError, fmt.Sprintf("❌ %s Detail HTTP %d: %s", regionLog, respAlbum.StatusCode, string(respAlbum.Body)))
		return data, fmt.Errorf("detail request failed") 
	}

	var rawMap map[string]interface{}
	if err := json.Unmarshal(respAlbum.Body, &rawMap); err != nil {
		pdk.Log(pdk.LogError, fmt.Sprintf("❌ %s 基础解析失败: %v", regionLog, err))
	} else {
		var extractedDesc string
		
		if d, ok := rawMap["description"].(string); ok && strings.TrimSpace(d) != "" {
			extractedDesc = strings.TrimSpace(d)
			pdk.Log(pdk.LogInfo, fmt.Sprintf("✅ [%s] 成功从外层 description 提取专辑介绍", regionLog))
		} else {
			if facts, ok := rawMap["product_sales_facts"].(map[string]interface{}); ok {
				if ed, ok := facts["editorial"].(map[string]interface{}); ok {
					if d, ok := ed["description"].(string); ok && strings.TrimSpace(d) != "" {
						extractedDesc = strings.TrimSpace(d)
						pdk.Log(pdk.LogInfo, fmt.Sprintf("✅ [%s] 成功从 product_sales_facts 提取专辑介绍", regionLog))
					}
				}
			}
			
			if extractedDesc == "" {
				if factors, ok := rawMap["product_sales_factors"].(map[string]interface{}); ok {
					if ed, ok := factors["editorial"].(map[string]interface{}); ok {
						if d, ok := ed["description"].(string); ok && strings.TrimSpace(d) != "" {
							extractedDesc = strings.TrimSpace(d)
							pdk.Log(pdk.LogInfo, fmt.Sprintf("✅ [%s] 成功从 product_sales_factors 提取专辑介绍", regionLog))
						}
					}
				}
			}
		}

		if extractedDesc == "" {
			if catch, ok := rawMap["catchline"].(string); ok && strings.TrimSpace(catch) != "" {
				extractedDesc = catch
				pdk.Log(pdk.LogInfo, fmt.Sprintf("✅ [%s] 成功从 catchline 提取兜底介绍", regionLog))
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
						pdk.Log(pdk.LogInfo, fmt.Sprintf("✅ [%s] 成功匹配到 PDF", regionLog))
						break
					}
				}
			}
		}
		
		if pdfLink != "" {
			data.PDFLink = pdfLink
			data.PDFName = pdfName
		}
		
		pdk.Log(pdk.LogInfo, fmt.Sprintf("✅ [Qobuz %s] 解析完成 -> Description长度: %d, PDF链接: %s", regionLog, len(data.Description), data.PDFLink))
	}

	var detail struct {
		ID                  interface{} `json:"id"`
		Title               string `json:"title"`
		Label               struct { Name string `json:"name"` } `json:"label"`
		ReleasedAt          int64  `json:"released_at"`
		ReleaseDateOriginal string `json:"release_date_original"`
		Genre               struct { Name string `json:"name"` } `json:"genre"`
		Image               struct { Large string `json:"large"` } `json:"image"`
		Artist              struct { ID interface{} `json:"id"`; Name string `json:"name"` } `json:"artist"`
		Tracks              struct {
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
		pdk.Log(pdk.LogError, fmt.Sprintf("❌ 核心解析失败: %v", err))
		return data, fmt.Errorf("parse error")
	}

	if !useFrToken {
		data.AlbumID = fmt.Sprintf("%v", detail.ID)
		data.AlbumName = detail.Title
		data.PicURL = detail.Image.Large
		data.Company = detail.Label.Name
		data.PublishTime = detail.ReleasedAt * 1000
		data.PublishDate = detail.ReleaseDateOriginal

		targetArtistName := detail.Artist.Name
		if targetArtistName == "" && len(detail.Tracks.Items) > 0 { targetArtistName = detail.Tracks.Items[0].Performer.Name }
		if targetArtistName == "" { targetArtistName = fallbackArtist }
		
		bio, img := getCachedArtistInfo(targetArtistName)
		if img != "" { data.ArtistPicURL = img }
		if bio != "" { data.ArtistBio = bio } 

		for _, t := range detail.Tracks.Items {
			work := t.Work
			compName := t.Composer.Name
			workInfo := ""

			if work != "" {
				workInfo = work
				compId := fmt.Sprintf("%v", t.Composer.ID)
				if compName != "" {
					workInfo = fmt.Sprintf("%s (%s)", workInfo, compName)
					if compId != "0" && compId != "<nil>" && compId != "" { 
						workInfo = fmt.Sprintf("%s [ID:qobuz_%s]", workInfo, compId) 
					}
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