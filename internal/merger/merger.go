package merger

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"gorm.io/gorm"

	"tvbox-merger/internal/database"
	"tvbox-merger/internal/fetcher"
	"tvbox-merger/internal/model"
)

// TVBoxJSON represents the full TVBox JSON structure
type TVBoxJSON struct {
	Wallpaper    string          `json:"wallpaper,omitempty"`
	Logo         string          `json:"logo,omitempty"`
	Spider       string          `json:"spider,omitempty"`
	WarningText  string          `json:"warningText,omitempty"`
	Sites        []SiteEntry     `json:"sites,omitempty"`
	Parses       []ParseEntry    `json:"parses,omitempty"`
	Lives        []LiveEntry     `json:"lives,omitempty"`
	IJK          json.RawMessage `json:"ijk,omitempty"`
	Ads          []string        `json:"ads,omitempty"`
	Flags        []string        `json:"flags,omitempty"`
	Proxies      []string        `json:"proxies,omitempty"`
	Extra        json.RawMessage `json:"-"` // Any extra fields we don't know about
}

// SiteEntry represents a site in TVBox JSON
type SiteEntry struct {
	Key         string          `json:"key"`
	Name        string          `json:"name"`
	Type        int             `json:"type,omitempty"`
	API         string          `json:"api,omitempty"`
	Searchable  *int            `json:"searchable,omitempty"`
	QuickSearch *int            `json:"quickSearch,omitempty"`
	Filterable  *int            `json:"filterable,omitempty"`
	Changeable  interface{}     `json:"changeable,omitempty"`
	Jar         string          `json:"jar,omitempty"`
	Ext         json.RawMessage `json:"ext,omitempty"`
	PlayerType  interface{}     `json:"playerType,omitempty"`
	Timeout     int             `json:"timeout,omitempty"`
	Style       json.RawMessage `json:"style,omitempty"`
}

// ParseEntry represents a parse source
type ParseEntry struct {
	Name string          `json:"name"`
	Type interface{}     `json:"type,omitempty"`
	URL  string          `json:"url"`
	Ext  json.RawMessage `json:"ext,omitempty"`
}

// LiveEntry represents a live TV source
type LiveEntry struct {
	Name       string          `json:"name"`
	Type       int             `json:"type,omitempty"`
	URL        string          `json:"url"`
	EPG        string          `json:"epg,omitempty"`
	Logo       string          `json:"logo,omitempty"`
	PlayerType int             `json:"playerType,omitempty"`
	Timeout    int             `json:"timeout,omitempty"`
	UA         string          `json:"ua,omitempty"`
	Ext        json.RawMessage `json:"ext,omitempty"`
}

// Merger handles merging multiple TVBox sources
type Merger struct {
	db  *gorm.DB
	fet *fetcher.Fetcher
}

func New(db *gorm.DB, fet *fetcher.Fetcher) *Merger {
	return &Merger{db: db, fet: fet}
}

// MergeAll fetches all enabled sources and merges them into one result.
// If groupID is 0, merges all groups; otherwise merges only that group.
func (m *Merger) MergeAll(groupID ...uint) error {
	var targetGroups []uint
	if len(groupID) > 0 && groupID[0] > 0 {
		targetGroups = []uint{groupID[0]}
	} else {
		groups, err := m.getAllGroupIDs()
		if err != nil {
			return err
		}
		targetGroups = groups
	}

	for _, gid := range targetGroups {
		if err := m.mergeGroup(gid); err != nil {
			log.Printf("Merge error for group %d: %v", gid, err)
		}
	}
	return nil
}

func (m *Merger) getAllGroupIDs() ([]uint, error) {
	groups, err := database.GetAllGroups(m.db)
	if err != nil {
		return nil, err
	}
	var ids []uint
	for _, g := range groups {
		ids = append(ids, g.ID)
	}
	return ids, nil
}

func (m *Merger) mergeGroup(groupID uint) error {
	sources, err := database.GetEnabledSourcesByGroup(m.db, groupID)
	if err != nil {
		return fmt.Errorf("get sources for group %d: %w", groupID, err)
	}

	if len(sources) == 0 {
		log.Printf("Group %d: no enabled sources to merge", groupID)
		return nil
	}

	var validSources []model.Source
	var allJSONs []TVBoxJSON

	for _, src := range sources {
		result := m.fetchWithCache(src)

		if !result.Healthy {
			// Try cache fallback
			if ok := m.checkCacheFallback(src.ID); !ok {
				log.Printf("Source %s (%s) excluded: fetch failed and cache invalid", src.Name, src.URL)
				continue
			}
			// Re-read from cache
			cache, err := database.GetCacheBySourceID(m.db, src.ID)
			if err != nil || !cache.IsValid {
				continue
			}
			validSources = append(validSources, src)
			tvbox, parseErr := parseTVBoxJSON([]byte(cache.ContentStr))
			if parseErr != nil {
				log.Printf("Cache parse error for %s: %v", src.Name, parseErr)
				continue
			}
			allJSONs = append(allJSONs, tvbox)
			continue
		}

		validSources = append(validSources, src)
		tvbox, parseErr := parseTVBoxJSON(result.JSONContent)
		if parseErr != nil {
			log.Printf("Parse error for %s: %v", src.Name, parseErr)
			// Invalidate cache
			database.InvalidateCache(m.db, src.ID)
			continue
		}
		allJSONs = append(allJSONs, tvbox)

		// Update cache
		_ = m.updateCache(src.ID, result)
	}

	if len(allJSONs) == 0 {
		return fmt.Errorf("no valid sources to merge")
	}

	// Merge all parsed JSONs
	merged := mergeTVBoxJSONs(allJSONs, validSources)

	// Serialize
	mergedBytes, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal merged: %w", err)
	}

	// Save to database (per group)
	if err := database.SaveMergedResult(m.db, groupID, string(mergedBytes), len(validSources)); err != nil {
		return fmt.Errorf("save merged result: %w", err)
	}

	log.Printf("Group %d merge complete: %d sources, %d sites, %d parses, %d lives",
		groupID, len(validSources), len(merged.Sites), len(merged.Parses), len(merged.Lives))
	return nil
}

