package handler

import (
	"github.com/dhikaarta/pay-gate-backend/pkg/database"
	"github.com/dhikaarta/pay-gate-backend/pkg/response"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

// HealthHandler handles liveness and readiness probe endpoints.
type HealthHandler struct {
	db *pgxpool.Pool
}

// NewHealthHandler constructs a HealthHandler.
func NewHealthHandler(db *pgxpool.Pool) *HealthHandler {
	return &HealthHandler{db: db}
}

// healthData is the payload returned by both health endpoints.
type healthData struct {
	Status string `json:"status"`
}

// Live godoc
//
//	@Summary		Liveness check
//	@Description	Returns 200 when the HTTP server is running.
//	@Tags			health
//	@Produce		json
//	@Success		200	{object}	response.successEnvelope{data=healthData}
//	@Router			/health [get]
func (h *HealthHandler) Live(c *gin.Context) {
	response.OK(c, healthData{Status: "ok"})
}

// Ready godoc
//
//	@Summary		Readiness check
//	@Description	Returns 200 when the database is reachable and the clean schema version is supported.
//	@Tags			health
//	@Produce		json
//	@Success		200	{object}	response.successEnvelope{data=healthData}
//	@Failure		503	{object}	response.errorEnvelope
//	@Router			/health/ready [get]
func (h *HealthHandler) notReady(c *gin.Context, code, message string) {
	c.JSON(503, gin.H{
		"success": false,
		"error": gin.H{
			"code":    code,
			"message": message,
		},
		"meta": gin.H{
			"request_id": c.GetString("request_id"),
		},
	})
}

func (h *HealthHandler) Ready(c *gin.Context) {
	if h.db == nil {
		h.notReady(c, "DATABASE_UNAVAILABLE", "Database is not ready")
		return
	}

	if err := database.Ping(c.Request.Context(), h.db); err != nil {
		h.notReady(c, "DATABASE_UNAVAILABLE", "Database is not ready")
		return
	}
	if err := database.CheckSchema(c.Request.Context(), h.db); err != nil {
		h.notReady(c, "DATABASE_NOT_READY", "Database schema is not ready")
		return
	}

	response.OK(c, healthData{Status: "ok"})
}
