package routers

import (
	"io"
	"os"
	"fmt"
	"time"
	"strings"
	"net/url"
	"encoding/json"
	"path/filepath"

	"github.com/bogem/id3v2/v2"
	"github.com/go-flac/go-flac"
	"github.com/go-flac/flacvorbis"
	"github.com/go-flac/flacpicture"
	"github.com/Sorrow446/go-mp4tag"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
)

func cleanLyric(text string) string { return strings.TrimSpace(text) }

func getLocalAlbumData(albumDir string) (AlbumData, bool) {
	b, err := os.ReadFile(filepath.Join(albumDir, "qobuz_metadata.json"))
	if err == nil {
		var data AlbumData
		if err := json.Unmarshal(b, &data); err == nil && data.AlbumID != "" { return data, true }
	}
	return AlbumData{}, false
}

func saveLocalAlbumData(albumDir string, data AlbumData) {
	if !getConfigBool("enable_write_metadata", false) { return }
	b, _ := json.MarshalIndent(data, "", "  ")
	os.WriteFile(filepath.Join(albumDir, "qobuz_metadata.json"), b, 0666)
}

func isTrackProcessed(albumDir, filename string) bool {
	content, err := os.ReadFile(filepath.Join(albumDir, "qobuz_processed.txt"))
	if err != nil { return false }
	return strings.Contains(string(content), filename+"\n")
}

func markTrackProcessed(albumDir, filename string) {
	if !getConfigBool("enable_write_processed", false) { return }
	f, err := os.OpenFile(filepath.Join(albumDir, "qobuz_processed.txt"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err == nil {
		f.WriteString(filename + "\n")
		f.Close()
	}
}

func matchLocalFileToSong(filename string, songs []SongData) (SongData, bool) {
	for _, s := range songs {
		if fuzzyMatch(filename, s.Name) { return s, true }
	}
	return SongData{}, false
}

func downloadImage(urlStr, savePath string) {
	if urlStr == "" || savePath == "" { return }
	if stat, err := os.Stat(savePath); err == nil && stat.Size() > 1024 { return }
	debugLog(fmt.Sprintf("下载图片: %s -> %s", urlStr, savePath))
	resp, err := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: urlStr, Headers: map[string]string{"User-Agent": defaultUserAgent}})
	if err == nil && resp.StatusCode == 200 && len(resp.Body) > 1024 {
		os.WriteFile(savePath, resp.Body, 0666)
	} else {
		debugLog(fmt.Sprintf("下载图片失败或内容过小: 状态码=%v", resp.StatusCode))
	}
}

func downloadQobuzPDFToDisk(albumData AlbumData, saveDir string) {
	if !getConfigBool("enable_write_pdf", false) || albumData.PDFLink == "" || saveDir == "" { return }
	fileName := albumData.PDFName
	if fileName == "" { fileName = "booklet.pdf" }
	savePath := filepath.Join(saveDir, fileName)
	if stat, err := os.Stat(savePath); err == nil && stat.Size() > 1024 { return }
	
	debugLog(fmt.Sprintf("下载 PDF: %s", albumData.PDFLink))
	resp, err := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: albumData.PDFLink, Headers: map[string]string{"User-Agent": defaultUserAgent}})
	if err == nil && resp.StatusCode == 200 { os.WriteFile(savePath, resp.Body, 0666) }
}

func fetchAndWriteLocalLyrics(title, artist, absolutePath string) string {
	if absolutePath == "" { return "" }
	saveDir := filepath.Dir(absolutePath)
	ext := filepath.Ext(absolutePath)
	baseName := strings.TrimSuffix(filepath.Base(absolutePath), ext)
	lrcPath := filepath.Join(saveDir, baseName+".lrc")

	if content, err := os.ReadFile(lrcPath); err == nil { return string(content) }

	var lrcText string
	customAPI := getConfigString("lyrics_api_url", "")
	if customAPI != "" {
		debugLog(fmt.Sprintf("请求自定义歌词 API: title=%s artist=%s", title, artist))
		customAPI = strings.TrimRight(customAPI, "/")
		apiURL := fmt.Sprintf("%s/api/lyric?title=%s&artist=%s", customAPI, url.QueryEscape(title), url.QueryEscape(artist))
		resp, err := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: apiURL, Headers: map[string]string{"User-Agent": defaultUserAgent}})
		if err == nil && resp.StatusCode == 200 {
			var customResp struct { Lrc interface{} `json:"lrc"` }
			if errParse := json.Unmarshal(resp.Body, &customResp); errParse == nil {
				if s, ok := customResp.Lrc.(string); ok { lrcText = s } else if m, ok := customResp.Lrc.(map[string]interface{}); ok { if l, ok := m["lyric"].(string); ok { lrcText = l } }
			}
		}
	}
	
	lrcText = cleanLyric(lrcText)
	if lrcText != "" && getConfigBool("enable_write_lyrics", false) {
		os.WriteFile(lrcPath, []byte(lrcText), 0666)
		debugLog(fmt.Sprintf("已将歌词写入硬盘: %s", lrcPath))
	}
	return lrcText
}

func downloadPDF(urlStr, folderPath, originalName string) {
	if urlStr == "" || folderPath == "" { return }
	fileName := originalName
	if fileName == "" { fileName = "booklet.pdf" }
	fullPath := filepath.Join(folderPath, fileName)
	if stat, err := os.Stat(fullPath); err == nil && stat.Size() > 1024 { return } 
	
	resp, err := host.HTTPSend(host.HTTPRequest{Method: "GET", URL: urlStr, Headers: map[string]string{"User-Agent": defaultUserAgent}})
	if err == nil && resp.StatusCode == 200 { os.WriteFile(fullPath, resp.Body, 0666) }
}

