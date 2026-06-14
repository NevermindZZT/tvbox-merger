package merger

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"gorm.io/gorm"

	"tvbox-merger/internal/config"
	"tvbox-merger/internal/database"
	"tvbox-merger/internal/fetcher"
	"tvbox-merger/internal/model"
)	

// Merger handles merging multiple TVBox sources
type Merger struct {
	db  *gorm.DB
	fet *fetcher.Fetcher
	cfg *config.Config
}

func New(db *gorm.DB, fet *fetcher.Fetcher, cfg *config.Config) *Merger {
	return &Merger{db: db, fet: fet, cfg: cfg}
}

// ─── Per-Group Merge ───────────────────────────────────────────

func (m *Merger) MergeAll(groupID ...uint) error {
	var targetGroups []uint
	if len(groupID) > 0 && groupID[0] > 0 {
		targetGroups = []uint{groupID[0]}
	} else {
		groups, err := database.GetAllGroups(m.db)
		if err != nil {
			return err
		}
		for _, g := range groups {
			targetGroups = append(targetGroups, g.ID)
		}
	}
	for _, gid := range targetGroups {
		if err := m.mergeGroup(gid); err != nil {
			log.Printf("分组 %d 合并失败: %v", gid, err)
		}
	}
	return nil
}

func (m *Merger) mergeGroup(groupID uint) error {
	sources, err := database.GetEnabledSourcesByGroup(m.db, groupID)
	if err != nil {
		return fmt.Errorf("读取分组 %d 源列表: %w", groupID, err)
	}
	if len(sources) == 0 {
		log.Printf("分组 %d: 没有启用的源", groupID)
		return nil
	}

	var validSources []model.Source
	type srcData struct {
		raw      map[string]json.RawMessage
		rawBytes []byte // 原始 JSON 字节（用于单源精确透传）
	}
	var rawJSONs []srcData

	for _, src := range sources {
		result := m.fet.Fetch(src.URL)

		if !result.Healthy {
			if ok := m.checkCacheFallback(src.ID); !ok {
				log.Printf("源 %s 已排除: 抓取失败且缓存无效", src.Name)
				continue
			}
			cache, err := database.GetCacheBySourceID(m.db, src.ID)
			if err != nil || !cache.IsValid {
				continue
			}
			validSources = append(validSources, src)
			parsed, parseErr := parseRawJSON([]byte(cache.ContentStr))
			if parseErr != nil {
				continue
			}
			rawJSONs = append(rawJSONs, srcData{raw: parsed, rawBytes: []byte(cache.ContentStr)})
			continue
		}

		validSources = append(validSources, src)
		parsed, parseErr := parseRawJSON(result.JSONContent)
		if parseErr != nil {
			log.Printf("源 %s JSON 解析失败: %v", src.Name, parseErr)
			_ = database.InvalidateCache(m.db, src.ID)
			continue
		}
		rawJSONs = append(rawJSONs, srcData{raw: parsed, rawBytes: result.JSONContent})
		_ = m.updateCache(src.ID, result)
	}

	if len(rawJSONs) == 0 {
		return fmt.Errorf("没有可用的源")
	}

	// 检查是否为代理模式
	group, _ := database.GetGroupByID(m.db, groupID)
	isProxy := group != nil && group.ProxyMode
	if isProxy {
		log.Printf("分组 %d 代理模式: 精确透传 %s", groupID, validSources[0].Name)
		// 代理模式下也保存文件缓存（给 handler 的 HTTP 反向代理用）
		if len(rawJSONs) > 0 {
			_ = database.SaveProxyCache(m.cfg.CacheDir, groupID, rawJSONs[0].rawBytes, nil, 200)
		}
	}

	var mergedBytes []byte

	// 单源或代理模式：使用原始字节精确透传（byte-for-byte 一致）
	if isProxy || len(rawJSONs) == 1 {
		mergedBytes = rawJSONs[0].rawBytes
	} else {
		jsonMaps := make([]map[string]json.RawMessage, len(rawJSONs))
		for i, sd := range rawJSONs {
			jsonMaps[i] = sd.raw
		}
		merged := mergeRawJSONs(jsonMaps)
		mergedBytes, err = json.MarshalIndent(merged, "", "  ")
		if err != nil {
			return fmt.Errorf("序列化合并结果: %w", err)
		}
	}

	if err := database.SaveMergedResult(m.db, groupID, string(mergedBytes), len(validSources)); err != nil {
		return fmt.Errorf("保存合并结果: %w", err)
	}

	log.Printf("分组 %d 合并完成: %d 个源, %d 字节", groupID, len(validSources), len(mergedBytes))
	return nil
}

// ─── 缓存管理 ─────────────────────────────────────────────────

func (m *Merger) checkCacheFallback(sourceID uint) bool {
	cache, err := database.GetCacheBySourceID(m.db, sourceID)
	if err != nil {
		return false
	}
	if !cache.IsValid {
		return false
	}
	if !json.Valid([]byte(cache.ContentStr)) {
		_ = database.InvalidateCache(m.db, sourceID)
		return false
	}
	return true
}

