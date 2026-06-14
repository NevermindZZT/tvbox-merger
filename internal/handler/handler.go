package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	proxy     *http.Client
}

func newProxyClient(cfg *config.Config) *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
}

func SetupRoutes(r *gin.Engine, db *gorm.DB, cfg *config.Config) *scheduler.Scheduler {
	authHandler := auth.New(cfg.JWTSecret, cfg.AdminUsername, cfg.AdminPassword)
	sched := scheduler.New(db, cfg)

	h := &Handler{
		db:        db,
		cfg:       cfg,
		auth:      authHandler,
		scheduler: sched,
		proxy:     newProxyClient(cfg),
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
	// 代理模式：直接做 HTTP 反向代理，完全透传上游响应
	group, err := database.GetGroupByID(h.db, groupID)
	if err == nil && group.ProxyMode {
		h.handleProxyMode(c, groupID)
		return
	}

	// 合并模式：从 SQLite 读取合并结果
	merged, err := database.GetMergedResultByGroup(h.db, groupID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "该分组暂无合并结果，请先在管理后台添加源并触发合并"})
		return
	}

	if !json.Valid([]byte(merged.Content)) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "合并源数据已损坏"})
		return
	}

	// 使用纯 application/json（不含 charset），添加 CORS 和缓存控制头，
	// 以匹配多数 TVBox 原始源的响应格式，避免客户端解析异常
	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
	c.Writer.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	c.Writer.Header().Set("ETag", fmt.Sprintf("tvbox-merger-%d", merged.UpdatedAt.Unix()))
	c.Writer.Header().Set("Last-Modified", merged.UpdatedAt.Format(http.TimeFormat))
	// 直接写裸 JSON 字节，Content-Length 由 Go HTTP 自动计算
	c.Writer.WriteHeader(http.StatusOK)
	c.Writer.Write([]byte(merged.Content))
}

// handleProxyMode 对上游源做完整的 HTTP 反向代理
// 1. 直接请求上游 URL
// 2. 拷贝所有上游响应头（跳逐跳头除外）
// 3. 流式写入上游响应体和状态码
// 4. 缓存到文件用于离线回退
func (h *Handler) handleProxyMode(c *gin.Context, groupID uint) {
	sources, err := database.GetEnabledSourcesByGroup(h.db, groupID)
	if err != nil || len(sources) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "该分组没有可用的源"})
		return
	}
	src := sources[0]

	// 获取缓存的 UA 或使用默认值
	ua := h.cfg.DefaultUA
	if ua == "" {
		ua = "okhttp/4.1.0"
	}

	// 构建上游请求
	req, err := http.NewRequest("GET", src.URL, nil)
	if err != nil {
		log.Printf("代理 [%d] 请求创建失败: %v", groupID, err)
		// 回退到缓存
		h.serveProxyFromCache(c, groupID)
		return
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "application/json, text/plain, */*")

	resp, err := h.proxy.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if err != nil {
			log.Printf("代理 [%d] 请求上游失败: %v", groupID, err)
		} else {
			log.Printf("代理 [%d] 上游返回 %d", groupID, resp.StatusCode)
		}
		if resp != nil {
			resp.Body.Close()
		}
		// 回退到缓存
		h.serveProxyFromCache(c, groupID)
		return
	}
	defer resp.Body.Close()

	// 读取上游响应体
	body, err := io.ReadAll(io.LimitReader(resp.Body, 50*1024*1024))
	if err != nil {
		log.Printf("代理 [%d] 读取响应体失败: %v", groupID, err)
		h.serveProxyFromCache(c, groupID)
		return
	}

	// 保存到文件缓存
	_ = database.SaveProxyCache(h.cfg.CacheDir, groupID, body, resp.Header, resp.StatusCode)

	// 拷贝所有上游响应头（跳逐跳头除外）
	hopByHop := map[string]bool{
		"Connection":          true,
		"Keep-Alive":          true,
		"Transfer-Encoding":   true,
		"TE":                  true,
		"Trailer":             true,
		"Upgrade":             true,
		"Proxy-Authorization": true,
		"Proxy-Authenticate":  true,
	}
	for k, v := range resp.Header {
		if !hopByHop[k] {
			c.Writer.Header()[k] = v
		}
	}
	// 确保 Content-Type 和 CORS 头存在
	if c.Writer.Header().Get("Content-Type") == "" {
		c.Writer.Header().Set("Content-Type", "application/json")
	}
	if c.Writer.Header().Get("Access-Control-Allow-Origin") == "" {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
	}

	// 写入上游的状态码和响应体
	c.Writer.WriteHeader(resp.StatusCode)
	c.Writer.Write(body)

	log.Printf("代理 [%d] 透传完成: %s (%d 字节)", groupID, src.Name, len(body))
}

// serveProxyFromCache 从文件缓存中恢复上游响应
func (h *Handler) serveProxyFromCache(c *gin.Context, groupID uint) {
	if !database.ProxyCacheExists(h.cfg.CacheDir, groupID) {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "上游不可用且无缓存"})
		return
	}

	body, meta, err := database.LoadProxyCache(h.cfg.CacheDir, groupID)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "缓存读取失败"})
		return
	}

	// 恢复缓存的响应头
	hopByHop := map[string]bool{
		"Connection":          true,
		"Keep-Alive":          true,
		"Transfer-Encoding":   true,
		"TE":                  true,
		"Trailer":             true,
		"Upgrade":             true,
		"Proxy-Authorization": true,
		"Proxy-Authenticate":  true,
	}
	for k, v := range meta.Headers {
		if !hopByHop[k] {
			c.Writer.Header()[k] = v
		}
	}
	if c.Writer.Header().Get("Content-Type") == "" {
		c.Writer.Header().Set("Content-Type", "application/json")
	}
	c.Writer.Header().Set("Access-Control-Allow-Origin", "*")

	statusCode := meta.StatusCode
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	c.Writer.WriteHeader(statusCode)
	c.Writer.Write(body)

	log.Printf("代理 [%d] 从缓存恢复 (%d 字节)", groupID, len(body))
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
		Name      string `form:"name" json:"name" binding:"required"`
		Slug      string `form:"slug" json:"slug" binding:"required"`
		ProxyMode bool   `form:"proxy_mode" json:"proxy_mode"`
	}
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "分组名称和标识不能为空"})
		return
	}

	g := &model.Group{
		Name:      req.Name,
		Slug:      req.Slug,
		ProxyMode: req.ProxyMode,
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
		Name      string `form:"name" json:"name"`
		Slug      string `form:"slug" json:"slug"`
		ProxyMode *bool  `form:"proxy_mode" json:"proxy_mode"`
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
	if req.ProxyMode != nil {
		g.ProxyMode = *req.ProxyMode
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

	// 检查分组是否为代理模式：代理模式只能添加一个源
	group, err := database.GetGroupByID(h.db, uint(gid))
	if err == nil && group.ProxyMode {
		sources, err := database.GetEnabledSourcesByGroup(h.db, group.ID)
		if err == nil && len(sources) > 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "代理模式分组只能有一个源"})
			return
		}
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