func fetchMetadataAndTag(absPath, title, artist, album string) {
	albumDir := getCleanAlbumDir(absPath)
	fileName := filepath.Base(absPath)
	ext := filepath.Ext(absPath)
	
	if isTrackProcessed(albumDir, fileName) {
		debugLog(fmt.Sprintf("文件已处理过，跳过标签写入: %s", fileName))
		return 
	}

	debugLog(fmt.Sprintf("准备进行文件刮削: %s", absPath))
	albumData := fetchAndCacheAlbum("", album, artist, albumDir)
	if albumData.AlbumID == "" { return }

	matchedSong, foundSong := matchLocalFileToSong(fileName, albumData.Songs)
	if !foundSong {
		debugLog(fmt.Sprintf("⚠️ 无法将本地文件与 API 返回曲目匹配: %s", fileName))
		return 
	}

	lyricText := fetchAndWriteLocalLyrics(matchedSong.Name, artist, absPath)
	
	if !getConfigBool("enable_write_tags", false) {
		return
	}

	var picData []byte
	if getConfigBool("enable_write_cover_image", true) { picData, _ = os.ReadFile(filepath.Join(albumDir, "cover.jpg")) }

	finalComment := albumData.Description
	publishDate := ""
	if albumData.PublishTime > 0 { publishDate = time.Unix(albumData.PublishTime/1000, 0).UTC().Format("2006-01-02") + "T00:00:00Z" }

	debugLog(fmt.Sprintf("写入标签... 艺人=%s 专辑=%s", strings.Join(matchedSong.Artists, "/"), albumData.AlbumName))
	if writeTags(absPath, ext, matchedSong, albumData, publishDate, finalComment, finalComment, lyricText, picData) {
		markTrackProcessed(albumDir, fileName)
		debugLog("写入成功!")
	} else {
		debugLog("写入失败!")
	}
}

func cleanFlacFile(absPath string) error {
	file, err := os.Open(absPath)
	if err != nil { return err }
	header := make([]byte, 10)
	if _, err := file.Read(header); err != nil { file.Close(); return err }
	if string(header[0:3]) != "ID3" { file.Close(); return fmt.Errorf("未检测到 ID3 头部") }
	size := (int(header[6]) << 21) | (int(header[7]) << 14) | (int(header[8]) << 7) | int(header[9])
	totalSize := int64(size + 10)
	tempPath := absPath + ".tmp_fix"
	tempFile, err := os.Create(tempPath)
	if err != nil { file.Close(); return err }
	file.Seek(totalSize, 0)
	_, err = io.Copy(tempFile, file)
	tempFile.Close(); file.Close()
	if err != nil { os.Remove(tempPath); return err }
	return os.Rename(tempPath, absPath)
}

func writeTags(absPath, ext string, song SongData, album AlbumData, publishDate, comment, description, lyric string, picData []byte) bool {
	filename := filepath.Base(absPath)
	artistStr := strings.Join(song.Artists, "/")

	defer func() {
		if r := recover(); r != nil {
			pdk.Log(pdk.LogError, fmt.Sprintf("🚫 致命崩溃拦截 (%s): %v", filename, r))
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
		
		if publishDate != "" && tag.Year() != publishDate { 
			tag.SetYear(publishDate)
			tag.DeleteFrames("TDRC") 
			tag.AddTextFrame("TDRC", id3v2.EncodingUTF8, publishDate)
			changed = true 
		}

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
			if err := tag.Save(); err != nil {
				pdk.Log(pdk.LogError, fmt.Sprintf("❌ MP3 写入物理文件失败: %v", err))
				return false
			}
			pdk.Log(pdk.LogInfo, fmt.Sprintf("✅ 成功写入 MP3 标签: %s", filename))
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
		
		if publishDate != "" {
			d, err := cmt.Get("DATE")
			if err != nil || len(d) == 0 || d[0] != publishDate {
				var newComments []string
				for _, c := range cmt.Comments {
					if !strings.HasPrefix(strings.ToUpper(c), "DATE=") {
						newComments = append(newComments, c)
					}
				}
				cmt.Comments = newComments
				cmt.Add("DATE", publishDate)
				changed = true
			}
		}

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
			
			if err := f.Save(tempPath); err != nil { 
				os.Remove(tempPath)
				pdk.Log(pdk.LogError, fmt.Sprintf("❌ FLAC 写入临时文件失败 (可能磁盘满或无权限): %v", err))
				return false 
			}
			
			if err := os.Rename(tempPath, absPath); err != nil {
				os.Remove(tempPath)
				pdk.Log(pdk.LogError, fmt.Sprintf("❌ FLAC 替换原文件失败 (可能被播放器锁定占用): %v", err))
				return false
			}
			pdk.Log(pdk.LogInfo, fmt.Sprintf("✅ 成功写入 FLAC 标签: %s", filename))
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
		
		if publishDate != "" && tags.Date != publishDate { 
			tags.Date = publishDate
			changed = true 
		}

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
			if err := mp4.Write(tags, []string{}); err != nil { 
				pdk.Log(pdk.LogError, fmt.Sprintf("❌ M4A 写入物理文件失败: %v", err))
				return false 
			}
			pdk.Log(pdk.LogInfo, fmt.Sprintf("✅ 成功写入 M4A 标签: %s", filename))
		}
		return true
	}
	return false
}