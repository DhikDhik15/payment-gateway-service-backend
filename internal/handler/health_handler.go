package handler

import (
	"net/http"

	"github.com/dhikaarta/pay-gate-backend/pkg/database"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

// HealthHandler handles liveness and readiness probe endpoints.
type HealthHandler struct {
	db *pgxpool.Pool
}

// NewHealthHandler constructs a HealthHandler. db may be nil when the
// readiness check is not required (e.g. in early boot).
func NewHealthHandler(db *pgxpool.Pool) *HealthHandler {
	return &HealthHandler{db: db}
}

// Live godoc
//
//	@Summary		Liveness check
//	@Description	Returns 200 when the HTTP server is running.
//	@Tags			health
//	@Produce		json
//	@Success		200	{object}	map[string]string
//	@Router			/health [get]
func (h *HealthHandler) Live(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// Ready godoc
//
//	@Summary		Readiness check
//	@Description	Returns 200 when the server is ready to serve traffic (database reachable).
//	@Tags			health
//	@Produce		json
//	@Success		200	{object}	map[string]string
//	@Failure		503	{object}	map[string]string
//	@Router			/health/ready [get]
func (h *HealthHandler) Ready(c *gin.Context) {
	if h.db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "database not initialised"})
		return
	}

	if err := database.Ping(c.Request.Context(), h.db); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status": "database unreachable",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
