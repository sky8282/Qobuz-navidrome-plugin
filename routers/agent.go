package routers

import (
	"os"
	"fmt"
	"time"
	"strings"
	"net/url"
	"encoding/json"
	"path/filepath"

	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/navidrome/navidrome/plugins/pdk/go/metadata"
	"github.com/navidrome/navidrome/plugins/pdk/go/scrobbler"
)

type qobuzAgent struct{}

var (
	_ metadata.ArtistBiographyProvider = (*qobuzAgent)(nil)
	_ metadata.ArtistImagesProvider    = (*qobuzAgent)(nil)
	_ metadata.AlbumImagesProvider     = (*qobuzAgent)(nil)
	_ metadata.AlbumInfoProvider       = (*qobuzAgent)(nil)
	_ metadata.SimilarArtistsProvider  = (*qobuzAgent)(nil)
	_ metadata.ArtistTopSongsProvider  = (*qobuzAgent)(nil)
	_ lyrics.Lyrics                    = (*qobuzAgent)(nil)
	_ scrobbler.Scrobbler              = (*qobuzAgent)(nil)
)

func Init() {
	agent := &qobuzAgent{}
	metadata.Register(agent)
	lyrics.Register(agent)
	scrobbler.Register(agent)

	//pdk.Log(pdk.LogInfo, "===============================================")
	//pdk.Log(pdk.LogInfo, "🔥 Qobuz Plugin (Modular) Started 🔥 ")
	//pdk.Log(pdk.LogInfo, "===============================================")
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

func getConfigInt(key string, defaultVal int) int {
	val, ok := pdk.GetConfig(key)
	if !ok || val == "" { return defaultVal }
	var i int
	if _, err := fmt.Sscanf(val, "%d", &i); err != nil { return defaultVal }
	return i
}

func debugLog(msg string) {
	if getConfigBool("enable_debug_log", true) {
		pdk.Log(pdk.LogInfo, "ℹ️  "+msg)
	}
}

func getNavidromeUser() string { return getConfigString("navidrome_user", "admin") }

type CacheWrapper struct {
	Timestamp int64           `json:"ts"`
	Payload   json.RawMessage `json:"payload"`
}

func cacheSet(key string, data []byte) {
	wrap := CacheWrapper{Timestamp: time.Now().Unix(), Payload: data}
	b, _ := json.Marshal(wrap)
	host.KVStoreSet(key, b)
}

func cacheGet(key string) ([]byte, bool) {
	b, ok, _ := host.KVStoreGet(key)
	if !ok { return nil, false }
	var wrap CacheWrapper
	if err := json.Unmarshal(b, &wrap); err == nil && wrap.Timestamp > 0 {
		days := getConfigInt("cache_days", 180)
		if time.Now().Unix()-wrap.Timestamp > int64(days*86400) { return nil, false }
		return wrap.Payload, true
	}
	return b, true
}

func (a *qobuzAgent) GetAlbumInfo(input metadata.AlbumRequest) (*metadata.AlbumInfoResponse, error) {
	debugLog(fmt.Sprintf("触发 GetAlbumInfo: 专辑=[%s], 歌手=[%s]", input.Name, input.Artist))
	finalArtist := cleanArtistName(input.Artist)
	albumDir := resolveAlbumDir(input.Name, finalArtist)
	
	if albumDir != "" {
		if data, found := getLocalAlbumData(albumDir); found && data.Description != "" {
			debugLog("GetAlbumInfo: 命中本地元数据缓存")
			desc := strings.ReplaceAll(data.Description, "\n", "<br>")
			return &metadata.AlbumInfoResponse{Description: desc}, nil
		}
	}

	fetchedData := fetchAndCacheAlbum("", input.Name, finalArtist, albumDir)
	if fetchedData.AlbumID != "" && fetchedData.Description != "" {
		debugLog("GetAlbumInfo: 成功从 Qobuz 获取到介绍")
		desc := strings.ReplaceAll(fetchedData.Description, "\n", "<br>")
		return &metadata.AlbumInfoResponse{Description: desc}, nil
	}
	debugLog("GetAlbumInfo: 未能获取到介绍")
	return nil, nil
}

func (a *qobuzAgent) GetAlbumImages(input metadata.AlbumRequest) (*metadata.AlbumImagesResponse, error) {
	debugLog(fmt.Sprintf("触发 GetAlbumImages: 专辑=[%s]", input.Name))
	finalArtist := cleanArtistName(input.Artist)
	albumDir := resolveAlbumDir(input.Name, finalArtist)
	
	if albumDir != "" {
		coverPath := filepath.Join(albumDir, "cover.jpg")
		if stat, err := os.Stat(coverPath); err == nil && stat.Size() > 1024 {
			debugLog(fmt.Sprintf("GetAlbumImages: 本地封面已存在，中止请求 (%s)", coverPath))
			return nil, nil
		}
	}

	fetchedData := fetchAndCacheAlbum("", input.Name, finalArtist, albumDir)
	if fetchedData.PicURL != "" {
		debugLog(fmt.Sprintf("GetAlbumImages: 返回封面链接 %s", fetchedData.PicURL))
		return &metadata.AlbumImagesResponse{Images: []metadata.ImageInfo{{URL: fetchedData.PicURL, Size: 1200}}}, nil
	}
	return nil, nil
}

func (a *qobuzAgent) GetArtistImages(input metadata.ArtistRequest) (*metadata.ArtistImagesResponse, error) {
	debugLog(fmt.Sprintf("触发 GetArtistImages: 歌手=[%s]", input.Name))
	artistDir := guessArtistPath(input.Name)
	if artistDir != "" {
		artistImgPath := filepath.Join(artistDir, "artist.jpg")
		if stat, err := os.Stat(artistImgPath); err == nil && stat.Size() > 1024 {
			debugLog(fmt.Sprintf("GetArtistImages: 本地头像已存在，直接读取 (%s)", artistImgPath))
			return nil, nil
		}
	}

	_, img := getCachedArtistInfo(input.Name)
	if img != "" {
		debugLog(fmt.Sprintf("GetArtistImages: 返回头像链接 %s", img))
		if getConfigBool("enable_write_artist_image", true) && artistDir != "" {
			downloadImage(img, filepath.Join(artistDir, "artist.jpg"))
		}
		if getConfigBool("enable_write_global_artist_image", true) {
			saveGlobalArtistImage(input.Name, img)
		}
		return &metadata.ArtistImagesResponse{Images: []metadata.ImageInfo{{URL: img, Size: 1200}}}, nil
	}
	return nil, nil
}

func (a *qobuzAgent) GetArtistBiography(input metadata.ArtistRequest) (*metadata.ArtistBiographyResponse, error) {
	debugLog(fmt.Sprintf("触发 GetArtistBiography: 歌手=[%s]", input.Name))
	bio, _ := getCachedArtistInfo(input.Name)
	if bio != "" {
		bio = strings.ReplaceAll(bio, "\n", "<br>")
		return &metadata.ArtistBiographyResponse{Biography: bio}, nil
	}
	return nil, nil
}

func (a *qobuzAgent) GetSimilarArtists(input metadata.SimilarArtistsRequest) (*metadata.SimilarArtistsResponse, error) {
	debugLog(fmt.Sprintf("触发 GetSimilarArtists: 歌手=[%s]", input.Name))
	cleanName := cleanSearchTerm(input.Name)
	cleanName = strings.TrimPrefix(cleanName, "similar-")
	
	simUrl := fmt.Sprintf("%s/catalog/search?query=%s&type=artists&limit=1", qobuzBaseURL, url.QueryEscape(cleanName))
	simUrl = appendRegion(simUrl, false)
	sResp, err := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: simUrl, Headers: buildQobuzHeaders(false)})
	if err != nil || sResp.StatusCode != 200 { return nil, nil }

	var sr struct { Artists struct { Items []struct { ID interface{} `json:"id"` } `json:"items"` } `json:"artists"` }
	json.Unmarshal(sResp.Body, &sr)
	if len(sr.Artists.Items) == 0 { return nil, nil }
	
	targetArtistID := fmt.Sprintf("%v", sr.Artists.Items[0].ID)
	if targetArtistID == "0" || targetArtistID == "<nil>" || targetArtistID == "" { return nil, nil }

	simUrl = fmt.Sprintf("%s/artist/getSimilarArtists?artist_id=%s&limit=20", qobuzBaseURL, targetArtistID)
	simUrl = appendRegion(simUrl, false)
	simResp, err := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: simUrl, Headers: buildQobuzHeaders(false)})
	if err != nil || simResp.StatusCode != 200 { return nil, nil }

	var sim struct { Artists struct { Items []struct { ID interface{} `json:"id"`; Name string `json:"name"` } `json:"items"` } `json:"artists"` }
	json.Unmarshal(simResp.Body, &sim)
	
	var res []metadata.ArtistRef
	for _, art := range sim.Artists.Items {
		if art.Name != "" {
			res = append(res, metadata.ArtistRef{ID: fmt.Sprintf("qobuz_art_%v", art.ID), Name: art.Name})
		}
	}
	debugLog(fmt.Sprintf("GetSimilarArtists: 成功获取相似艺人数量 = %d", len(res)))
	return &metadata.SimilarArtistsResponse{Artists: res}, nil
}

