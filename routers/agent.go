package routers

import (
	"os"
	"fmt"
	"time"
	"math/rand"
	"strings"
	"net/url"
	"encoding/json"
	"path/filepath"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
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

var (
	globalLastSeedArtist  string
	globalLastTrackTitle  string
)

func Init() {
	agent := &qobuzAgent{}
	metadata.Register(agent)
	lyrics.Register(agent)
	scrobbler.Register(agent)
	
	rand.Seed(time.Now().UnixNano())

	InitQueue()
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

	EnqueueAlbumTask(input.Name, finalArtist)

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
	cleanSeed := cleanSearchTerm(input.Name)
	pdk.Log(pdk.LogInfo, fmt.Sprintf("🔍 开始为种子歌手 [%s] 挖掘官方推荐...", input.Name))
	
	Storage.SetRadioSeedLock(cleanSeed)

	var allSongs []RadioTrack
	seenMap := make(map[string]bool)

	if globalLastTrackTitle != "" && cleanSearchTerm(globalLastSeedArtist) == cleanSeed {
		trackID := resolveQobuzTrackID(globalLastTrackTitle, cleanSeed)
		if trackID != "" {
			for i := 0; i < 10; i++ {
				body, _ := getTrackRadioAPI(trackID, 30, i*30)
				songs := parseRadioResp(body)
				if len(songs) == 0 { break }
				for _, s := range songs {
					if !seenMap[s.ID] { 
						seenMap[s.ID] = true
						allSongs = append(allSongs, s) 
					}
				}
				if i < 9 { time.Sleep(600 * time.Millisecond) }
			}
		}
	}

	if len(allSongs) == 0 {
		artistID := resolveQobuzArtistID(cleanSeed)
		if artistID != "" {
			for i := 0; i < 10; i++ {
				body, _ := getArtistRadioAPI(artistID, 30, i*30)
				songs := parseRadioResp(body)
				if len(songs) == 0 { break }
				for _, s := range songs {
					if !seenMap[s.ID] { 
						seenMap[s.ID] = true
						allSongs = append(allSongs, s) 
					}
				}
				if i < 9 { time.Sleep(600 * time.Millisecond) }
			}
		}
	}

	if len(allSongs) == 0 {
		return doStandardSimilarArtists(cleanSeed)
	}

	var validArtistsPool []string
	tempMap := make(map[string][]metadata.SongRef)

	for _, s := range allSongs {
		artClean := cleanSearchTerm(s.ArtistName)
		if artClean == "" { continue }
		
		if artClean == cleanSeed {
			continue
		}

		tempMap[artClean] = append(tempMap[artClean], metadata.SongRef{ID: s.ID, Name: s.Title})
	}

	for art := range tempMap {
		validArtistsPool = append(validArtistsPool, art)
	}

	if len(validArtistsPool) == 0 {
		pdk.Log(pdk.LogWarn, "⚠️ 清洗后没有发现除本尊外的关联歌手，启动安全回退保护")
		validArtistsPool = append(validArtistsPool, "qobuz_vibe_fallback")
		tempMap["qobuz_vibe_fallback"] = []metadata.SongRef{}
	}

	rand.Shuffle(len(validArtistsPool), func(i, j int) {
		validArtistsPool[i], validArtistsPool[j] = validArtistsPool[j], validArtistsPool[i]
	})

	// 给 Navidrome 50 个艺人
	limitCount := 50
	if len(validArtistsPool) < limitCount {
		limitCount = len(validArtistsPool)
	}
	finalSelectedArtists := validArtistsPool[:limitCount]

	var similarResponseList []metadata.ArtistRef
	for _, art := range finalSelectedArtists {
		Storage.SetRadioTracks(art, tempMap[art])
		similarResponseList = append(similarResponseList, metadata.ArtistRef{ID: "qobuz_art_" + art, Name: art})
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf("🎉 成功清洗本尊，并随机洗牌抽取了 %d 位官方关联艺人", len(similarResponseList)))
	
	return &metadata.SimilarArtistsResponse{Artists: similarResponseList}, nil
}

func (a *qobuzAgent) GetArtistTopSongs(input metadata.TopSongsRequest) (*metadata.TopSongsResponse, error) {
	reqArtist := cleanSearchTerm(input.Name)

	if Storage.IsRadioSeedLocked(reqArtist) {
		pdk.Log(pdk.LogInfo, fmt.Sprintf("🚫 命中 60 秒保护期，拦截在生成电台时尝试混入种子艺人 [%s] 热门单曲的请求！", input.Name))
		return &metadata.TopSongsResponse{Songs: []metadata.SongRef{}}, nil
	}

	if poolTracks, exists := Storage.GetRadioTracks(reqArtist); exists {
		// 限制每个艺人只给 5 首歌曲！
		if len(poolTracks) > 5 {
			poolTracks = poolTracks[:5]
		}
		pdk.Log(pdk.LogInfo, fmt.Sprintf("📻 歌手 [%s] 释放 %d 首官方电台单曲...", input.Name, len(poolTracks)))
		return &metadata.TopSongsResponse{Songs: poolTracks}, nil
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf("👤 检测到独立访问，获取艺人 [%s] 的 100%% 纯净热门单曲...", input.Name))
	
	query := url.QueryEscape(reqArtist)
	searchURL := fmt.Sprintf("%s/catalog/search?query=%s&type=tracks&limit=50", qobuzBaseURL, query)
	searchURL = appendRegion(searchURL, false)

	resp, err := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: searchURL, Headers: buildQobuzHeaders(false)})
	if err != nil || resp.StatusCode != 200 { return nil, err }

	var raw struct {
		Tracks struct {
			Items []struct {
				ID        interface{} `json:"id"`
				Title     string      `json:"title"`
				Performer struct {
					Name string `json:"name"`
				} `json:"performer"`
			} `json:"items"`
		} `json:"tracks"`
	}

	if err := json.Unmarshal(resp.Body, &raw); err != nil { return nil, err }

	var songs []metadata.SongRef
	searchArtistLower := strings.ToLower(reqArtist)

	for _, t := range raw.Tracks.Items {
		if t.Title != "" && t.ID != nil {
			perfName := strings.ToLower(cleanSearchTerm(t.Performer.Name))
			if perfName == "" || strings.Contains(perfName, searchArtistLower) || strings.Contains(searchArtistLower, perfName) {
				songs = append(songs, metadata.SongRef{
					ID:   fmt.Sprintf("qobuz_song_%v", t.ID),
					Name: t.Title,
				})
			}
		}
	}
	
	return &metadata.TopSongsResponse{Songs: songs}, nil
}