// fetchWithCache tries to fetch a source, falls back to cache if fetch fails
func (m *Merger) fetchWithCache(src model.Source) fetcher.FetchResult {
	result := m.fet.Fetch(src.URL)
	if result.Healthy {
		return result
	}

	// Fetch failed, check cache
	log.Printf("Fetch failed for %s: %v, checking cache", src.Name, result.Err)
	return result
}

// checkCacheFallback verifies if a cached version is still usable
func (m *Merger) checkCacheFallback(sourceID uint) bool {
	cache, err := database.GetCacheBySourceID(m.db, sourceID)
	if err != nil {
		return false
	}

	if !cache.IsValid {
		return false
	}

	// Verify cached content is valid JSON
	if !json.Valid([]byte(cache.ContentStr)) {
		database.InvalidateCache(m.db, sourceID)
		return false
	}

	return true
}

func (m *Merger) updateCache(sourceID uint, result fetcher.FetchResult) error {
	cache := &model.Cache{
		SourceID: sourceID,
		RawBytes: result.RawBytes,
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

// mergeTVBoxJSONs merges multiple TVBox JSONs into one
func mergeTVBoxJSONs(jsons []TVBoxJSON, sources []model.Source) TVBoxJSON {
	if len(jsons) == 0 {
		return TVBoxJSON{}
	}

	merged := jsons[0]

	// Keep track of seen keys for dedup
	seenSites := make(map[string]bool)
	seenParses := make(map[string]bool)
	seenLives := make(map[string]bool)
	seenAds := make(map[string]bool)
	seenFlags := make(map[string]bool)
	seenProxies := make(map[string]bool)

	// Mark entries from the first source
	for _, s := range merged.Sites {
		if s.Key != "" {
			seenSites[s.Key] = true
		}
	}
	for _, p := range merged.Parses {
		key := p.Name + "|" + p.URL
		seenParses[key] = true
	}
	for _, l := range merged.Lives {
		seenLives[l.Name] = true
	}
	for _, a := range merged.Ads {
		seenAds[a] = true
	}
	for _, f := range merged.Flags {
		seenFlags[f] = true
	}
	for _, p := range merged.Proxies {
		seenProxies[p] = true
	}

	// Merge from other sources
	for i := 1; i < len(jsons); i++ {
		other := jsons[i]

		// Sites: dedup by key
		for _, s := range other.Sites {
			if !seenSites[s.Key] {
				merged.Sites = append(merged.Sites, s)
				seenSites[s.Key] = true
			}
		}

		// Parses: dedup by name+url
		for _, p := range other.Parses {
			key := p.Name + "|" + p.URL
			if !seenParses[key] {
				merged.Parses = append(merged.Parses, p)
				seenParses[key] = true
			}
		}

		// Lives: dedup by name
		for _, l := range other.Lives {
			if !seenLives[l.Name] {
				merged.Lives = append(merged.Lives, l)
				seenLives[l.Name] = true
			}
		}

		// Ads: simple dedup
		for _, a := range other.Ads {
			if !seenAds[a] {
				merged.Ads = append(merged.Ads, a)
				seenAds[a] = true
			}
		}

		// Flags
		for _, f := range other.Flags {
			if !seenFlags[f] {
				merged.Flags = append(merged.Flags, f)
				seenFlags[f] = true
			}
		}

		// Proxies
		for _, p := range other.Proxies {
			if !seenProxies[p] {
				merged.Proxies = append(merged.Proxies, p)
				seenProxies[p] = true
			}
		}

		// Top-level fields: only set if not already set
		if merged.Wallpaper == "" && other.Wallpaper != "" {
			merged.Wallpaper = other.Wallpaper
		}
		if merged.Logo == "" && other.Logo != "" {
			merged.Logo = other.Logo
		}
		if merged.Spider == "" && other.Spider != "" {
			merged.Spider = other.Spider
		}
		if merged.WarningText == "" && other.WarningText != "" {
			merged.WarningText = other.WarningText
		}
		if merged.IJK == nil && other.IJK != nil {
			merged.IJK = other.IJK
		}
	}

	return merged
}

// parseTVBoxJSON parses raw JSON bytes into TVBoxJSON struct
func parseTVBoxJSON(raw []byte) (TVBoxJSON, error) {
	var tvbox TVBoxJSON
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()

	if err := decoder.Decode(&tvbox); err != nil {
		return TVBoxJSON{}, fmt.Errorf("json decode: %w", err)
	}
	return tvbox, nil
}

func now() time.Time {
	return time.Now().UTC()
}
