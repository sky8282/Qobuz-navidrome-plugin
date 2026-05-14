package routers

import (
	"os"
	"fmt"
	"errors"
	"regexp"
	"strings"
	"net/url"
	"encoding/json"
	"path/filepath"

	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	
)

var reDiscFolder = regexp.MustCompile(`^(?i)(cd|disc|disk|vol|volume)[\s\._-]*\d+$`)
var errWalkStop = errors.New("stop walk")

type subsonicAlbumResponse struct {
	SubsonicResponse struct {
		Album struct {
			Song []struct {
				ID          string `json:"id"`
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

func getCleanAlbumDir(absPath string) string {
	albDir := filepath.Dir(absPath)
	base := strings.ToLower(filepath.Base(albDir))
	if reDiscFolder.MatchString(base) || base == "cd" || base == "disc" || base == "disk" {
		return filepath.Dir(albDir)
	}
	return albDir
}

func getBaseMusicDir() string {
	libraries, err := host.LibraryGetAllLibraries()
	if err == nil && len(libraries) > 0 {
		for _, lib := range libraries {
			root := lib.MountPoint
			if root == "" { root = lib.Path }
			if root != "" { return root }
		}
	}
	return ""
}

func saveGlobalArtistImage(artistName string, picURL string) {
	if !getConfigBool("enable_write_global_artist_image", true) {
		return
	}
	if picURL == "" || artistName == "" {
		return
	}
	
	baseDir := getBaseMusicDir()
	if baseDir == "" {
		pdk.Log(pdk.LogError, "❌ 无法获取媒体库根目录，跳过写入全局头像")
		return
	}

	artistFolder := filepath.Join(baseDir, "artist")
	if err := os.MkdirAll(artistFolder, 0755); err != nil {
		pdk.Log(pdk.LogError, fmt.Sprintf("❌ 创建全局 artist 目录失败 (可能没有对根目录的写权限): %v", err))
		return
	}

	safeArtistName := strings.ReplaceAll(strings.ReplaceAll(artistName, "/", "_"), "\\", "_")
	savePath := filepath.Join(artistFolder, safeArtistName+".jpg")

	if stat, err := os.Stat(savePath); err == nil && stat.Size() > 1024 {
	    pdk.Log(pdk.LogInfo, fmt.Sprintf("⏭️ 全局歌手头像已存在: %s", savePath))
		return
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf("📸 准备下载并写入全局歌手头像到: %s", savePath))

	resp, err := host.HTTPSend(host.HTTPRequest{
		Method:  "GET", 
		URL:     picURL, 
		Headers: buildQobuzHeaders(false),
	})
	
	if err != nil {
		pdk.Log(pdk.LogError, fmt.Sprintf("❌ 全局头像下载网络失败: %v", err))
		return
	}
	if resp.StatusCode != 200 {
		pdk.Log(pdk.LogError, fmt.Sprintf("❌ 全局头像下载失败 HTTP %d", resp.StatusCode))
		return
	}

	if err := os.WriteFile(savePath, resp.Body, 0666); err != nil {
		pdk.Log(pdk.LogError, fmt.Sprintf("❌ 写入全局头像文件失败 (请检查 [%s] 的写入权限!): %v", artistFolder, err))
	} else {
		pdk.Log(pdk.LogInfo, fmt.Sprintf("✅ 成功保存全局歌手头像: %s", savePath))
	}
}

func resolveAbsolutePath(relPath, suffix string, size int64) (string, error) {
	if relPath == "" { return "", fmt.Errorf("empty path") }
	if filepath.IsAbs(relPath) {
		if stat, err := os.Stat(relPath); err == nil && !stat.IsDir() {
			return relPath, nil
		}
	}

	libraries, err := host.LibraryGetAllLibraries()
	if err != nil { return relPath, err }

	for _, lib := range libraries {
		root := lib.MountPoint
		if root == "" { root = lib.Path }
		if root == "" { continue }

		fullPath := filepath.Join(root, relPath)
		if stat, err := os.Stat(fullPath); err == nil && !stat.IsDir() {
			return fullPath, nil
		}

		baseName := filepath.Base(relPath)
		foundPath := ""
		filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil { return nil }
			if !info.IsDir() && info.Name() == baseName {
				if size > 0 {
					if info.Size() == size {
						foundPath = path
						return errWalkStop
					}
				} else {
					foundPath = path
					return errWalkStop
				}
			}
			return nil
		})

		if foundPath != "" { return foundPath, nil }
	}
	return relPath, fmt.Errorf("not found")
}

func resolveFromRelativePath(relPath string) string {
	abs, _ := resolveAbsolutePath(relPath, "", 0)
	return abs
}

func getSongDetailsFromSubsonic(username, trackID string) (*subsonicSongResponse, error) {
	if username == "" { username = getNavidromeUser() }
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

	if abs == "" || !filepath.IsAbs(abs) {
		abs = resolveFromRelativePath(fallbackPath)
	}

	if finalArtist == "" { finalArtist = cleanArtistName(trackArtist) }
	
	if finalArtist == "" && abs != "" && filepath.IsAbs(abs) {
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
		
		entries, err := os.ReadDir(root)
		if err == nil {
			for _, entry := range entries {
				if entry.IsDir() && fuzzyMatch(entry.Name(), cleanArtist) {
					return filepath.Join(root, entry.Name())
				}
			}
		}
	}
	return ""
}

func findAlbumDirViaSubsonicAPI(albumName, artistName string) string {
	if albumName == "" { return "" }
	
	queries := []string{
		cleanSearchTerm(albumName) + " " + cleanSearchTerm(artistName),
		cleanSearchTerm(albumName),
		albumName,
	}

	for _, q := range queries {
		if strings.TrimSpace(q) == "" { continue }
		query := url.QueryEscape(q)
		jsonStr, err := host.SubsonicAPICall(fmt.Sprintf("search3?query=%s&albumCount=10&u=%s&f=json&v=1.16.0", query, getNavidromeUser()))
		if err != nil { continue }
		
		var resp struct {
			SubsonicResponse struct {
				SearchResult3 struct {
					Album []struct { ID, Name string } `json:"album"`
				} `json:"searchResult3"`
			} `json:"subsonic-response"`
		}
		json.Unmarshal([]byte(jsonStr), &resp)
		
		for _, alb := range resp.SubsonicResponse.SearchResult3.Album {
			if fuzzyMatch(alb.Name, albumName) {
				jsonAlStr, err := host.SubsonicAPICall("getAlbum?id=" + alb.ID + "&u=" + getNavidromeUser() + "&f=json&v=1.16.0")
				if err == nil {
					var alResp subsonicAlbumResponse
					json.Unmarshal([]byte(jsonAlStr), &alResp)
					if len(alResp.SubsonicResponse.Album.Song) > 0 {
						song := alResp.SubsonicResponse.Album.Song[0]
						abs, _ := resolveAbsolutePath(song.Path, song.Suffix, song.Size)
						if abs != "" && filepath.IsAbs(abs) { 
							return getCleanAlbumDir(abs)
						}
					}
				}
			}
		}
	}
	return ""
}

func guessAlbumDir(albumName, artistName string) string {
	dir := findAlbumDirViaSubsonicAPI(albumName, artistName)
	if dir != "" { return dir }

	artistDir := guessArtistPath(artistName)
	if artistDir != "" {
		entries, err := os.ReadDir(artistDir)
		if err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					if fuzzyMatch(entry.Name(), albumName) {
						return filepath.Join(artistDir, entry.Name())
					}
				}
			}
		}
	}
	return ""
}

func resolveAlbumDir(albumName, artistName string) string {
	finalArtist := cleanArtistName(artistName)
	cacheKey := fmt.Sprintf("path_album_%s_%s", cleanSearchTerm(albumName), cleanSearchTerm(finalArtist))
	
	if data, ok, _ := host.KVStoreGet(cacheKey); ok {
		dir := string(data)
		if stat, err := os.Stat(dir); err == nil && stat.IsDir() { return dir }
	}
	
	dir := guessAlbumDir(albumName, finalArtist)
	if dir != "" { host.KVStoreSet(cacheKey, []byte(dir)) }
	return dir
}

func resolveArtistDir(artistName string) string {
	finalArtist := cleanArtistName(artistName)
	if finalArtist == "" { return "" }
	cacheKey := fmt.Sprintf("path_artist_%s", cleanSearchTerm(finalArtist))

	if data, ok, _ := host.KVStoreGet(cacheKey); ok {
		dir := string(data)
		if stat, err := os.Stat(dir); err == nil && stat.IsDir() { return dir }
	}

	dir := guessArtistPath(finalArtist)
	if dir != "" { host.KVStoreSet(cacheKey, []byte(dir)) }
	return dir
}