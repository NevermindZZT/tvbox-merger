package auth

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

type AuthHandler struct {
	jwtSecret   []byte
	adminUser   string
	adminPass   string
}

func New(jwtSecret, adminUser, adminPass string) *AuthHandler {
	return &AuthHandler{
		jwtSecret: []byte(jwtSecret),
		adminUser: adminUser,
		adminPass: adminPass,
	}
}

type Claims struct {
	Username string `json:"username"`
	jwt.RegisteredClaims
}

func (a *AuthHandler) Login(c *gin.Context) {
	var req struct {
		Username string `json:"username" form:"username"`
		Password string `json:"password" form:"password"`
	}

	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}

	if req.Username != a.adminUser || req.Password != a.adminPass {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid credentials"})
		return
	}

	// Generate JWT
	expiration := time.Now().Add(24 * time.Hour)
	claims := &Claims{
		Username: req.Username,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(expiration),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "tvbox-merger",
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, err := token.SignedString(a.jwtSecret)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Token generation failed"})
		return
	}

	// Set cookie
	c.SetCookie("token", tokenStr, int(expiration.Unix()), "/", "", false, true)

	c.JSON(http.StatusOK, gin.H{
		"token":   tokenStr,
		"message": "Login successful",
	})
}

func (a *AuthHandler) Logout(c *gin.Context) {
	c.SetCookie("token", "", -1, "/", "", false, true)
	c.JSON(http.StatusOK, gin.H{"message": "Logged out"})
}

// Middleware checks JWT from cookie or Authorization header
func (a *AuthHandler) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		tokenStr := ""

		// Try cookie first
		cookie, err := c.Cookie("token")
		if err == nil && cookie != "" {
			tokenStr = cookie
		}

		// Try Authorization header
		if tokenStr == "" {
			auth := c.GetHeader("Authorization")
			if strings.HasPrefix(auth, "Bearer ") {
				tokenStr = strings.TrimPrefix(auth, "Bearer ")
			}
		}

		if tokenStr == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "No token provided"})
			c.Abort()
			return
		}

		claims := &Claims{}
		token, err := jwt.ParseWithClaims(tokenStr, claims, func(token *jwt.Token) (interface{}, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}
			return a.jwtSecret, nil
		})

		if err != nil || !token.Valid {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired token"})
			c.Abort()
			return
		}

		c.Set("username", claims.Username)
		c.Next()
	}
}
