package routers

import (
	"fmt"
	"time"
	"encoding/json"
	
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/metadata"
)

type DB struct{}

var Storage = &DB{}

type CacheWrapper struct {
	Timestamp int64           `json:"ts"`
	Payload   json.RawMessage `json:"payload"`
}

func (db *DB) SaveAlbumPath(albumName, artistName, dir string) {
	cacheKey := fmt.Sprintf("path_album_%s_%s", cleanSearchTerm(albumName), cleanSearchTerm(artistName))
	host.KVStoreSet(cacheKey, []byte(dir))
}

func (db *DB) GetAlbumPath(albumName, artistName string) (string, bool) {
	cacheKey := fmt.Sprintf("path_album_%s_%s", cleanSearchTerm(albumName), cleanSearchTerm(artistName))
	if data, ok, _ := host.KVStoreGet(cacheKey); ok {
		return string(data), true
	}
	return "", false
}

func (db *DB) SaveArtistPath(artistName, dir string) {
	cacheKey := fmt.Sprintf("path_artist_%s", cleanSearchTerm(artistName))
	host.KVStoreSet(cacheKey, []byte(dir))
}

func (db *DB) GetArtistPath(artistName string) (string, bool) {
	cacheKey := fmt.Sprintf("path_artist_%s", cleanSearchTerm(artistName))
	if data, ok, _ := host.KVStoreGet(cacheKey); ok {
		return string(data), true
	}
	return "", false
}

func (db *DB) SetCache(key string, data []byte) {
	wrap := CacheWrapper{
		Timestamp: time.Now().Unix(),
		Payload:   data,
	}
	b, _ := json.Marshal(wrap)
	host.KVStoreSet(key, b)
}

func (db *DB) GetCache(key string) ([]byte, bool) {
	b, ok, _ := host.KVStoreGet(key)
	if !ok {
		return nil, false
	}
	var wrap CacheWrapper
	if err := json.Unmarshal(b, &wrap); err == nil && wrap.Timestamp > 0 {
		days := getConfigInt("cache_days", 180)
		if time.Now().Unix()-wrap.Timestamp > int64(days*86400) {
			return nil, false
		}
		return wrap.Payload, true
	}
	return b, true
}

func (db *DB) SetAlbumLock(albumName, artistName string) {
	lockKey := fmt.Sprintf("album_lock:%s:%s", cleanSearchTerm(albumName), cleanSearchTerm(artistName))
	host.KVStoreSet(lockKey, []byte(fmt.Sprintf("%d", time.Now().Unix())))
}

func (db *DB) IsAlbumLocked(albumName, artistName string) bool {
	lockKey := fmt.Sprintf("album_lock:%s:%s", cleanSearchTerm(albumName), cleanSearchTerm(artistName))
	if lockData, ok, _ := host.KVStoreGet(lockKey); ok {
		var ts int64
		fmt.Sscanf(string(lockData), "%d", &ts)
		if time.Now().Unix()-ts < 180 {
			return true
		}
	}
	return false
}

func stringHash(s string) int {
	h := 0
	for i := 0; i < len(s); i++ {
		h = 31*h + int(s[i])
	}
	if h < 0 {
		h = -h
	}
	return h
}

func (db *DB) AcquireGlobalWorkerSlot(albumName string, maxWorkers int, timeoutSeconds int) int {
	startTime := time.Now().Unix()
	
	myTicket := fmt.Sprintf("%d_%d", time.Now().UnixNano(), stringHash(albumName))

	for {
		for i := 1; i <= maxWorkers; i++ {
			slotKey := fmt.Sprintf("worker_slot:%d", i)
			
			if lockData, ok, _ := host.KVStoreGet(slotKey); ok {
				var lockTicket string
				var ts int64
				fmt.Sscanf(string(lockData), "%d|%s", &ts, &lockTicket)
				
				if time.Now().Unix()-ts < 90 {
					continue
				}
			}
			
			lockVal := fmt.Sprintf("%d|%s", time.Now().Unix(), myTicket)
			host.KVStoreSet(slotKey, []byte(lockVal))
			
			delay := time.Duration(600 + (stringHash(myTicket) % 200)) * time.Millisecond
			time.Sleep(delay)
			
			if checkData, ok, _ := host.KVStoreGet(slotKey); ok {
				if string(checkData) == lockVal {
					return i
				}
			}
		}

		if time.Now().Unix()-startTime > int64(timeoutSeconds) {
			return -1
		}

		jitter := time.Duration((stringHash(albumName)%1000)+500) * time.Millisecond
		time.Sleep(jitter)
	}
}

func (db *DB) ReleaseGlobalWorkerSlot(slotID int) {
	if slotID > 0 {
		slotKey := fmt.Sprintf("worker_slot:%d", slotID)
		host.KVStoreSet(slotKey, []byte("0|empty"))
	}
}

func (db *DB) SetRadioTracks(artistName string, tracks []metadata.SongRef) {
	key := fmt.Sprintf("radio_tracks_%s", cleanSearchTerm(artistName))
	data, _ := json.Marshal(tracks)
	
	wrap := CacheWrapper{
		Timestamp: time.Now().Unix(),
		Payload:   data,
	}
	b, _ := json.Marshal(wrap)
	host.KVStoreSet(key, b)
}

func (db *DB) GetRadioTracks(artistName string) ([]metadata.SongRef, bool) {
	key := fmt.Sprintf("radio_tracks_%s", cleanSearchTerm(artistName))
	b, ok, _ := host.KVStoreGet(key)
	if !ok { return nil, false }

	var wrap CacheWrapper
	if err := json.Unmarshal(b, &wrap); err == nil && wrap.Timestamp > 0 {
		if time.Now().Unix()-wrap.Timestamp < 60 {
			var tracks []metadata.SongRef
			if err := json.Unmarshal(wrap.Payload, &tracks); err == nil {
				return tracks, true
			}
		}
	}
	return nil, false
}

func (db *DB) SetRadioSeedLock(artistName string) {
	key := fmt.Sprintf("radio_seed_lock_%s", cleanSearchTerm(artistName))
	host.KVStoreSet(key, []byte(fmt.Sprintf("%d", time.Now().Unix())))
}

func (db *DB) IsRadioSeedLocked(artistName string) bool {
	key := fmt.Sprintf("radio_seed_lock_%s", cleanSearchTerm(artistName))
	if data, ok, _ := host.KVStoreGet(key); ok {
		var ts int64
		fmt.Sscanf(string(data), "%d", &ts)
		if time.Now().Unix()-ts < 60 {
			return true
		}
	}
	return false
}