func (a *qobuzAgent) GetArtistTopSongs(input metadata.TopSongsRequest) (*metadata.TopSongsResponse, error) {
	debugLog(fmt.Sprintf("▶️ [触发动作] GetArtistTopSongs (歌手电台): 目标歌手=[%s]", input.Name))
	
	artistID := resolveQobuzArtistID(input.Name)
	if artistID == "" { 
		debugLog(fmt.Sprintf("⚠️ [数据缺失] GetArtistTopSongs: 搜索未能解析到歌手 [%s] 的 Qobuz ID，终止获取，返回空", input.Name))
		return nil, nil 
	}
	
	debugLog(fmt.Sprintf("🔍 [解析成功] GetArtistTopSongs: 歌手 [%s] 对应的 ID=[%s]，准备请求电台数据", input.Name, artistID))
	
	body, err := getArtistRadioAPI(artistID, 50, 0)
	if err != nil { 
		debugLog(fmt.Sprintf("❌ [业务中断] GetArtistTopSongs: 核心获取流程报错, Err=%v", err))
		return nil, err 
	}
	
	var raw struct { 
		Tracks struct { 
			Items []struct { 
				ID interface{} `json:"id"`
				Title string `json:"title"` 
			} `json:"items"` 
		} `json:"tracks"` 
	}
	
	if err := json.Unmarshal(body, &raw); err != nil {
		debugLog(fmt.Sprintf("❌ [JSON 解析] GetArtistTopSongs: 电台数据反序列化失败: %v", err))
		return nil, err
	}
	
	var songs []metadata.SongRef
	for _, t := range raw.Tracks.Items {
		if t.Title != "" && t.ID != nil {
			songs = append(songs, metadata.SongRef{
				ID:   fmt.Sprintf("qobuz_song_%v", t.ID), 
				Name: t.Title,
			})
		}
	}
	
	if len(songs) == 0 {
		debugLog(fmt.Sprintf("⚠️ [数据为空] GetArtistTopSongs: 歌手 [%s] 的电台返回了空列表，不填充数据", input.Name))
		return nil, nil
	}
	
	debugLog(fmt.Sprintf("🎉 [处理完成] GetArtistTopSongs: 成功获取歌手 [%s] 的电台歌曲，共计 %d 首入库", input.Name, len(songs)))
	return &metadata.TopSongsResponse{Songs: songs}, nil
}

