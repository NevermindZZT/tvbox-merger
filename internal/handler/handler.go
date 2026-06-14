package handler

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"tvbox-merger/internal/auth"
	"tvbox-merger/internal/config"
	"tvbox-merger/internal/database"
	"tvbox-merger/internal/model"
	"tvbox-merger/internal/scheduler"
)

type Handler struct {
	db        *gorm.DB
	cfg       *config.Config
	auth      *auth.AuthHandler
	scheduler *scheduler.Scheduler
}

func SetupRoutes(r *gin.Engine, db *gorm.DB, cfg *config.Config) *scheduler.Scheduler {
	authHandler := auth.New(cfg.JWTSecret, cfg.AdminUsername, cfg.AdminPassword)
	sched := scheduler.New(db, cfg)

	h := &Handler{
		db:        db,
		cfg:       cfg,
		auth:      authHandler,
		scheduler: sched,
	}

	// Public routes
	r.GET("/", h.Index)
	r.GET("/tvbox.json", h.GetMergedSource)              // default group
	r.GET("/tvbox/:slug", h.GetMergedBySlug)              // named group (/tvbox/xxx.json or /tvbox/xxx)
	r.GET("/admin/login", h.LoginPage)
	r.POST("/admin/login", h.auth.Login)
	r.POST("/admin/logout", h.auth.Logout)

	// Admin routes (require authentication)
	admin := r.Group("/admin")
	admin.Use(h.auth.Middleware())
	{
		admin.GET("/", h.AdminDashboard)

		// Group management
		admin.POST("/groups", h.CreateGroup)
		admin.PUT("/groups/:id", h.UpdateGroup)
		admin.DELETE("/groups/:id", h.DeleteGroup)

		// Source management (under a group)
		admin.POST("/groups/:gid/sources", h.CreateSource)
		admin.PUT("/sources/:id", h.UpdateSource)
		admin.DELETE("/sources/:id", h.DeleteSource)
		admin.POST("/groups/:gid/refresh", h.TriggerRefresh)
		admin.GET("/status", h.SourceStatus)
	}

	return sched
}

// ─── Public Pages ──────────────────────────────────────────────

func (h *Handler) Index(c *gin.Context) {
	c.HTML(http.StatusOK, "index.html", gin.H{
		"title": "TVBox 源合并",
	})
}

func (h *Handler) LoginPage(c *gin.Context) {
	c.HTML(http.StatusOK, "login.html", gin.H{
		"title": "管理员登录",
	})
}

// GetMergedSource serves the merged TVBox JSON for the default group
func (h *Handler) GetMergedSource(c *gin.Context) {
	h.serveMergedByGroup(c, 1) // default group ID = 1
}

// GetMergedBySlug serves the merged TVBox JSON for a named group
func (h *Handler) GetMergedBySlug(c *gin.Context) {
	slug := c.Param("slug")
	// Strip .json extension if present
	slug = strings.TrimSuffix(slug, ".json")

	group, err := database.GetGroupBySlug(h.db, slug)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "分组不存在"})
		return
	}
	h.serveMergedByGroup(c, group.ID)
}

func (h *Handler) serveMergedByGroup(c *gin.Context, groupID uint) {
	merged, err := database.GetMergedResultByGroup(h.db, groupID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "该分组暂无合并结果，请先在管理后台添加源并触发合并"})
		return
	}

	if !json.Valid([]byte(merged.Content)) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "合并源数据已损坏"})
		return
	}

	c.Data(http.StatusOK, "application/json; charset=utf-8", []byte(merged.Content))
}

// ─── Admin Dashboard ───────────────────────────────────────────

func (h *Handler) AdminDashboard(c *gin.Context) {
	groupStatuses, err := database.GetGroupStatus(h.db)
	if err != nil {
		c.HTML(http.StatusInternalServerError, "admin.html", gin.H{"error": err.Error()})
		return
	}

	sources, err := database.GetAllSources(h.db)
	if err != nil {
		c.HTML(http.StatusInternalServerError, "admin.html", gin.H{"error": err.Error()})
		return
	}

	// Build map of group name by ID
	groupNames := make(map[uint]string)
	for _, gs := range groupStatuses {
		groupNames[gs.ID] = gs.Name
	}

	// Attach group name to source statuses
	var statuses []model.SourceStatus
	for _, src := range sources {
		status := model.SourceStatus{
			Source:    src,
			GroupName: groupNames[src.GroupID],
		}
		cache, err := database.GetCacheBySourceID(h.db, src.ID)
		if err == nil {
			status.LastFetchTime = cache.FetchedAt.Format("2006-01-02 15:04:05")
			status.StatusCode = cache.StatusCode
			status.CacheValid = cache.IsValid
			status.Healthy = cache.IsValid
		}
		statuses = append(statuses, status)
	}

	c.HTML(http.StatusOK, "admin.html", gin.H{
		"title":    "管理后台",
		"groups":   groupStatuses,
		"sources":  statuses,
	})
}