func doStandardSimilarArtists(cleanSeed string) (*metadata.SimilarArtistsResponse, error) {
	artistID := resolveQobuzArtistID(cleanSeed)
	if artistID == "" { return nil, nil }

	simUrl := fmt.Sprintf("%s/artist/getSimilarArtists?artist_id=%s&limit=20", qobuzBaseURL, artistID)
	simUrl = appendRegion(simUrl, false)
	simResp, err := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: simUrl, Headers: buildQobuzHeaders(false)})
	if err != nil || simResp.StatusCode != 200 { return nil, nil }

	var sim struct { Artists struct { Items []struct { ID interface{} `json:"id"`; Name string `json:"name"` } `json:"items"` } `json:"albums"` }
	json.Unmarshal(simResp.Body, &sim)
	
	var res []metadata.ArtistRef
	for _, art := range sim.Artists.Items {
		if art.Name != "" {
			res = append(res, metadata.ArtistRef{ID: "qobuz_art_" + cleanSearchTerm(art.Name), Name: art.Name})
		}
	}
	return &metadata.SimilarArtistsResponse{Artists: res}, nil
}

func (a *qobuzAgent) GetLyrics(input lyrics.GetLyricsRequest) (lyrics.GetLyricsResponse, error) {
	debugLog(fmt.Sprintf("触发 GetLyrics: 歌曲=[%s]", input.Track.Title))
	if !getConfigBool("enable_lyrics", true) { return lyrics.GetLyricsResponse{}, nil }
	finalArtist, abs := getTrackArtistAndDir(getNavidromeUser(), input.Track.ID, input.Track.Artist, input.Track.Path)
	lyricText := fetchAndWriteLocalLyrics(input.Track.Title, finalArtist, abs)
	if lyricText == "" { return lyrics.GetLyricsResponse{}, nil }
	return lyrics.GetLyricsResponse{Lyrics: []lyrics.LyricsText{{Text: lyricText}}}, nil
}

func (a *qobuzAgent) IsAuthorized(_ scrobbler.IsAuthorizedRequest) (bool, error) { return true, nil }

func (a *qobuzAgent) PlaybackReport(req scrobbler.PlaybackReportRequest) error { return nil }

func (a *qobuzAgent) NowPlaying(req scrobbler.NowPlayingRequest) error {
	finalArtist, abs := getTrackArtistAndDir(req.Username, req.Track.ID, req.Track.Artist, req.Track.Path)
	globalLastTrackTitle = req.Track.Title
	globalLastSeedArtist = cleanSearchTerm(finalArtist)
	if abs != "" {
		albumDir := getCleanAlbumDir(abs)
		Storage.SaveAlbumPath(req.Track.Album, finalArtist, albumDir)
		fetchMetadataAndTag(abs, req.Track.Title, finalArtist, req.Track.Album)
	}
	return nil
}

func (a *qobuzAgent) Scrobble(req scrobbler.ScrobbleRequest) error {
	finalArtist, abs := getTrackArtistAndDir(req.Username, req.Track.ID, req.Track.Artist, req.Track.Path)
	if abs != "" {
		albumDir := getCleanAlbumDir(abs)
		Storage.SaveAlbumPath(req.Track.Album, finalArtist, albumDir)
		Storage.SaveArtistPath(finalArtist, filepath.Dir(albumDir))
		fetchMetadataAndTag(abs, req.Track.Title, finalArtist, req.Track.Album)
	}
	return nil
}

type RadioTrack struct {
	ID         string
	Title      string
	ArtistName string
}

func parseRadioResp(body []byte) []RadioTrack {
	var raw struct {
		Tracks struct {
			Items []struct {
				ID      interface{} `json:"id"`
				Title   string      `json:"title"`
				Artists []struct {
					Name string `json:"name"`
				} `json:"artists"`
				Performer struct {
					Name string `json:"name"`
				} `json:"performer"`
			} `json:"items"`
		} `json:"tracks"`
	}
	if err := json.Unmarshal(body, &raw); err != nil { return nil }
	var songs []RadioTrack
	for _, t := range raw.Tracks.Items {
		if t.Title != "" && t.ID != nil {
			artName := ""
			if len(t.Artists) > 0 { artName = t.Artists[0].Name }
			if artName == "" { artName = t.Performer.Name }
			if artName != "" {
				songs = append(songs, RadioTrack{ID: fmt.Sprintf("%v", t.ID), Title: t.Title, ArtistName: artName})
			}
		}
	}
	return songs
}