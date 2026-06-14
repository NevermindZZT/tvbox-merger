package database

import (
	"os"
	"path/filepath"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"tvbox-merger/internal/config"
	"tvbox-merger/internal/model"
)

var DB *gorm.DB

func Init(cfg *config.Config) (*gorm.DB, error) {
	dataDir := filepath.Dir(cfg.DataSourcePath)
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, err
	}

	db, err := gorm.Open(sqlite.Open(cfg.DataSourcePath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, err
	}

	if err := db.AutoMigrate(
		&model.Group{},
		&model.Source{},
		&model.Cache{},
		&model.MergedResult{},
	); err != nil {
		return nil, err
	}

	DB = db
	return db, nil
}

func EnsureCacheDir(cacheDir string) error {
	return os.MkdirAll(cacheDir, 0755)
}

// ─── Group CRUD ────────────────────────────────────────────────

func CreateGroup(db *gorm.DB, g *model.Group) error {
	return db.Create(g).Error
}

func GetAllGroups(db *gorm.DB) ([]model.Group, error) {
	var groups []model.Group
	err := db.Order("id ASC").Find(&groups).Error
	return groups, err
}

func GetGroupByID(db *gorm.DB, id uint) (*model.Group, error) {
	var g model.Group
	err := db.First(&g, id).Error
	return &g, err
}

func GetGroupBySlug(db *gorm.DB, slug string) (*model.Group, error) {
	var g model.Group
	err := db.Where("slug = ?", slug).First(&g).Error
	return &g, err
}

func UpdateGroup(db *gorm.DB, g *model.Group) error {
	return db.Save(g).Error
}

func DeleteGroup(db *gorm.DB, id uint) error {
	// Delete all sources, caches, and merged results for this group
	db.Where("source_id IN (SELECT id FROM sources WHERE group_id = ?)", id).Delete(&model.Cache{})
	db.Where("group_id = ?", id).Delete(&model.Source{})
	db.Where("group_id = ?", id).Delete(&model.MergedResult{})
	return db.Delete(&model.Group{}, id).Error
}

// EnsureDefaultGroup creates a default group if none exist
func EnsureDefaultGroup(db *gorm.DB) {
	var count int64
	db.Model(&model.Group{}).Count(&count)
	if count == 0 {
		db.Create(&model.Group{Name: "默认分组", Slug: "default"})
	}
}

// ─── Source CRUD ───────────────────────────────────────────────

func CreateSource(db *gorm.DB, s *model.Source) error {
	return db.Create(s).Error
}

func GetAllSources(db *gorm.DB) ([]model.Source, error) {
	var sources []model.Source
	err := db.Order("group_id ASC, sort_order ASC, id ASC").Find(&sources).Error
	return sources, err
}

func GetSourcesByGroup(db *gorm.DB, groupID uint) ([]model.Source, error) {
	var sources []model.Source
	err := db.Where("group_id = ?", groupID).Order("sort_order ASC, id ASC").Find(&sources).Error
	return sources, err
}

func GetEnabledSourcesByGroup(db *gorm.DB, groupID uint) ([]model.Source, error) {
	var sources []model.Source
	err := db.Where("group_id = ? AND enabled = ?", groupID, true).Order("sort_order ASC, id ASC").Find(&sources).Error
	return sources, err
}

func GetSourceByID(db *gorm.DB, id uint) (*model.Source, error) {
	var s model.Source
	err := db.First(&s, id).Error
	return &s, err
}

func UpdateSource(db *gorm.DB, s *model.Source) error {
	return db.Save(s).Error
}

func DeleteSource(db *gorm.DB, id uint) error {
	db.Where("source_id = ?", id).Delete(&model.Cache{})
	return db.Delete(&model.Source{}, id).Error
}

// ─── Cache CRUD ────────────────────────────────────────────────

func GetCacheBySourceID(db *gorm.DB, sourceID uint) (*model.Cache, error) {
	var c model.Cache
	err := db.Where("source_id = ?", sourceID).First(&c).Error
	return &c, err
}

func UpsertCache(db *gorm.DB, c *model.Cache) error {
	var existing model.Cache
	result := db.Where("source_id = ?", c.SourceID).First(&existing)
	if result.Error != nil {
		return db.Create(c).Error
	}
	c.ID = existing.ID
	return db.Model(&existing).Updates(map[string]interface{}{
		"raw_bytes":   c.RawBytes,
		"content_str": c.ContentStr,
		"fetched_at":  c.FetchedAt,
		"status_code": c.StatusCode,
		"is_valid":    c.IsValid,
	}).Error
}

func InvalidateCache(db *gorm.DB, sourceID uint) error {
	return db.Model(&model.Cache{}).Where("source_id = ?", sourceID).Update("is_valid", false).Error
}

// ─── MergedResult ──────────────────────────────────────────────

func GetMergedResultByGroup(db *gorm.DB, groupID uint) (*model.MergedResult, error) {
	var m model.MergedResult
	err := db.Where("group_id = ?", groupID).First(&m).Error
	return &m, err
}

func SaveMergedResult(db *gorm.DB, groupID uint, content string, sourceCount int) error {
	var m model.MergedResult
	result := db.Where("group_id = ?", groupID).First(&m)
	if result.Error != nil {
		return db.Create(&model.MergedResult{
			GroupID:     groupID,
			Content:     content,
			SourceCount: sourceCount,
		}).Error
	}
	return db.Model(&m).Updates(map[string]interface{}{
		"content":      content,
		"source_count": sourceCount,
	}).Error
}

// GetGroupStatus returns all groups with their merge status
func GetGroupStatus(db *gorm.DB) ([]model.GroupStatus, error) {
	groups, err := GetAllGroups(db)
	if err != nil {
		return nil, err
	}

	var statuses []model.GroupStatus
	for _, g := range groups {
		gs := model.GroupStatus{
			Group:   g,
			SlugURL: "/tvbox/" + g.Slug + ".json",
		}

		var srcCount int64
		db.Model(&model.Source{}).Where("group_id = ?", g.ID).Count(&srcCount)
		gs.SourceCount = int(srcCount)

		mr, err := GetMergedResultByGroup(db, g.ID)
		if err == nil {
			gs.LastMergeTime = mr.UpdatedAt.Format("2006-01-02 15:04:05")
		}

		statuses = append(statuses, gs)
	}
	return statuses, nil
}