func (a *qobuzAgent) GetLyrics(input lyrics.GetLyricsRequest) (lyrics.GetLyricsResponse, error) {
	debugLog(fmt.Sprintf("触发 GetLyrics: 歌曲=[%s]", input.Track.Title))
	if !getConfigBool("enable_lyrics", true) { return lyrics.GetLyricsResponse{}, nil }
	
	finalArtist, abs := getTrackArtistAndDir(getNavidromeUser(), input.Track.ID, input.Track.Artist, input.Track.Path)
	lyricText := fetchAndWriteLocalLyrics(input.Track.Title, finalArtist, abs)
	if lyricText == "" { return lyrics.GetLyricsResponse{}, nil }
	
	debugLog("GetLyrics: 成功向播放器返回歌词")
	return lyrics.GetLyricsResponse{Lyrics: []lyrics.LyricsText{{Text: lyricText}}}, nil
}

func (a *qobuzAgent) IsAuthorized(_ scrobbler.IsAuthorizedRequest) (bool, error) { return true, nil }

func (a *qobuzAgent) PlaybackReport(req scrobbler.PlaybackReportRequest) error { return nil }

func (a *qobuzAgent) NowPlaying(req scrobbler.NowPlayingRequest) error {
	debugLog(fmt.Sprintf("▶️ NowPlaying 动作触发: 歌曲=[%s], 绝对路径=[%s]", req.Track.Title, req.Track.Path))
	finalArtist, abs := getTrackArtistAndDir(req.Username, req.Track.ID, req.Track.Artist, req.Track.Path)
	if abs != "" {
		albumDir := getCleanAlbumDir(abs)
		debugLog(fmt.Sprintf("NowPlaying 解析得到物理目录: %s", albumDir))
		
		cacheKey := fmt.Sprintf("path_album_%s_%s", cleanSearchTerm(req.Track.Album), cleanSearchTerm(finalArtist))
		host.KVStoreSet(cacheKey, []byte(albumDir))
		fetchMetadataAndTag(abs, req.Track.Title, finalArtist, req.Track.Album)
	} else {
		debugLog("⚠️ NowPlaying 无法获取到绝对路径，中断搜刮")
	}
	return nil
}

func (a *qobuzAgent) Scrobble(req scrobbler.ScrobbleRequest) error {
	debugLog(fmt.Sprintf("✅ Scrobble (上报/打卡) 触发: 歌曲=[%s]", req.Track.Title))
	finalArtist, abs := getTrackArtistAndDir(req.Username, req.Track.ID, req.Track.Artist, req.Track.Path)
	if abs != "" {
		albumDir := getCleanAlbumDir(abs)
		
		cacheKey := fmt.Sprintf("path_album_%s_%s", cleanSearchTerm(req.Track.Album), cleanSearchTerm(finalArtist))
		host.KVStoreSet(cacheKey, []byte(albumDir))
		
		artistCacheKey := fmt.Sprintf("path_artist_%s", cleanSearchTerm(finalArtist))
		host.KVStoreSet(artistCacheKey, []byte(filepath.Dir(albumDir)))

		fetchMetadataAndTag(abs, req.Track.Title, finalArtist, req.Track.Album)
	}
	return nil
}