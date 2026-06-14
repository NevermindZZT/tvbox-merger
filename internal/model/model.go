package model

import "time"

// Group represents a named merge rule group
type Group struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Name      string    `gorm:"size:255;not null" json:"name"`
	Slug      string    `gorm:"size:255;uniqueIndex;not null" json:"slug"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Source represents a TVBox JSON source configuration
type Source struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	GroupID   uint      `gorm:"index;not null" json:"group_id"`
	Name      string    `gorm:"size:255;not null" json:"name"`
	URL       string    `gorm:"size:1024;not null" json:"url"`
	Enabled   bool      `gorm:"default:true" json:"enabled"`
	SortOrder int       `gorm:"default:0" json:"sort_order"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Cache stores the raw fetched content for a source
type Cache struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	SourceID   uint      `gorm:"uniqueIndex;not null" json:"source_id"`
	RawBytes   []byte    `gorm:"type:blob" json:"-"`
	ContentStr string    `gorm:"type:text" json:"content_str"`
	FetchedAt  time.Time `json:"fetched_at"`
	StatusCode int       `gorm:"default:0" json:"status_code"`
	IsValid    bool      `gorm:"default:false" json:"is_valid"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// MergedResult stores the latest merged TVBox JSON for a group
type MergedResult struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	GroupID     uint      `gorm:"uniqueIndex;not null" json:"group_id"`
	Content     string    `gorm:"type:text" json:"content"`
	SourceCount int       `gorm:"default:0" json:"source_count"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// SourceStatus represents a source with its health info (for admin display)
type SourceStatus struct {
	Source
	GroupName      string `json:"group_name"`
	LastFetchTime  string `json:"last_fetch_time"`
	StatusCode     int    `json:"status_code"`
	CacheValid     bool   `json:"cache_valid"`
	Healthy        bool   `json:"healthy"`
}

// GroupStatus represents a group with its merge status
type GroupStatus struct {
	Group
	SourceCount   int    `json:"source_count"`
	LastMergeTime string `json:"last_merge_time"`
	SlugURL       string `json:"slug_url"`
}