// ─── Group Management ──────────────────────────────────────────

func (h *Handler) CreateGroup(c *gin.Context) {
	var req struct {
		Name string `form:"name" json:"name" binding:"required"`
		Slug string `form:"slug" json:"slug" binding:"required"`
	}
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "分组名称和标识不能为空"})
		return
	}

	g := &model.Group{
		Name: req.Name,
		Slug: req.Slug,
	}
	if err := database.CreateGroup(h.db, g); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "分组已创建", "group": g})
}

func (h *Handler) UpdateGroup(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}

	g, err := database.GetGroupByID(h.db, uint(id))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "分组不存在"})
		return
	}

	var req struct {
		Name string `form:"name" json:"name"`
		Slug string `form:"slug" json:"slug"`
	}
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求无效"})
		return
	}

	if req.Name != "" {
		g.Name = req.Name
	}
	if req.Slug != "" {
		g.Slug = req.Slug
	}

	if err := database.UpdateGroup(h.db, g); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "分组已更新", "group": g})
}

func (h *Handler) DeleteGroup(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}

	// Don't allow deleting the default group (ID=1)
	if id == 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "不能删除默认分组"})
		return
	}

	if err := database.DeleteGroup(h.db, uint(id)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "分组已删除"})
}

// ─── Source Management ─────────────────────────────────────────

func (h *Handler) CreateSource(c *gin.Context) {
	gid, err := strconv.ParseUint(c.Param("gid"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的分组 ID"})
		return
	}

	var req struct {
		Name      string `form:"name" json:"name" binding:"required"`
		URL       string `form:"url" json:"url" binding:"required"`
		Enabled   bool   `form:"enabled" json:"enabled"`
		SortOrder int    `form:"sort_order" json:"sort_order"`
	}
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "名称和 URL 不能为空"})
		return
	}

	src := &model.Source{
		GroupID:   uint(gid),
		Name:      req.Name,
		URL:       req.URL,
		Enabled:   req.Enabled,
		SortOrder: req.SortOrder,
	}

	if err := database.CreateSource(h.db, src); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "源已创建", "source": src})
}

func (h *Handler) UpdateSource(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}

	src, err := database.GetSourceByID(h.db, uint(id))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "源不存在"})
		return
	}

	var req struct {
		Name      string `form:"name" json:"name"`
		URL       string `form:"url" json:"url"`
		Enabled   *bool  `form:"enabled" json:"enabled"`
		SortOrder *int   `form:"sort_order" json:"sort_order"`
	}
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求无效"})
		return
	}

	if req.Name != "" {
		src.Name = req.Name
	}
	if req.URL != "" {
		src.URL = req.URL
	}
	if req.Enabled != nil {
		src.Enabled = *req.Enabled
	}
	if req.SortOrder != nil {
		src.SortOrder = *req.SortOrder
	}

	if err := database.UpdateSource(h.db, src); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "源已更新", "source": src})
}

func (h *Handler) DeleteSource(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 ID"})
		return
	}

	if err := database.DeleteSource(h.db, uint(id)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "源已删除"})
}

// ─── Actions ───────────────────────────────────────────────────

func (h *Handler) TriggerRefresh(c *gin.Context) {
	gid, err := strconv.ParseUint(c.Param("gid"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的分组 ID"})
		return
	}

	go func() {
		log.Printf("手动刷新分组 %d", gid)
		if err := h.scheduler.TriggerMerge(uint(gid)); err != nil {
			log.Printf("刷新分组 %d 出错: %v", gid, err)
		}
	}()

	c.JSON(http.StatusOK, gin.H{"message": "刷新已开始"})
}

func (h *Handler) SourceStatus(c *gin.Context) {
	sources, err := database.GetAllSources(h.db)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var statuses []gin.H
	for _, src := range sources {
		s := gin.H{
			"id":      src.ID,
			"group_id": src.GroupID,
			"name":    src.Name,
			"url":     src.URL,
			"enabled": src.Enabled,
		}

		cache, err := database.GetCacheBySourceID(h.db, src.ID)
		if err == nil {
			s["last_fetch"] = cache.FetchedAt.Format("2006-01-02 15:04:05")
			s["status_code"] = cache.StatusCode
			s["cache_valid"] = cache.IsValid
			s["healthy"] = cache.IsValid
		} else {
			s["healthy"] = false
			s["cache_valid"] = false
		}

		statuses = append(statuses, s)
	}

	c.JSON(http.StatusOK, gin.H{"sources": statuses})
}