func (m *Merger) updateCache(sourceID uint, result fetcher.FetchResult) error {
	cache := &model.Cache{
		SourceID:   sourceID,
		RawBytes:   result.RawBytes,
		ContentStr: func() string {
			if result.JSONContent != nil {
				return string(result.JSONContent)
			}
			return ""
		}(),
		FetchedAt:  now(),
		StatusCode: result.StatusCode,
		IsValid:    result.Healthy,
	}
	return database.UpsertCache(m.db, cache)
}

func now() time.Time {
	return time.Now().UTC()
}

// ─── 透传合并核心 ─────────────────────────────────────────────

// parseRawJSON 将 JSON 解析为 map，保留所有字段
func parseRawJSON(raw []byte) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		if len(raw) > 3 && raw[0] == 0xEF && raw[1] == 0xBB && raw[2] == 0xBF {
			return parseRawJSON(raw[3:])
		}
		return nil, err
	}
	return m, nil
}

// mergeRawJSONs 将多个 TVBox JSON map 合并为一个。
// 保留第一个源的全部非数组字段（含顶层 spider 作为默认回退），
// 数组字段（sites/parses/lives/ads/flags/proxies）去重合并。
// 各源的 spider 会注入到该源中缺少 jar 的 type-3 站点上，
// 这样支持读取每个站点 jar 的客户端能使用各自源的正确 spider。
func mergeRawJSONs(jsons []map[string]json.RawMessage) map[string]json.RawMessage {
	if len(jsons) == 0 {
		return nil
	}

	// 提取每个源的 spider
	type srcInfo struct {
		spider string
		raw    map[string]json.RawMessage
	}
	var srcs []srcInfo
	for _, j := range jsons {
		var si srcInfo
		si.raw = j
		if spiderRaw, ok := j["spider"]; ok {
			json.Unmarshal(spiderRaw, &si.spider)
		}
		srcs = append(srcs, si)
	}

	// 合并结果：复制第一个源的全部非数组字段（含 spider 作为默认回退）
	merged := make(map[string]json.RawMessage)
	skipFields := map[string]bool{"sites": true, "parses": true, "lives": true,
		"ads": true, "flags": true, "proxies": true}
	for k, v := range srcs[0].raw {
		if !skipFields[k] {
			merged[k] = v
		}
	}

	// 合并数组字段
	arrayFields := []string{"sites", "parses", "lives", "ads", "flags", "proxies"}
	for _, field := range arrayFields {
		var allItems []json.RawMessage
		seen := make(map[string]bool)

		for _, src := range srcs {
			raw, ok := src.raw[field]
			if !ok || raw == nil {
				continue
			}
			var items []json.RawMessage
			if err := json.Unmarshal(raw, &items); err != nil {
				continue
			}

			// sites: 为缺少 jar 的 type-3 站点注入当前源的 spider
			if field == "sites" && src.spider != "" {
				items = injectSpider(items, src.spider)
			}

			for _, item := range items {
				key := dedupKey(field, item)
				if !seen[key] {
					seen[key] = true
					allItems = append(allItems, item)
				}
			}
		}

		if len(allItems) > 0 {
			b, _ := json.Marshal(allItems)
			merged[field] = b
		}
	}

	return merged
}

// injectSpider 为 spider 类型（type=3）但没有 jar 的站点注入当前源的 spider
func injectSpider(sites []json.RawMessage, spider string) []json.RawMessage {
	if spider == "" {
		return sites
	}
	result := make([]json.RawMessage, len(sites))
	copy(result, sites)

	for i, site := range sites {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(site, &m); err != nil {
			continue
		}
		// 已有 jar 的不处理
		if _, hasJar := m["jar"]; hasJar {
			continue
		}
		// 检查 type 是否为 3（兼容数字或字符串）
		if !isType3(m["type"]) {
			continue
		}
		jarJSON, _ := json.Marshal(spider)
		m["jar"] = jarJSON
		updated, _ := json.Marshal(m)
		result[i] = updated
	}
	return result
}

// isType3 判断 type 字段是否等于 3（支持数字和字符串）
func isType3(raw json.RawMessage) bool {
	if raw == nil {
		return false
	}
	var ti int
	if err := json.Unmarshal(raw, &ti); err == nil && ti == 3 {
		return true
	}
	var ts string
	if err := json.Unmarshal(raw, &ts); err == nil && ts == "3" {
		return true
	}
	return false
}

// dedupKey 根据字段类型计算去重 key
func dedupKey(field string, item json.RawMessage) string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(item, &obj); err != nil {
		return string(item)
	}
	switch field {
	case "sites":
		if key, ok := obj["key"]; ok {
			return string(key)
		}
	case "parses":
		return string(obj["name"]) + "|" + string(obj["url"])
	case "lives":
		if name, ok := obj["name"]; ok {
			return string(name)
		}
	}
	return string(item)
}
